"""Destructive behavior is tested only inside owned temporary fixtures."""
from __future__ import annotations

from contextlib import redirect_stderr, redirect_stdout
from dataclasses import replace
from datetime import datetime
import io
import os
from pathlib import Path
import subprocess
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import ai_cache_cleanup as cleanup


class CleanupFixture(unittest.TestCase):
    def setUp(self) -> None:
        self.sandbox = tempfile.TemporaryDirectory(prefix='cache-cleanup-test-')
        self.root = Path(self.sandbox.name).resolve()
        self.home = self.root / 'home'
        self.temp = self.root / 'tmp'
        self.home.mkdir()
        self.temp.mkdir()
        self.future = time.time() + 30 * 86400
        self.idle = cleanup.Usage(frozenset())

    def tearDown(self) -> None:
        self.sandbox.cleanup()

    def file(self, relative: str, data: str = 'cached') -> Path:
        path = self.home / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(data)
        return path

    def select(self, rules: list[cleanup.Rule] | None = None,
               usage: cleanup.Usage | None = None) -> list[cleanup.Candidate]:
        items, _ = cleanup.select_candidates(
            rules if rules is not None else cleanup.rules_for(self.home, self.temp),
            usage if usage is not None else self.idle, self.future,
        )
        return items


class CleanupTests(CleanupFixture):
    def test_only_allowlisted_cache_is_removed(self) -> None:
        cache = self.file('.codex/cache/old.json')
        protected = [self.file('.codex/sessions/chat.jsonl'),
                     self.file('.claude/projects/chat.jsonl'),
                     self.file('.codex/auth.json'),
                     self.file('project/main.py'),
                     self.file('Library/Caches/JetBrains/GoLand/LocalHistory/data')]
        items = self.select()
        with redirect_stderr(io.StringIO()):
            _, count, failures = cleanup.execute(items, self.idle)
        self.assertEqual((count, failures), (1, 0))
        self.assertFalse(cache.exists())
        self.assertTrue(all(path.exists() for path in protected))

    def test_default_cli_does_not_delete_or_invoke_go(self) -> None:
        cache = self.file('.codex/cache/old.json')
        command = SimpleNamespace(stdout=str(self.temp), returncode=0)
        with patch.object(cleanup.Path, 'home', return_value=self.home), \
                patch.object(cleanup, 'capture_usage', return_value=self.idle), \
                patch.object(cleanup, 'local_go_rule') as go, \
                patch.object(cleanup.subprocess, 'run', return_value=command), \
                patch.object(cleanup.time, 'time', return_value=self.future), \
                redirect_stdout(io.StringIO()):
            self.assertEqual(cleanup.main([]), 0)
        go.assert_not_called()
        self.assertTrue(cache.exists())

    def test_failed_refresh_prevents_all_deletion(self) -> None:
        cache = self.file('.codex/cache/old.json')
        command = SimpleNamespace(stdout=str(self.temp), returncode=0)
        with patch.object(cleanup.Path, 'home', return_value=self.home), \
                patch.object(cleanup, 'capture_usage', side_effect=[self.idle, cleanup.UnsafePath('inventory failed')]), \
                patch.object(cleanup.subprocess, 'run', return_value=command), \
                patch.object(cleanup.time, 'time', return_value=self.future), \
                redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
            self.assertEqual(cleanup.main(['--apply']), 2)
        self.assertTrue(cache.exists())

    def test_recent_metadata_keeps_a_file_even_with_old_mtime(self) -> None:
        cache = self.file('.codex/cache/recent.json')
        os.utime(cache, (1, 1))
        items, _ = cleanup.select_candidates(
            cleanup.rules_for(self.home, self.temp), self.idle, time.time(),
        )
        self.assertEqual(items, [])
        self.assertTrue(cache.exists())

    def test_open_file_is_preserved(self) -> None:
        cache = self.file('.codex/cache/open.json')
        self.assertEqual(self.select(usage=cleanup.Usage(frozenset({str(cache)}))), [])

    def test_running_browser_keeps_its_cache(self) -> None:
        cache = self.file('Library/Caches/Codex/data')
        usage = cleanup.Usage(frozenset(), ('/Applications/ChatGPT.app/Contents/MacOS/ChatGPT',))
        self.assertEqual(self.select(usage=usage), [])
        self.assertTrue(cache.exists())

    def test_cache_root_symlink_never_reaches_project(self) -> None:
        target = self.file('project/data.json')
        parent = self.home / '.codex'
        parent.mkdir()
        (parent / 'cache').symlink_to(target.parent, target_is_directory=True)
        self.assertEqual(self.select(), [])
        self.assertEqual(target.read_text(), 'cached')

    def test_replaced_ancestor_does_not_delete_symlink_target(self) -> None:
        cache = self.file('.codex/cache/data.json')
        items = self.select()
        outside = self.file('project/data.json', 'keep')
        cache.parent.rename(cache.parent.with_name('original-cache'))
        cache.parent.symlink_to(outside.parent, target_is_directory=True)
        with redirect_stderr(io.StringIO()):
            _, _, failures = cleanup.execute(items, self.idle)
        self.assertEqual(failures, 1)
        self.assertEqual(outside.read_text(), 'keep')

    def test_replaced_file_is_skipped(self) -> None:
        cache = self.file('.codex/cache/data.json')
        items = self.select()
        other = self.file('replacement.json', 'new-data')
        os.replace(other, cache)
        with redirect_stderr(io.StringIO()):
            _, count, failures = cleanup.execute(items, self.idle)
        self.assertEqual((count, failures), (0, 0))
        self.assertEqual(cache.read_text(), 'new-data')

    def test_nested_symlink_is_unlinked_without_following(self) -> None:
        cache = self.file('.npm/_cacache/data')
        protected = self.file('project/important', 'keep')
        (cache.parent / 'project-link').symlink_to(protected.parent, target_is_directory=True)
        items = self.select()
        _, _, failures = cleanup.execute(items, self.idle)
        self.assertEqual(failures, 0)
        self.assertFalse(cache.parent.exists())
        self.assertEqual(protected.read_text(), 'keep')

    def test_temporary_directory_with_repository_is_preserved(self) -> None:
        project = self.temp / 'go-build123'
        project.mkdir()
        (project / '.git').write_text('gitdir: /unrelated')
        (project / 'source.py').write_text('keep')
        self.assertEqual(self.select(), [])
        self.assertTrue((project / 'source.py').exists())

    def test_failed_delete_is_not_reported_as_success(self) -> None:
        blocked = self.file('.codex/cache/blocked.json')
        good = self.file('.codex/cache/good.json')
        items = self.select()
        unlink = os.unlink

        def fail_one(name: str, *args: object, **kwargs: object) -> None:
            if name == blocked.name:
                raise PermissionError('fixture-denied')
            unlink(name, *args, **kwargs)

        with patch.object(cleanup.os, 'unlink', side_effect=fail_one), redirect_stderr(io.StringIO()):
            _, count, failures = cleanup.execute(items, self.idle)
        self.assertEqual((count, failures), (1, 1))
        self.assertTrue(blocked.exists())
        self.assertFalse(good.exists())

    def test_inventory_error_is_not_treated_as_idle(self) -> None:
        result = subprocess.CompletedProcess([], 1, stdout=b'', stderr=b'failed')
        with patch.object(cleanup.subprocess, 'run', return_value=result):
            with self.assertRaises(cleanup.UnsafePath):
                cleanup.capture_usage()

    def test_go_custom_cache_path_is_refused(self) -> None:
        result = subprocess.CompletedProcess([], 0, stdout='/some/project', stderr='')
        with patch.object(cleanup.shutil, 'which', return_value='/fake/go'), \
                patch.object(cleanup.subprocess, 'run', return_value=result):
            with self.assertRaises(cleanup.UnsafePath):
                cleanup.local_go_rule(self.home)

    def test_go_command_never_cleans_modules(self) -> None:
        cache = self.file('Library/Caches/go-build/object')
        rule = cleanup.Rule(cache.parent, 'Go fixture', 'go', 0, executable='/fake/go')
        item = self.select([rule])[0]

        def clean(argv: list[str], **kwargs: object) -> subprocess.CompletedProcess:
            self.assertEqual(argv, ['/fake/go', 'clean', '-cache'])
            self.assertEqual(kwargs['env']['GOTOOLCHAIN'], 'local')
            self.assertEqual(kwargs['env']['GOCACHE'], str(cache.parent))
            cache.unlink()
            return subprocess.CompletedProcess(argv, 0)

        with patch.object(cleanup.subprocess, 'run', side_effect=clean):
            _, count = cleanup.remove_candidate(item, self.idle)
        self.assertEqual(count, 1)


class ExtendedCleanupTests(CleanupFixture):
    def process(self, pid: int, executable: str) -> cleanup.Process:
        started = datetime.fromtimestamp(time.time() - 3 * 86400).strftime('%a %b %d %H:%M:%S %Y')
        return cleanup.Process(pid, 1, os.getuid(), started, executable)

    def preview(self) -> tuple[cleanup.Usage, str, Path]:
        executable = '/Library/Frameworks/Python.framework/Versions/3.15/Resources/Python.app/Contents/MacOS/Python'
        source = self.home / 'Downloads/mytab-mac-plugins/dist'
        source.mkdir(parents=True)
        log = self.file('go/src/Project/gin_api/.cache/python-3152-migration-20261027/http-server-54321.log')
        process = self.process(987654, executable)
        command = f'{executable} -u -m http.server 54321 --bind 127.0.0.1 --directory {source}'
        handles = (
            cleanup.Handle(process.pid, 'cwd', 'DIR', str(source.parent)),
            cleanup.Handle(process.pid, 'txt', 'REG', executable),
            cleanup.Handle(process.pid, '0', 'CHR', '/dev/null'),
            cleanup.Handle(process.pid, '1', 'REG', str(log)),
            cleanup.Handle(process.pid, '2', 'REG', str(log)),
            cleanup.Handle(process.pid, '3', 'IPv4', '127.0.0.1:54321', 'TCP', 'LISTEN'),
        )
        return cleanup.Usage(frozenset({str(log)}), (), handles, (process,)), command, log

    def test_lsof_protocol_parser_uses_real_field_layout(self) -> None:
        data = b'p504\0\nf8\0tIPv4\0PTCP\0n127.0.0.1:9000\0TST=LISTEN\0TQR=0\0TQS=0\0\n'
        self.assertEqual(cleanup.parse_handles(data), (cleanup.Handle(504, '8', 'IPv4', '127.0.0.1:9000', 'TCP', 'LISTEN'),))
        with self.assertRaises(cleanup.UnsafePath):
            cleanup.parse_handles(b'p504\0f8\0n/path\0')

    def test_process_parser_keeps_start_time_and_spaces(self) -> None:
        data = ' 123 1 501 Tue Oct 20 12:34:56 2026 /Applications/An App.app/bin/service\n'
        record = cleanup.parse_processes(data)[0]
        self.assertEqual((record.pid, record.started, record.executable), (123, 'Tue Oct 20 12:34:56 2026', '/Applications/An App.app/bin/service'))
        with self.assertRaises(cleanup.UnsafePath):
            cleanup.parse_processes('123 unknown')

    def test_revoked_macos_handle_is_preserved_as_nonpassive(self) -> None:
        handle = cleanup.parse_handles(b'p5700\0\nf0\0n(revoked)\0\n')[0]
        self.assertEqual(handle.kind, 'REVOKED')
        self.assertFalse(cleanup.passive_handle(handle))

    def test_stop_flag_without_apply_is_read_only(self) -> None:
        cache = self.file('.codex/cache/old.json')
        with patch.object(cleanup.Path, 'home', return_value=self.home), \
                patch.object(cleanup, 'capture_usage', return_value=self.idle), \
                patch.object(cleanup.subprocess, 'run', return_value=SimpleNamespace(stdout=str(self.temp))), \
                patch.object(cleanup, 'execute_services') as services, \
                patch.object(cleanup.os, 'kill') as kill, redirect_stdout(io.StringIO()):
            self.assertEqual(cleanup.main(['--stop-idle-services']), 0)
        services.assert_not_called()
        kill.assert_not_called()
        self.assertTrue(cache.exists())

    def test_apply_alone_never_stops_services(self) -> None:
        cache = self.file('.codex/cache/old.json')
        with patch.object(cleanup.Path, 'home', return_value=self.home), \
                patch.object(cleanup, 'capture_usage', return_value=self.idle), \
                patch.object(cleanup.subprocess, 'run', return_value=SimpleNamespace(stdout=str(self.temp))), \
                patch.object(cleanup.time, 'time', return_value=self.future), \
                patch.object(cleanup, 'execute_services') as services, redirect_stdout(io.StringIO()):
            self.assertEqual(cleanup.main(['--apply']), 0)
        services.assert_not_called()
        self.assertFalse(cache.exists())

    def test_both_switches_execute_selected_services(self) -> None:
        sentinel = object()
        with patch.object(cleanup.Path, 'home', return_value=self.home), \
                patch.object(cleanup, 'capture_usage', return_value=self.idle), \
                patch.object(cleanup, 'idle_services', return_value=[sentinel]), \
                patch.object(cleanup, 'show_services'), \
                patch.object(cleanup.subprocess, 'run', return_value=SimpleNamespace(stdout=str(self.temp))), \
                patch.object(cleanup, 'execute_services', return_value=(1, 0, 0, 0)) as services, \
                redirect_stdout(io.StringIO()):
            self.assertEqual(cleanup.main(['--apply', '--stop-idle-services']), 0)
        services.assert_called_once_with([sentinel], self.home)

    def test_empty_directory_rules_preserve_nonempty_and_recent(self) -> None:
        empty = self.temp / '.tmpABC123'
        empty.mkdir()
        full = self.temp / '.tmpDEF456'
        full.mkdir()
        (full / 'source.py').write_text('keep')
        items = self.select()
        self.assertEqual([i.path for i in items], [empty])
        # A file created after preview must never be recursively removed.
        (empty / 'new.py').write_text('keep')
        with self.assertRaises(cleanup.UnsafePath):
            cleanup.remove_candidate(items[0], self.idle)
        self.assertTrue((empty / 'new.py').exists())
        self.assertTrue((full / 'source.py').exists())

    def test_empty_directory_is_removed_with_rmdir(self) -> None:
        empty = self.temp / '.tmpABC123'
        empty.mkdir()
        item = self.select()[0]
        with patch.object(cleanup.shutil, 'rmtree') as recursive:
            cleanup.remove_candidate(item, self.idle)
        recursive.assert_not_called()
        self.assertFalse(empty.exists())

    def test_file_appearing_at_rmdir_is_preserved(self) -> None:
        empty = self.temp / '.tmpABC123'
        empty.mkdir()
        item = self.select()[0]
        original = os.rmdir

        def add_file(name: str, **kwargs: object) -> None:
            (empty / 'new-data').write_text('keep')
            original(name, **kwargs)

        with patch.object(cleanup.os, 'rmdir', side_effect=add_file):
            with self.assertRaises(cleanup.UnsafePath):
                cleanup.remove_candidate(item, self.idle)
        self.assertEqual((empty / 'new-data').read_text(), 'keep')

    def test_new_cache_scopes_and_python_guard(self) -> None:
        paths = [self.file('Library/Caches/pip/http-v2/download'),
                 self.file('.cache/node-compile-cache/bytecode')]
        wrapper = self.temp / 'yarn--123-0.5'
        wrapper.mkdir()
        (wrapper / 'node').symlink_to('/not-followed')
        items = self.select()
        self.assertEqual({i.path for i in items}, {paths[0].parent, paths[1].parent, wrapper})
        busy = cleanup.Usage(frozenset(), ('/usr/bin/python3',))
        self.assertNotIn(paths[0].parent, {i.path for i in self.select(usage=busy)})

    def test_preview_eligibility_and_connections(self) -> None:
        usage, command, _ = self.preview()
        with patch.object(cleanup, 'process_command', return_value=command):
            selected = cleanup.idle_services(usage, self.home, self.future)
            self.assertEqual(len(selected), 1)
            connection = cleanup.Handle(987654, '4', 'IPv4', '127.0.0.1:54321->127.0.0.1:12345', 'TCP', 'ESTABLISHED')
            self.assertEqual(cleanup.idle_services(replace(usage, handles=usage.handles+(connection,)), self.home, self.future), [])
        with patch.object(cleanup, 'process_command', return_value=command.replace('127.0.0.1', '0.0.0.0')):
            self.assertEqual(cleanup.idle_services(usage, self.home, self.future), [])
        with patch.object(cleanup, 'process_command', return_value=command):
            self.assertEqual(cleanup.idle_services(usage, self.home, time.time()), [])  # fresh log

    def test_other_project_and_non_detached_preview_are_preserved(self) -> None:
        usage, command, _ = self.preview()
        with patch.object(cleanup, 'process_command', return_value=command+'/other'):
            self.assertEqual(cleanup.idle_services(usage, self.home, self.future), [])
        with patch.object(cleanup, 'process_command', return_value=command):
            parented = replace(usage, records=(replace(usage.records[0], ppid=123),))
            self.assertEqual(cleanup.idle_services(parented, self.home, self.future), [])

    def test_old_crash_handler_requires_current_version_and_no_ipc(self) -> None:
        old = '/Applications/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Versions/1.0/Helpers/browser_crashpad_handler'
        new = old.replace('/1.0/', '/2.0/')
        process = self.process(987654, old)
        current = self.process(987655, new)
        handles = tuple(cleanup.Handle(process.pid, str(fd), 'CHR', '/dev/null') for fd in range(3))
        usage = cleanup.Usage(frozenset(), (), handles, (process, current))
        with patch.object(cleanup.os.path, 'isfile', side_effect=lambda path: path == new), \
                patch.object(cleanup.os.path, 'lexists', return_value=False):
            self.assertEqual([i.process.pid for i in cleanup.idle_services(usage, self.home, self.future)], [process.pid])
            self.assertEqual(cleanup.idle_services(replace(usage, records=(process,)), self.home, self.future), [])
            ipc = cleanup.Handle(process.pid, '3', 'PIPE', 'pipe')
            self.assertEqual(cleanup.idle_services(replace(usage, handles=handles+(ipc,)), self.home, self.future), [])
        with patch.object(cleanup.os.path, 'isfile', return_value=True), patch.object(cleanup.os.path, 'lexists', return_value=True):
            self.assertEqual(cleanup.idle_services(usage, self.home, self.future), [])

    def test_changed_process_identity_prevents_signal(self) -> None:
        usage, command, _ = self.preview()
        with patch.object(cleanup, 'process_command', return_value=command):
            item = cleanup.idle_services(usage, self.home, self.future)[0]
        changed = replace(usage, records=(replace(usage.records[0], started='Mon Oct 19 01:00:00 2026'),))
        with patch.object(cleanup, 'capture_usage', return_value=changed), \
                patch.object(cleanup, 'process_command', return_value=command), \
                patch.object(cleanup.time, 'time', return_value=self.future), \
                patch.object(cleanup.os, 'kill') as kill:
            with self.assertRaises(cleanup.UnsafePath):
                cleanup.stop_service(item, self.home)
        kill.assert_not_called()

    def test_successful_stop_sends_only_sigterm(self) -> None:
        usage, command, _ = self.preview()
        with patch.object(cleanup, 'process_command', return_value=command):
            item = cleanup.idle_services(usage, self.home, self.future)[0]
        with patch.object(cleanup, 'capture_usage', return_value=usage), \
                patch.object(cleanup, 'process_command', return_value=command), \
                patch.object(cleanup.time, 'time', return_value=self.future), \
                patch.object(cleanup, 'read_processes', return_value=()), \
                patch.object(cleanup.os, 'kill') as kill:
            cleanup.stop_service(item, self.home)
        kill.assert_called_once_with(item.process.pid, cleanup.signal.SIGTERM)

    def test_timeout_preserves_log_and_reports_failure(self) -> None:
        usage, command, log = self.preview()
        with patch.object(cleanup, 'process_command', return_value=command):
            item = cleanup.idle_services(usage, self.home, self.future)[0]
        with patch.object(cleanup, 'capture_usage', return_value=usage), \
                patch.object(cleanup, 'process_command', return_value=command), \
                patch.object(cleanup.time, 'time', return_value=self.future), \
                patch.object(cleanup.time, 'monotonic', side_effect=[0, 6]), \
                patch.object(cleanup, 'read_processes', return_value=usage.records), \
                patch.object(cleanup.os, 'kill') as kill, \
                patch.object(cleanup, 'clear_service_logs') as clear, redirect_stderr(io.StringIO()):
            self.assertEqual(cleanup.execute_services([item], self.home), (0, 0, 0, 1))
        kill.assert_called_once_with(item.process.pid, cleanup.signal.SIGTERM)
        clear.assert_not_called()
        self.assertTrue(log.exists())

    def test_service_log_cleanup_preserves_other_files(self) -> None:
        usage, command, log = self.preview()
        keep = log.parent / 'other-project.txt'
        keep.write_text('keep')
        with patch.object(cleanup, 'process_command', return_value=command):
            item = cleanup.idle_services(usage, self.home, self.future)[0]
        with patch.object(cleanup, 'capture_usage', return_value=self.idle):
            _, count = cleanup.clear_service_logs(item, self.home)
        self.assertEqual(count, 1)
        self.assertFalse(log.exists())
        self.assertEqual(keep.read_text(), 'keep')

    def test_service_log_removes_only_empty_parent(self) -> None:
        usage, command, log = self.preview()
        with patch.object(cleanup, 'process_command', return_value=command):
            item = cleanup.idle_services(usage, self.home, self.future)[0]
        with patch.object(cleanup, 'capture_usage', return_value=self.idle):
            self.assertEqual(cleanup.clear_service_logs(item, self.home)[1], 1)
        self.assertFalse(log.parent.exists())
        self.assertTrue(log.parent.parent.exists())

    def test_signal_permission_failure_preserves_logs(self) -> None:
        usage, command, log = self.preview()
        with patch.object(cleanup, 'process_command', return_value=command):
            item = cleanup.idle_services(usage, self.home, self.future)[0]
        with patch.object(cleanup, 'stop_service', side_effect=PermissionError('fixture')) as stop, \
                patch.object(cleanup, 'clear_service_logs') as clear, redirect_stderr(io.StringIO()):
            self.assertEqual(cleanup.execute_services([item], self.home), (0, 0, 0, 1))
        stop.assert_called_once()
        clear.assert_not_called()
        self.assertTrue(log.exists())

    def test_replaced_or_open_service_log_is_preserved(self) -> None:
        usage, command, log = self.preview()
        with patch.object(cleanup, 'process_command', return_value=command):
            item = cleanup.idle_services(usage, self.home, self.future)[0]
        with patch.object(cleanup, 'capture_usage', return_value=usage):
            with self.assertRaises(cleanup.UnsafePath):
                cleanup.clear_service_logs(item, self.home)
        replacement = self.file('replacement.log')
        os.replace(replacement, log)
        with patch.object(cleanup, 'capture_usage', return_value=self.idle):
            with self.assertRaises(cleanup.UnsafePath):
                cleanup.clear_service_logs(item, self.home)
        self.assertTrue(log.exists())


if __name__ == '__main__':
    unittest.main()

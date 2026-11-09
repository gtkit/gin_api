#!/usr/bin/env python3
"""macOS AI 缓存、临时文件和闲置服务工具：默认只预览。

本机已确认的解释器：Python 3.15.2
  /Library/Frameworks/Python.framework/Versions/3.15/bin/python3.15
脚本只使用标准库，最低 Python 3.11，不需要 pip install，不使用 sudo。

执行方法（先 cd 到此脚本所在目录，或把文件名换成完整路径）：
  python3 --version
  python3 ai_cache_cleanup.py --help
  python3 ai_cache_cleanup.py
      只扫描并汇总，不删除。首次使用先运行此命令。
  python3 ai_cache_cleanup.py --verbose
      只预览，并列出具体候选路径和跳过原因。
  python3 ai_cache_cleanup.py --apply
      永久删除符合条件的缓存、旧日志和临时文件；不停止服务。
  python3 ai_cache_cleanup.py --stop-idle-services
      仍然只预览；显示符合严格规则的闲置服务候选。
  python3 ai_cache_cleanup.py --apply --stop-idle-services
      清理文件，并停止已再次核对的闲置候选，清理其限定迁移日志。
  python3 ai_cache_cleanup.py --include-go-build
      仍然只预览，额外把 Go 构建缓存纳入检查。
  python3 ai_cache_cleanup.py --apply --include-go-build
      执行清理，并额外调用本机 go clean -cache；不删除 Go 模块依赖。
  python3 ai_cache_cleanup.py --apply --stop-idle-services --include-go-build
      同时执行上述三类操作。首次使用前应先运行不含 --apply 的预览。
  python3 ai_cache_cleanup.py --apply --verbose
      执行清理，并显示具体路径和处理结果。

如果当前终端尚未刷新 PATH，可始终使用本机完整解释器路径：
  /Library/Frameworks/Python.framework/Versions/3.15/bin/python3.15 ai_cache_cleanup.py

范围与限制：
  只处理代码中列明的 Codex/Claude、npm/npx、Composer、Homebrew 等缓存，
  以及 Yarn/Bun/JCEF/下载暂存、Node/V8 编译缓存和 pip 下载缓存。
  缓存一般至少保留 1 天，旧日志/安装暂存/新增临时文件至少 7 天。
  .tmpXXXXXX 只清理至少 7 天且真正为空的目录；非空目录保留。
  不扫描项目源码、聊天记录、登录配置、IDE 本地历史、已安装插件/主运行时。
  不跟随符号链接；保留正在使用、过新、权限异常或检查期间发生变化的项目。
  应先停止相关构建、关闭相关应用；程序运行时相应缓存会保守跳过。
  lsof 是检查时快照，不能阻止其他程序稍后打开文件；执行前会再次检查。
  --apply 不经过废纸篓。已删除日志不能自动恢复，缓存则由相应工具重新生成。
  “文件占用估算”和“磁盘可用空间变化”分别统计，二者不保证相等。
  不下载工具、不修改系统设置；缺少必要工具或用量检查失败时停止。

服务处理规则：
  默认列出本用户的 TCP 监听，不把没有连接等同于不需要。
  只允许两类至少 24 小时的候选：程序文件已不存在、无活动句柄且已有
  当前版本运行的旧 Codex crashpad；或本机 MyTab 的已知 Python 预览命令，
  仅绑定 127.0.0.1、无连接，且限定迁移日志至少 24 小时未更新。
  MyTab 路径为 ~/Downloads/mytab-mac-plugins/dist；日志仅限本机 gin_api
  的 .cache/python-<数字>-migration-<日期>/http-server-<端口>.log。
  系统、数据库、Docker、普通应用和不符合规则的预览服务全部保留。
  发送 SIGTERM 前复核 PID、启动时间、命令与句柄，最多等待 5 秒；
  不发送 SIGKILL，不修改开机项；停止失败时保留日志并返回非零。

退出码：0=完成（可能有保护性跳过）；1=部分删除失败；2=环境/参数检查失败；
130=用户中断，已完成的删除不会自动撤回。
"""
from __future__ import annotations

import argparse
from bisect import bisect_left
from collections import Counter, defaultdict
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import datetime
import errno
import hashlib
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import sys
import time
from typing import Iterator


class UnsafePath(Exception):
    """A candidate cannot be checked or removed within the allowed scope."""


@dataclass(frozen=True)
class Process:
    pid: int
    ppid: int
    uid: int
    started: str
    executable: str

    @property
    def started_at(self) -> float:
        return datetime.strptime(self.started, '%a %b %d %H:%M:%S %Y').timestamp()


@dataclass(frozen=True)
class Handle:
    pid: int
    fd: str
    kind: str
    name: str
    protocol: str = ''
    state: str = ''


@dataclass(frozen=True)
class Usage:
    open_paths: frozenset[str]
    processes: tuple[str, ...] = ()
    handles: tuple[Handle, ...] = ()
    records: tuple[Process, ...] = ()
    sorted_paths: tuple[str, ...] = field(init=False, repr=False, compare=False)

    def __post_init__(self) -> None:
        object.__setattr__(self, 'sorted_paths', tuple(sorted(self.open_paths)))

    def busy(self, path: Path, directory: bool) -> bool:
        name = os.path.realpath(path)
        prefix = name + os.sep
        index = bisect_left(self.sorted_paths, prefix)
        return name in self.open_paths or (directory and index < len(self.sorted_paths)
                                          and self.sorted_paths[index].startswith(prefix))

    def running(self, group: str) -> bool:
        names = {Path(p).name.lower() for p in self.processes}
        if group == 'go':
            return bool(names & {'go', 'gopls', 'compile', 'link', 'asm', 'cgo', 'golangci-lint'})
        if group == 'node':
            return bool(names & {'node', 'npm', 'npx'})
        if group == 'python':
            entries = (p.executable for p in self.records if p.pid != os.getpid()) if self.records else self.processes
            return any(re.fullmatch(r'(python[0-9.]*|pip[0-9.]*|uv)', Path(p).name, re.I) for p in entries)
        words = ('codex', 'chatgpt') if group == 'codex' else (group,)
        return any(word in process.lower() for process in self.processes for word in words)


def parse_processes(output: str) -> tuple[Process, ...]:
    records = []
    for line in output.splitlines():
        parts = line.split(None, 8)
        if len(parts) != 9:
            raise UnsafePath('ps 输出不完整')
        try:
            item = Process(int(parts[0]), int(parts[1]), int(parts[2]), ' '.join(parts[3:8]), parts[8])
            item.started_at
        except ValueError as exc:
            raise UnsafePath('ps 身份字段无效') from exc
        records.append(item)
    if not records:
        raise UnsafePath('ps 未返回进程')
    return tuple(records)


def read_processes() -> tuple[Process, ...]:
    result = subprocess.run(['/bin/ps', '-ww', '-axo', 'pid=,ppid=,uid=,lstart=,comm='],
                            capture_output=True, text=True, timeout=15, check=True,
                            env=dict(os.environ, LC_ALL='C'))
    return parse_processes(result.stdout)


def parse_handles(data: bytes) -> tuple[Handle, ...]:
    handles = []
    pid = 0
    fields: dict[str, str] = {}

    def finish() -> None:
        if fields:
            # macOS 对已撤销的终端句柄只返回 n(revoked)，没有 t 字段。
            # 保留该标记；它不能成为允许自动停止的被动句柄。
            if 't' not in fields and fields.get('n') == '(revoked)':
                fields['t'] = 'REVOKED'
            if not pid or not all(key in fields for key in ('f', 't', 'n')):
                raise UnsafePath('lsof 句柄字段不完整')
            handles.append(Handle(pid, fields['f'], fields['t'], fields['n'],
                                  fields.get('P', ''), fields.get('state', '')))

    for token in data.split(b'\0'):
        token = token.lstrip(b'\n')
        if not token:
            continue
        key, value = chr(token[0]), os.fsdecode(token[1:])
        if key == 'p':
            finish()
            fields = {}
            if not value.isdecimal() or int(value) <= 0:
                raise UnsafePath('lsof PID 无效')
            pid = int(value)
        elif key == 'f':
            finish()
            fields = {'f': value}
        elif key in {'t', 'n', 'P'}:
            fields[key] = value
        elif key == 'T' and value.startswith('ST='):
            fields['state'] = value[3:]
    finish()
    return tuple(handles)


def capture_usage(pid: int | None = None) -> Usage:
    """Read only process names and open paths, never command-line secrets."""
    command = ['/usr/sbin/lsof', '-nP', '-u', str(os.getuid()), '-F0pftnPT']
    if pid is not None:
        command.extend(['-a', '-p', str(pid)])
    result = subprocess.run(
        command,
        capture_output=True, timeout=60, check=False,
    )
    if result.returncode or result.stderr:
        raise UnsafePath('lsof 检查失败，不能确认使用情况；未执行删除')
    handles = parse_handles(result.stdout)
    paths = {os.path.realpath(handle.name) for handle in handles if handle.name.startswith('/')}
    if not paths:
        raise UnsafePath('lsof 未返回可验证的文件路径；未执行删除')
    records = read_processes()
    return Usage(frozenset(paths), tuple(p.executable for p in records), handles, records)


@dataclass(frozen=True)
class Rule:
    base: Path
    label: str
    mode: str = 'tree'  # tree, children, files, shallow-files, empty, go
    days: int = 7
    pattern: str = '.*'
    guard: str = ''
    executable: str = ''

    def allows(self, path: Path) -> bool:
        if '..' in path.parts or not path.is_absolute():
            return False
        if self.mode in {'tree', 'go'}:
            return path == self.base
        if self.mode in {'children', 'shallow-files', 'empty'} and path.parent != self.base:
            return False
        return path != self.base and path.is_relative_to(self.base) and bool(
            re.fullmatch(self.pattern, path.name)
        )


@contextmanager
def open_parent(path: Path) -> Iterator[int]:
    """Anchor each parent component without following a swapped symlink."""
    if not path.is_absolute() or '..' in path.parts or not path.name:
        raise UnsafePath('路径不是允许的绝对路径')
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in path.parent.parts[1:]:
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = child
        yield fd
    finally:
        os.close(fd)


def identity(info: os.stat_result) -> tuple[int, ...]:
    return (info.st_dev, info.st_ino, info.st_mode, info.st_size,
            info.st_mtime_ns, info.st_ctime_ns)


@dataclass(frozen=True)
class Snapshot:
    root: tuple[int, ...]
    digest: str
    allocated: int
    files: int
    newest: float
    directory: bool


def inspect_path(path: Path, uid: int) -> Snapshot:
    """Hash metadata only; never open cache, chat or credential contents."""
    digest = hashlib.sha256()
    allocated = files = 0
    newest = 0.0
    counted: set[tuple[int, int]] = set()
    with open_parent(path) as parent:
        initial = os.stat(path.name, dir_fd=parent, follow_symlinks=False)
        if stat.S_ISLNK(initial.st_mode):
            raise UnsafePath('符号链接')
        if not (stat.S_ISDIR(initial.st_mode) or stat.S_ISREG(initial.st_mode)):
            raise UnsafePath('非普通文件或目录')

        def add(name: str, info: os.stat_result) -> None:
            nonlocal allocated, files, newest
            if info.st_uid != uid or info.st_dev != initial.st_dev:
                raise UnsafePath('所有者不同或跨文件系统')
            if Path(name).name in {'.git', '.hg', '.svn'}:
                raise UnsafePath('包含版本库标识，按项目保留')
            digest.update(os.fsencode(name) + b'\0' + repr(identity(info)).encode() + b'\0')
            newest = max(newest, info.st_mtime, info.st_ctime)
            if stat.S_ISREG(info.st_mode):
                files += 1
                key = (info.st_dev, info.st_ino)
                if key not in counted:
                    counted.add(key)
                    allocated += info.st_blocks * 512

        add('.', initial)
        directory = stat.S_ISDIR(initial.st_mode)
        if directory:
            fd = os.open(path.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
            try:
                if identity(os.fstat(fd)) != identity(initial):
                    raise UnsafePath('扫描前目录已改变')
                for relative, dirs, names, current in os.fwalk('.', dir_fd=fd, follow_symlinks=False):
                    dirs.sort()
                    names.sort()
                    for name in dirs + names:
                        add(str(Path(relative) / name), os.stat(name, dir_fd=current, follow_symlinks=False))
            finally:
                os.close(fd)
    return Snapshot(identity(initial), digest.hexdigest(), allocated, files, newest, directory)


def candidate_paths(rule: Rule) -> Iterator[Path]:
    if rule.mode in {'tree', 'go'}:
        yield rule.base
        return
    with open_parent(rule.base) as parent:
        fd = os.open(rule.base.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        try:
            if rule.mode in {'children', 'shallow-files', 'empty'}:
                for name in sorted(os.listdir(fd)):
                    path = rule.base / name
                    if rule.allows(path):
                        yield path
            else:
                for relative, dirs, names, current in os.fwalk('.', dir_fd=fd, follow_symlinks=False):
                    if {'.git', '.hg', '.svn'} & set(dirs + names):
                        dirs[:] = []
                        continue
                    dirs.sort()
                    for name in sorted(names):
                        if name.lower() in {'index', 'the-real-index', 'lock', 'lockfile'}:
                            continue
                        if re.fullmatch(r'data_[0-9]+', name) or name.endswith(('.sqlite', '.sqlite-wal', '.sqlite-shm', '.db', '.db-wal', '.db-shm')):
                            continue
                        path = rule.base / relative / name
                        if rule.allows(path):
                            yield path
        finally:
            os.close(fd)


def rules_for(home: Path, temp: Path) -> list[Rule]:
    rules = [Rule(home / '.codex/cache', 'Codex 目录缓存', 'files', 1)]
    for relative, guard in [
        ('Library/Caches/Codex', 'codex'),
        ('Library/Application Support/Codex/GraphiteDawnCache', 'codex'),
        *[(f'Library/Application Support/Claude/{name}', 'claude') for name in
          ('Cache', 'Code Cache', 'GPUCache', 'DawnGraphiteCache', 'DawnWebGPUCache', 'GraphiteDawnCache')],
    ]:
        rules.append(Rule(home / relative, 'AI 应用缓存', 'files', 1, guard=guard))
    for relative in (
        'Library/Logs/com.openai.codex', 'Library/Logs/Claude', 'Library/Caches/claude-cli-nodejs',
        '.continue/logs', '.local/share/opencode/log',
    ):
        rules.append(Rule(home / relative, '旧诊断日志', 'files', 7))
    rules += [
        Rule(temp, 'Codex 临时资源', 'shallow-files', 1, r'\.com\.openai\.codex\.[A-Za-z0-9]+'),
        Rule(temp, '旧剪贴板临时图', 'shallow-files', 7, r'codex-clipboard-[a-f0-9-]+\.png'),
        Rule(temp, '旧 Go 临时构建', 'children', 1, r'go-build[0-9]+', 'go'),
        Rule(temp, '废弃浏览器下载', 'children', 7, r'playwright-download-[A-Za-z0-9]+'),
        Rule(home / '.cache/codex-runtimes', '旧 Codex 安装暂存', 'children', 7, r'codex-runtime-install-[A-Za-z0-9]+'),
        Rule(home / '.npm/_cacache', 'npm 下载缓存', days=1),
        Rule(home / '.npm/_npx', '旧 npx 临时包', 'children', 7, r'[a-f0-9]+', 'node'),
        Rule(home / '.npminstall_tarball', '旧 npm 压缩包'),
        Rule(home / '.npm/node-sass', '旧原生模块下载'),
        Rule(home / '.npm/_prebuilds', '旧原生模块下载'),
        Rule(home / '.npm/_logs', '旧 npm 日志', 'files', 7),
        Rule(home / 'Library/Caches/node-gyp', 'Node 编译头文件', 'children', 7, r'[0-9]+\.[0-9]+\.[0-9]+'),
        Rule(home / 'Library/Caches/golangci-lint', 'Go 分析缓存', 'children', 1, r'[a-f0-9]{2}', 'go'),
        Rule(home / 'Library/Caches/Homebrew/bootsnap', 'Homebrew 字节码缓存'),
        Rule(home / 'Library/Caches/Homebrew/api', '旧 Homebrew 元数据', 'shallow-files', 7),
        Rule(temp / 'node-compile-cache', 'Node 临时编译缓存', days=1),
        Rule(temp / f'v8-compile-cache-{os.getuid()}', '旧 V8 编译缓存', 'children', 7),
        Rule(home / '.cache/node-compile-cache', '旧 Node 编译缓存'),
        Rule(temp, '旧 Yarn 启动暂存', 'children', 7, r'yarn--[0-9]+-[0-9.]+'),
        Rule(temp, '旧 Bun 临时动态库', 'shallow-files', 7, rf'\.bun-{os.getuid()}-[a-f0-9]+\.(dylib|node)'),
        Rule(temp, '旧 JCEF 协议暂存', 'shallow-files', 7, r'jcef-p[0-9]+_scheme\.tmp'),
        Rule(temp, '旧下载暂存', 'shallow-files', 7, r'Alamofire_CFNetworkDownload_[A-Za-z0-9]+\.tmp'),
        Rule(temp, '旧空临时目录', 'empty', 7, r'\.tmp[A-Za-z0-9]{6}'),
        Rule(home / 'Library/Caches/pip/http-v2', 'pip 下载缓存', days=1, guard='python'),
        Rule(home / 'Library/Caches/pip/wheels', 'pip wheel 缓存', days=1, guard='python'),
    ]
    for name in ('files', 'vcs', 'repo'):
        rules.append(Rule(home / 'Library/Caches/composer' / name, 'Composer 下载缓存', 'children', 1))
    return rules


def local_go_rule(home: Path) -> Rule:
    executable = shutil.which('go')
    if not executable:
        raise UnsafePath('未安装 Go，跳过 Go 构建缓存')
    env = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off')
    result = subprocess.run([executable, 'env', 'GOCACHE'], cwd=home, env=env,
                            capture_output=True, text=True, timeout=20, check=True)
    allowed = home / 'Library/Caches/go-build'
    if result.stdout.strip() != str(allowed):
        raise UnsafePath('GOCACHE 不是本机标准缓存路径，拒绝清理自定义位置')
    return Rule(allowed, 'Go 构建缓存（显式选择）', 'go', 0, guard='go', executable=executable)


@dataclass(frozen=True)
class Candidate:
    path: Path
    rule: Rule
    snapshot: Snapshot


def empty_directory(path: Path) -> bool:
    with open_parent(path) as parent:
        info = os.stat(path.name, dir_fd=parent, follow_symlinks=False)
        if not stat.S_ISDIR(info.st_mode):
            return False
        fd = os.open(path.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        try:
            return not os.listdir(fd)
        finally:
            os.close(fd)


def select_candidates(rules: list[Rule], usage: Usage, now: float, verbose: bool = False) -> tuple[list[Candidate], Counter[str]]:
    selected = []
    skipped: Counter[str] = Counter()
    for rule in rules:
        if rule.guard and usage.running(rule.guard):
            skipped[f'{rule.label}：相关程序运行中'] += 1
            continue
        if verbose:
            print(f'扫描 {rule.base}', flush=True)
        try:
            for path in candidate_paths(rule):
                try:
                    if rule.mode == 'empty' and not empty_directory(path):
                        skipped['非空或非目录的临时目标'] += 1
                        continue
                    snap = inspect_path(path, os.getuid())
                    if rule.mode in {'files', 'shallow-files'} and snap.directory:
                        continue
                    if now - snap.newest < rule.days * 86400:
                        skipped['近期文件/目录'] += 1
                    elif usage.busy(path, snap.directory):
                        skipped['有打开文件'] += 1
                    else:
                        selected.append(Candidate(path, rule, snap))
                except FileNotFoundError:
                    skipped['扫描期间已消失'] += 1
                except (OSError, UnsafePath) as exc:
                    skipped[str(exc)] += 1
                    if verbose:
                        print(f'保留 {path}: {exc}', file=sys.stderr)
        except FileNotFoundError:
            continue
        except (OSError, UnsafePath) as exc:
            skipped[str(exc)] += 1
            if verbose:
                print(f'保留 {rule.base}: {exc}', file=sys.stderr)
    return selected, skipped


def remove_candidate(item: Candidate, usage: Usage) -> tuple[int, int]:
    if not item.rule.allows(item.path):
        raise UnsafePath('目标超出固定规则')
    if (item.rule.guard and usage.running(item.rule.guard)) or usage.busy(item.path, item.snapshot.directory):
        raise UnsafePath('使用情况已改变')
    current = inspect_path(item.path, os.getuid())
    if current != item.snapshot:
        raise UnsafePath('扫描后内容或身份已改变')
    if item.rule.mode == 'go':
        env = dict(os.environ, GOTOOLCHAIN='local', GOWORK='off', GOCACHE=str(item.path))
        env.pop('GOCACHEPROG', None)
        subprocess.run([item.rule.executable, 'clean', '-cache'], cwd=Path.home(), env=env,
                       capture_output=True, text=True, timeout=600, check=True)
        after = inspect_path(item.path, os.getuid())
        return max(0, current.allocated - after.allocated), max(0, current.files - after.files)
    with open_parent(item.path) as parent:
        latest = os.stat(item.path.name, dir_fd=parent, follow_symlinks=False)
        if identity(latest) != current.root:
            raise UnsafePath('删除前目标已改变')
        if item.rule.mode == 'empty':
            try:
                os.rmdir(item.path.name, dir_fd=parent)
            except OSError as exc:
                if exc.errno in {errno.ENOTEMPTY, errno.EEXIST}:
                    raise UnsafePath('临时目录已非空') from exc
                raise
        elif current.directory:
            if not shutil.rmtree.avoids_symlink_attacks:
                raise UnsafePath('当前 Python 不支持 fd 安全目录删除')
            shutil.rmtree(item.path.name, dir_fd=parent)
        else:
            os.unlink(item.path.name, dir_fd=parent)
    return current.allocated, current.files


CRASHPAD = re.compile(r'/Applications/ChatGPT\.app/Contents/Frameworks/Codex Framework\.framework/Versions/([0-9.]+)/Helpers/browser_crashpad_handler')
PYTHON_APP = re.compile(r'/Library/Frameworks/Python\.framework/Versions/[0-9.]+/Resources/Python\.app/Contents/MacOS/Python')


@dataclass(frozen=True)
class IdleService:
    process: Process
    kind: str
    command: str = ''
    port: int = 0
    logs: tuple[tuple[Path, tuple[int, ...]], ...] = ()


def process_command(pid: int) -> str:
    result = subprocess.run(['/bin/ps', '-ww', '-p', str(pid), '-o', 'command='],
                            capture_output=True, text=True, timeout=10, check=True)
    return result.stdout.strip()


def migration_log(path: Path, port: int, home: Path) -> Snapshot:
    base = home / 'go/src/Project/gin_api/.cache'
    if (path.parent.parent != base
            or not re.fullmatch(r'python-[0-9]+-migration-[0-9]{8}', path.parent.name)
            or path.name != f'http-server-{port}.log'):
        raise UnsafePath('预览日志不在已确认的迁移目录')
    snap = inspect_path(path, os.getuid())
    if snap.directory:
        raise UnsafePath('预览日志不是普通文件')
    return snap


def passive_handle(handle: Handle) -> bool:
    if handle.fd in {'cwd', 'rtd'}:
        return handle.kind == 'DIR'
    if handle.fd in {'txt', 'mem'}:
        return handle.kind == 'REG'
    return handle.fd in {'0', '1', '2'} and handle.kind == 'CHR' and handle.name == '/dev/null'


def idle_services(usage: Usage, home: Path, now: float) -> list[IdleService]:
    selected = []
    current_versions = {match[1] for p in usage.records
                        if p.uid == os.getuid() and (match := CRASHPAD.fullmatch(p.executable))
                        and os.path.isfile(p.executable)}
    by_pid: dict[int, list[Handle]] = defaultdict(list)
    for handle in usage.handles:
        by_pid[handle.pid].append(handle)
    source = home / 'Downloads/mytab-mac-plugins/dist'
    for process in usage.records:
        if process.uid != os.getuid() or process.ppid != 1 or now - process.started_at < 86400:
            continue
        handles = by_pid.get(process.pid, [])
        if not handles:
            continue
        crash = CRASHPAD.fullmatch(process.executable)
        if crash:
            if (current_versions - {crash[1]} and not os.path.lexists(process.executable)
                    and {'0', '1', '2'} <= {h.fd for h in handles}
                    and all(passive_handle(h) for h in handles)):
                selected.append(IdleService(process, '旧 Codex 崩溃报告进程'))
            continue
        if not PYTHON_APP.fullmatch(process.executable):
            continue
        try:
            command = process_command(process.pid)
            pattern = re.escape(process.executable) + r'(?: -u)? -m http\.server ([0-9]{1,5}) --bind 127\.0\.0\.1 --directory ' + re.escape(str(source))
            match = re.fullmatch(pattern, command)
            if not match or not source.is_dir():
                continue
            port = int(match[1])
            if not 1 <= port <= 65535:
                continue
            network = [h for h in handles if h.kind in {'IPv4', 'IPv6'}]
            if (len(network) != 1 or network[0].protocol != 'TCP'
                    or network[0].state != 'LISTEN' or network[0].name != f'127.0.0.1:{port}'):
                continue
            output = [h for h in handles if h.fd in {'1', '2'}]
            if {h.fd for h in output} != {'1', '2'} or any(h.kind != 'REG' for h in output):
                continue
            log_paths = {Path(h.name) for h in output}
            if len(log_paths) != 1:
                continue
            logs = []
            for path in log_paths:
                snap = migration_log(path, port, home)
                if now - snap.newest < 86400:
                    raise UnsafePath('预览日志近期仍有更新')
                logs.append((path, snap.root))
            if any(not (h in network or h in output or passive_handle(h)) for h in handles):
                continue
            selected.append(IdleService(process, '旧 MyTab 静态预览', command, port, tuple(logs)))
        except (OSError, UnsafePath, subprocess.SubprocessError):
            continue  # 缺少完整证据的服务仅展示，不纳入自动停止。
    return selected


def show_services(usage: Usage, services: list[IdleService]) -> None:
    names = {p.pid: Path(p.executable).name for p in usage.records}
    listeners: dict[int, set[str]] = defaultdict(set)
    for handle in usage.handles:
        if handle.protocol == 'TCP' and handle.state == 'LISTEN':
            listeners[handle.pid].add(handle.name)
    print(f'本用户 TCP 监听进程 {len(listeners)} 个（展示不代表可停止）：')
    for pid, addresses in sorted(listeners.items()):
        print(f'  PID {pid} {names.get(pid, "未知程序")}: {", ".join(sorted(addresses))}')
    print(f'符合严格停止规则的闲置候选 {len(services)} 个：')
    for item in services:
        print(f'  PID {item.process.pid}: {item.kind}' + (f'，端口 {item.port}' if item.port else ''))


def stop_service(item: IdleService, home: Path) -> None:
    # 针对目标 PID 读取句柄，但保留完整进程元数据来确认当前版本仍运行。
    fresh = capture_usage(item.process.pid)
    if item not in idle_services(fresh, home, time.time()):
        raise UnsafePath('进程身份、连接或日志已改变')
    os.kill(item.process.pid, signal.SIGTERM)
    deadline = time.monotonic() + 5
    while True:
        current = next((p for p in read_processes() if p.pid == item.process.pid), None)
        if current is None or current != item.process:
            return  # PID 已被重用时也不再对新进程发信号。
        if time.monotonic() >= deadline:
            raise TimeoutError('SIGTERM 后 5 秒仍未退出；保留日志，不升级信号')
        time.sleep(0.1)


def clear_service_logs(item: IdleService, home: Path) -> tuple[int, int]:
    if not item.logs:
        return 0, 0
    usage = capture_usage()
    removed = files = 0
    for path, before in item.logs:
        snap = migration_log(path, item.port, home)
        if snap.root[:2] != before[:2]:
            raise UnsafePath('退出后日志文件身份已改变')
        parent_info = path.parent.stat(follow_symlinks=False)
        size, count = remove_candidate(Candidate(path, Rule(path, '已停止服务日志', days=0), snap), usage)
        removed += size
        files += count
        with open_parent(path.parent) as parent:
            latest = os.stat(path.parent.name, dir_fd=parent, follow_symlinks=False)
            if (latest.st_dev, latest.st_ino) != (parent_info.st_dev, parent_info.st_ino):
                raise UnsafePath('日志目录身份已改变')
            try:
                os.rmdir(path.parent.name, dir_fd=parent)
            except OSError as exc:
                if exc.errno not in {errno.ENOTEMPTY, errno.EEXIST}:
                    raise
    return removed, files


def execute_services(services: list[IdleService], home: Path) -> tuple[int, int, int, int]:
    stopped = removed = files = failures = 0
    for item in services:
        try:
            stop_service(item, home)
            stopped += 1
            print(f'已停止 PID {item.process.pid}: {item.kind}', flush=True)
            size, count = clear_service_logs(item, home)
            removed += size
            files += count
        except (UnsafePath, FileNotFoundError, ProcessLookupError) as exc:
            print(f'保护性跳过 PID {item.process.pid}: {exc}', file=sys.stderr)
        except (OSError, subprocess.SubprocessError) as exc:
            failures += 1
            print(f'处理失败 PID {item.process.pid}: {exc}', file=sys.stderr)
    return stopped, removed, files, failures


def human_size(size: int) -> str:
    return f'{size / 1_000_000_000:.3f} GB' if abs(size) >= 1_000_000_000 else f'{size / 1_000_000:.2f} MB'


def execute(items: list[Candidate], usage: Usage, verbose: bool = False) -> tuple[int, int, int]:
    removed = files = failures = 0
    empty_removed = 0
    last_update = time.monotonic()
    for index, item in enumerate(items, 1):
        try:
            size, count = remove_candidate(item, usage)
            removed += size
            files += count
            empty_removed += item.rule.mode == 'empty'
            if verbose:
                print(f'已清理 {item.path}', flush=True)
        except (UnsafePath, FileNotFoundError) as exc:
            print(f'已保留 {item.path}: {exc}', file=sys.stderr)
        except (OSError, subprocess.SubprocessError) as exc:
            failures += 1
            print(f'失败 {item.path}: {exc}', file=sys.stderr)
        if time.monotonic() - last_update >= 5:
            print(f'进度 {index}/{len(items)}，已处理 {files} 个文件', flush=True)
            last_update = time.monotonic()
    if empty_removed:
        print(f'已清理空临时目录 {empty_removed} 个')
    return removed, files, failures


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--apply', action='store_true', help='实际永久删除；不加此项只预览')
    parser.add_argument('--include-go-build', action='store_true', help='额外纳入 Go 构建缓存；仍需 --apply 才执行')
    parser.add_argument('--stop-idle-services', action='store_true', help='停止严格核对的闲置服务；必须同时 --apply，否则只预览')
    parser.add_argument('--verbose', action='store_true', help='显示具体候选路径、扫描范围和跳过原因')
    args = parser.parse_args(argv)
    if sys.version_info < (3, 11) or sys.platform != 'darwin':
        parser.error('需要 macOS 和 Python 3.11+；本机使用 Python 3.15.2')
    if os.geteuid() == 0:
        parser.error('请使用普通用户执行，不要使用 sudo')
    home = Path.home().resolve()
    try:
        info = subprocess.run(['/usr/bin/getconf', 'DARWIN_USER_TEMP_DIR'], capture_output=True,
                              text=True, timeout=10, check=True)
        if not info.stdout.strip():
            raise UnsafePath('无法取得用户临时目录')
        temp = Path(info.stdout.strip()).resolve(strict=True)
        usage = capture_usage()
        services = idle_services(usage, home, time.time())
        show_services(usage, services)
        rules = rules_for(home, temp)
        if args.include_go_build:
            try:
                rules.append(local_go_rule(home))
            except (UnsafePath, OSError, subprocess.SubprocessError) as exc:
                print(f'未纳入 Go 缓存: {exc}', file=sys.stderr)
        print('正在扫描固定缓存范围……', flush=True)
        items, skipped = select_candidates(rules, usage, time.time(), args.verbose)
        groups: dict[str, list[int]] = defaultdict(lambda: [0, 0, 0])
        for item in items:
            groups[item.rule.label][0] += item.snapshot.files
            groups[item.rule.label][1] += item.snapshot.allocated
            groups[item.rule.label][2] += 1
            if args.verbose:
                print(f'候选 {item.path} ({human_size(item.snapshot.allocated)})')
        for label, (count, size, targets) in groups.items():
            print(f'{label}: {targets} 个目标、{count} 个文件，估算 {human_size(size)}')
        print(f'合计 {len(items)} 个目标，估算 {human_size(sum(i.snapshot.allocated for i in items))}')
        for reason, count in skipped.most_common():
            print(f'保护性跳过 {count}: {reason}')
        if not args.apply:
            print('预览结束，未删除或停止服务。清理加 --apply；停止候选再加 --stop-idle-services；Go 缓存再加 --include-go-build。')
            return 0
        before = shutil.disk_usage(home).free
        stopped = log_size = log_files = service_failures = 0
        if args.stop_idle_services:
            stopped, log_size, log_files, service_failures = execute_services(services, home)
        fresh_usage = capture_usage()  # 检查失败则文件删除阶段不开始。
        removed, count, failures = execute(items, fresh_usage, args.verbose)
        removed += log_size
        count += log_files
        failures += service_failures
        delta = shutil.disk_usage(home).free - before
        print(f'已停止闲置服务 {stopped} 个；服务停止开关: {"开启" if args.stop_idle_services else "未开启"}')
        print(f'已删除 {count} 个文件，文件占用估算 {human_size(removed)}；失败目标 {failures}')
        print(f'磁盘可用空间实际变化 {human_size(delta)}（会受其他程序及文件系统共享块影响）')
        return 1 if failures else 0
    except (OSError, UnsafePath, subprocess.SubprocessError) as exc:
        print(f'检查未完成: {exc}', file=sys.stderr)
        return 2


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        print('已中断；如使用了 --apply，部分删除可能已完成。', file=sys.stderr)
        raise SystemExit(130)

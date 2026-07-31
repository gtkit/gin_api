package projectinit

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- ⓪ 输入校验 ---

func TestValidateProjectName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "typical", input: "my_service", wantErr: false},
		// {0,62} 而非 {1,63}：单字符名合法，这条守住那个边界。
		{name: "single char", input: "x", wantErr: false},
		{name: "digits and underscores", input: "svc_2_v3", wantErr: false},
		{name: "63 chars boundary", input: "a" + strings.Repeat("b", 62), wantErr: false},

		{name: "empty", input: "", wantErr: true},
		{name: "64 chars over limit", input: "a" + strings.Repeat("b", 63), wantErr: true},
		{name: "uppercase", input: "Foo", wantErr: true},
		{name: "leading digit", input: "1abc", wantErr: true},
		{name: "leading underscore", input: "_abc", wantErr: true},
		{name: "hyphen", input: "my-service", wantErr: true},
		// 以下六条是注入面：会破坏 Go 字面量或成为 Make 表达式。
		{name: "double quote", input: `a"b`, wantErr: true},
		{name: "backslash", input: `a\b`, wantErr: true},
		{name: "make expression", input: "a$(shell echo x)", wantErr: true},
		{name: "backtick", input: "a`b`", wantErr: true},
		{name: "semicolon", input: "a;b", wantErr: true},
		{name: "newline", input: "a\nb", wantErr: true},
		{name: "path traversal", input: "../x", wantErr: true},
		{name: "path separator", input: "a/b", wantErr: true},
		{name: "leading space", input: " ab", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateProjectName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateProjectName(%q) err = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestRunRejectsInvalidNameWithoutTouchingFiles 证明非法项目名在**任何文件被修改之前**
// 就被拒绝——这是 ⓪ 阶段存在的全部意义。
func TestRunRejectsInvalidNameWithoutTouchingFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "double quote", input: `a"b`},
		{name: "make expression", input: "a$(shell echo x)"},
		{name: "newline", input: "a\nb"},
		{name: "path traversal", input: "../x"},
		{name: "uppercase", input: "Foo"},
		{name: "over length", input: "a" + strings.Repeat("b", 63)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newFixture(t, "example.com/newproj")
			before := snapshotTree(t, root)

			init := newStubbedInitializer(root)
			if _, err := init.Run(Options{Name: tt.input}); err == nil {
				t.Fatalf("expected refusal for name %q", tt.input)
			}
			assertTreeUnchanged(t, root, before)
		})
	}
}

func TestValidateBranchNameUsesGit(t *testing.T) {
	t.Parallel()
	requireGit(t)

	// 期望值取自 git check-ref-format 的真实行为，而不是自写正则的推测。
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{name: "simple", branch: "main", wantErr: false},
		{name: "slashed", branch: "feature/x", wantErr: false},
		{name: "leading dot", branch: ".foo", wantErr: true},
		{name: "lock suffix", branch: "foo.lock", wantErr: true},
		{name: "empty", branch: "", wantErr: true},
		{name: "space", branch: "a b", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateBranchName(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateBranchName(%q) err = %v, wantErr = %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

// --- Git 边界 ---

// TestRunRefusesExistingGitWithoutForce 覆盖最危险的那条路径：
// 旧实现在**成功路径**上也会 RemoveAll(".git")，用户在已有历史的仓库里手滑跑一次就没了。
func TestRunRefusesExistingGitWithoutForce(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	mustMkdir(t, filepath.Join(root, ".git"))
	before := snapshotTree(t, root)

	init := newStubbedInitializer(root)
	_, err := init.Run(Options{Name: "my_service"})
	if err == nil || !strings.Contains(err.Error(), "existing git repository") {
		t.Fatalf("expected refusal for existing git repository, got %v", err)
	}
	assertTreeUnchanged(t, root, before)
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf(".git must remain untouched: %v", err)
	}
}

// TestRunRefusesSpecialGitEntry 覆盖 linked worktree 与 symlink：即使带 --force 也必须拒绝。
func TestRunRefusesSpecialGitEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		force bool
	}{
		{
			name: "linked worktree file without force",
			setup: func(t *testing.T, root string) {
				mustWrite(t, filepath.Join(root, ".git"), "gitdir: /elsewhere/.git/worktrees/x\n")
			},
			force: false,
		},
		{
			name: "linked worktree file with force",
			setup: func(t *testing.T, root string) {
				mustWrite(t, filepath.Join(root, ".git"), "gitdir: /elsewhere/.git/worktrees/x\n")
			},
			force: true,
		},
		{
			name: "symlink with force",
			setup: func(t *testing.T, root string) {
				target := t.TempDir()
				if err := os.Symlink(target, filepath.Join(root, ".git")); err != nil {
					t.Fatalf("symlink: %v", err)
				}
			},
			force: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := newFixture(t, "example.com/newproj")
			tt.setup(t, root)
			before := snapshotTree(t, root)

			init := newStubbedInitializer(root)
			_, err := init.Run(Options{Name: "my_service", ForceReinitGit: tt.force})
			if err == nil {
				t.Fatal("expected refusal for special .git entry")
			}
			assertTreeUnchanged(t, root, before)
		})
	}
}

// --- 分阶段失败契约 ---

// TestSelfCheckFailureRestoresFilesByteForByte 覆盖「无 .git + ③ 自检失败」：
// 受影响文件必须逐字（含权限位）恢复。
func TestSelfCheckFailureRestoresFilesByteForByte(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	before := snapshotTree(t, root)

	init := newStubbedInitializer(root)
	init.selfCheck = func(string) error { return errors.New("boom") }
	init.gitInit = func(string, string) error {
		t.Fatal("git must not be initialized after self-check failure")
		return nil
	}

	_, err := init.Run(Options{Name: "my_service"})
	if err == nil || !strings.Contains(err.Error(), "self-check") {
		t.Fatalf("expected self-check failure, got %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestTransformFailureRestoresFiles 覆盖 ② 文件变换失败。
func TestTransformFailureRestoresFiles(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	before := snapshotTree(t, root)

	init := newStubbedInitializer(root)
	init.genAppmeta = func(string) error { return errors.New("generator exploded") }

	_, err := init.Run(Options{Name: "my_service"})
	if err == nil || !strings.Contains(err.Error(), "file transformation") {
		t.Fatalf("expected transform failure, got %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestGitInitFailureRestoresFiles 覆盖 ④ git init 失败。
func TestGitInitFailureRestoresFiles(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	before := snapshotTree(t, root)

	init := newStubbedInitializer(root)
	init.gitInit = func(string, string) error { return errors.New("init failed") }

	_, err := init.Run(Options{Name: "my_service"})
	if err == nil || !strings.Contains(err.Error(), "git init") {
		t.Fatalf("expected git init failure, got %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestCommitFailureWithExistingGitKeepsOldHistory 是本包最关键的一条契约：
// 输入原本有普通 .git 时，提交失败**绝不能**丢弃旧历史。
// 旧方案的「commit 失败一律保留新 Git」在这条路径上会永久销毁用户的历史。
func TestCommitFailureWithExistingGitKeepsOldHistory(t *testing.T) {
	t.Parallel()
	requireGit(t)

	root := newFixture(t, "example.com/newproj")
	sentinelBlob := makeRealRepoWithHistory(t, root)
	before := snapshotTree(t, root)

	init := newStubbedInitializer(root)
	init.gitInit = func(r, b string) error { return gitInitBranch(r, b) }
	init.gitAdd = func(r string) error { return gitAddAll(r) }
	init.gitCommit = func(string, string) error { return errors.New("commit refused") }

	_, err := init.Run(Options{Name: "my_service", ForceReinitGit: true})
	if err == nil || !strings.Contains(err.Error(), "git commit") {
		t.Fatalf("expected commit failure, got %v", err)
	}

	// 旧历史必须完好：能读回那条 sentinel commit。
	out, gerr := gitOutput(t, root, "log", "--format=%s", "-1")
	if gerr != nil {
		t.Fatalf("old git history is unusable after rollback: %v", gerr)
	}
	if strings.TrimSpace(out) != sentinelBlob {
		t.Fatalf("old history lost: HEAD subject = %q, want %q", strings.TrimSpace(out), sentinelBlob)
	}
	assertTreeUnchanged(t, root, before)
}

// TestCommitFailureWithoutExistingGitKeepsStagedFiles 覆盖另一档：
// 输入原本无 .git（gonew 产物）时没有旧历史可丢，保留新 Git 与已暂存文件更有用。
func TestCommitFailureWithoutExistingGitKeepsStagedFiles(t *testing.T) {
	t.Parallel()
	requireGit(t)

	root := newFixture(t, "example.com/newproj")
	init := newStubbedInitializer(root)
	init.gitInit = func(r, b string) error { return gitInitBranch(r, b) }
	init.gitAdd = func(r string) error { return gitAddAll(r) }
	init.gitCommit = func(string, string) error { return errors.New("commit refused") }

	_, err := init.Run(Options{Name: "my_service"})
	if err == nil {
		t.Fatal("expected commit failure")
	}
	if _, serr := os.Stat(filepath.Join(root, ".git")); serr != nil {
		t.Fatalf("fresh repository must be kept when there is no old history to protect: %v", serr)
	}
	out, _ := gitOutput(t, root, "diff", "--cached", "--name-only")
	if strings.TrimSpace(out) == "" {
		t.Fatal("files should remain staged so the user can finish with a single git commit")
	}
}

// --- 成功路径 ---

func TestRunStandardInstantiation(t *testing.T) {
	t.Parallel()
	requireGit(t)

	root := newFixture(t, "example.com/newproj")
	init := newRealGitInitializer(t, root)

	result, err := init.Run(Options{Name: "my_service"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// 身份写进了唯一来源。
	mk := readFile(t, filepath.Join(root, projectMkPath))
	if !strings.Contains(mk, "APP_NAME = my_service") {
		t.Fatalf("project.mk identity not written:\n%s", mk)
	}
	if !strings.Contains(mk, "APP_DISPLAY_NAME = My Service") {
		t.Fatalf("project.mk display name not derived:\n%s", mk)
	}
	// project.mk 的注释解释了取值约束与「什么不属于身份」，必须保留而非整文件重写。
	if !strings.Contains(mk, "唯一来源") {
		t.Fatalf("project.mk comments must be preserved:\n%s", mk)
	}

	// 母版专属部署入口已删除，且给出了自备部署入口的提示。
	for _, rel := range motherDeploymentFiles {
		if _, err := os.Lstat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed from derived projects, err=%v", rel, err)
		}
	}
	if !hasNoteContaining(result.Notes, "自备部署入口") {
		t.Fatalf("expected a note about providing your own deployment entrypoint, got %v", result.Notes)
	}

	// 覆盖率门禁不得被改动。
	if got := readFile(t, filepath.Join(root, "Makefile")); got != fixtureMakefile {
		t.Fatalf("Makefile must not be modified (coverage floor must not be reset):\n%s", got)
	}

	// 未被逐一列举为删除目标的文件必须原样保留。
	assertSentinelsIntact(t, root)

	// 分支名省略时精确为 main。
	out, _ := gitOutput(t, root, "rev-parse", "--abbrev-ref", "HEAD")
	if strings.TrimSpace(out) != "main" {
		t.Fatalf("branch = %q, want main", strings.TrimSpace(out))
	}

	if !hasNoteContaining(result.Notes, "Laravel") {
		t.Fatal("expected a legacy-removal note")
	}
}

// TestRunPreservesLocalExcludeAndItsEffect 是中性哨兵用例：
// 旧仓库的本地忽略条目必须在**首个提交**中依然生效——恢复晚于 git add 就等于没恢复。
func TestRunPreservesLocalExcludeAndItsEffect(t *testing.T) {
	t.Parallel()
	requireGit(t)

	root := newFixture(t, "example.com/newproj")
	makeRealRepoWithHistory(t, root)

	// 中性哨兵：名称不含任何开发工具标识。
	const sentinelDir = "local-only-sentinel/"
	excludePath := filepath.Join(root, ".git", "info", "exclude")
	mustMkdir(t, filepath.Dir(excludePath))
	excludeBody := "# local-only rules\n" + sentinelDir + "\n"
	mustWrite(t, excludePath, excludeBody)
	if err := os.Chmod(excludePath, 0o640); err != nil {
		t.Fatalf("chmod exclude: %v", err)
	}
	mustWrite(t, filepath.Join(root, "local-only-sentinel", "private.txt"), "must not be committed\n")

	init := newRealGitInitializer(t, root)
	if _, err := init.Run(Options{Name: "my_service", ForceReinitGit: true}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// 字节与 mode 都要逐字恢复。
	got := readFile(t, excludePath)
	if got != excludeBody {
		t.Fatalf("local exclude content not restored:\ngot  %q\nwant %q", got, excludeBody)
	}
	info, err := os.Stat(excludePath)
	if err != nil {
		t.Fatalf("stat restored exclude: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("local exclude mode = %v, want 0640", info.Mode().Perm())
	}

	// 真正的判据：哨兵目录没有进首个提交。
	out, _ := gitOutput(t, root, "ls-files")
	if strings.Contains(out, "local-only-sentinel") {
		t.Fatalf("sentinel path was committed despite the local exclude rule:\n%s", out)
	}
}

func TestRunNoCommitStagesWithoutCommitting(t *testing.T) {
	t.Parallel()
	requireGit(t)

	root := newFixture(t, "example.com/newproj")
	init := newRealGitInitializer(t, root)
	// --no-commit 不需要提交身份；若被调用即视为契约破坏。
	init.commitIdentity = func(string) error { t.Fatal("commit identity must not be required with --no-commit"); return nil }
	init.gitCommit = func(string, string) error { t.Fatal("commit must not happen with --no-commit"); return nil }

	result, err := init.Run(Options{Name: "my_service", NoCommit: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	staged, _ := gitOutput(t, root, "diff", "--cached", "--name-only")
	if strings.TrimSpace(staged) == "" {
		t.Fatal("files should be staged with --no-commit")
	}
	// 分支存在但无提交（unborn branch）是预期状态，不是错误。
	if _, err := gitOutput(t, root, "rev-parse", "HEAD"); err == nil {
		t.Fatal("no commit should exist with --no-commit")
	}
	branch, _ := gitOutput(t, root, "symbolic-ref", "--short", "HEAD")
	if strings.TrimSpace(branch) != "main" {
		t.Fatalf("unborn branch = %q, want main", strings.TrimSpace(branch))
	}
	if !hasNoteContaining(result.Notes, "未提交") {
		t.Fatalf("expected a not-committed note, got %v", result.Notes)
	}
}

func TestRunCustomBranch(t *testing.T) {
	t.Parallel()
	requireGit(t)

	root := newFixture(t, "example.com/newproj")
	init := newRealGitInitializer(t, root)

	if _, err := init.Run(Options{Name: "my_service", Branch: "feature/x"}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	out, _ := gitOutput(t, root, "rev-parse", "--abbrev-ref", "HEAD")
	if strings.TrimSpace(out) != "feature/x" {
		t.Fatalf("branch = %q, want feature/x", strings.TrimSpace(out))
	}
}

func TestRunDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	before := snapshotTree(t, root)

	init := newStubbedInitializer(root)
	init.gitInit = func(string, string) error { t.Fatal("gitInit called in dry-run"); return nil }
	init.selfCheck = func(string) error { t.Fatal("selfCheck called in dry-run"); return nil }
	init.genAppmeta = func(string) error { t.Fatal("genAppmeta called in dry-run"); return nil }

	result, err := init.Run(Options{Name: "my_service", DryRun: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.DryRun {
		t.Fatal("result.DryRun = false, want true")
	}
	if len(result.Rewritten) == 0 || len(result.Deleted) == 0 {
		t.Fatalf("dry-run should report planned changes, got rewritten=%v deleted=%v", result.Rewritten, result.Deleted)
	}
	assertTreeUnchanged(t, root, before)
}

func TestRunRefusesDirtyTree(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	mustMkdir(t, filepath.Join(root, ".git"))
	before := snapshotTree(t, root)

	init := newStubbedInitializer(root)
	init.workspaceClean = func(string) (bool, error) { return false, nil }

	_, err := init.Run(Options{Name: "my_service", ForceReinitGit: true})
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("expected dirty-tree refusal, got %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

func TestRunRefusesScaffoldMother(t *testing.T) {
	t.Parallel()

	root := newFixture(t, motherModulePath)
	before := snapshotTree(t, root)

	init := newStubbedInitializer(root)
	_, err := init.Run(Options{Name: "my_service"})
	if err == nil || !strings.Contains(err.Error(), "scaffold mother") {
		t.Fatalf("expected scaffold-mother refusal, got %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestSnapshotLivesOutsideProjectRoot 守住一条容易被忽略的约束：
// 快照放根内会被 ④ 阶段的 `git add -A` 纳入首个提交。
func TestSnapshotLivesOutsideProjectRoot(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	snap, err := newSnapshot(root)
	if err != nil {
		t.Fatalf("newSnapshot: %v", err)
	}
	defer func() { _ = snap.cleanup() }()

	rel, err := filepath.Rel(root, snap.dir)
	if err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("snapshot dir %s must not live inside the project root %s", snap.dir, root)
	}
	info, err := os.Stat(snap.dir)
	if err != nil {
		t.Fatalf("stat snapshot dir: %v", err)
	}
	// 旧 .git 可能含凭据缓存与 hook 脚本，不能对同机其他用户可读。
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot dir mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestSnapshotCleanupRemovesDir(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	snap, err := newSnapshot(root)
	if err != nil {
		t.Fatalf("newSnapshot: %v", err)
	}
	dir := snap.dir
	if err := snap.cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("snapshot dir must be removed, err=%v", err)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	t.Parallel()
	requireGit(t)

	root := newFixture(t, "example.com/newproj")
	init := newRealGitInitializer(t, root)

	if _, err := init.Run(Options{Name: "my_service"}); err != nil {
		t.Fatalf("first Run returned error: %v", err)
	}
	// 第二次运行时项目里已有 .git（第一次建的），因此需要 --force-reinit-git。
	second, err := init.Run(Options{Name: "my_service", ForceReinitGit: true})
	if err != nil {
		t.Fatalf("second Run returned error: %v", err)
	}
	// 部署入口第一次就删掉了，第二次不应再报删除。
	if len(second.Deleted) != 0 {
		t.Fatalf("second run should not delete anything again: %v", second.Deleted)
	}
}

// --- identity helpers ---

func TestSetMakeVar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		key     string
		value   string
		want    string
	}{
		{
			name:    "replaces existing value keeping comments",
			content: "# note\nAPP_NAME = old\nOTHER = keep\n",
			key:     "APP_NAME", value: "new",
			want: "# note\nAPP_NAME = new\nOTHER = keep\n",
		},
		{
			name:    "appends when missing",
			content: "OTHER = keep\n",
			key:     "APP_NAME", value: "new",
			want: "OTHER = keep\nAPP_NAME = new\n",
		},
		{
			name:    "appends newline when file lacks trailing newline",
			content: "OTHER = keep",
			key:     "APP_NAME", value: "new",
			want: "OTHER = keep\nAPP_NAME = new\n",
		},
		{
			name:    "does not touch a similarly named variable",
			content: "APP_NAME_SUFFIX = keep\nAPP_NAME = old\n",
			key:     "APP_NAME", value: "new",
			want: "APP_NAME_SUFFIX = keep\nAPP_NAME = new\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := setMakeVar(tt.content, tt.key, tt.value); got != tt.want {
				t.Fatalf("setMakeVar() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestDisplayNameFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "single word", input: "svc", want: "Svc"},
		{name: "two words", input: "my_service", want: "My Service"},
		{name: "digits kept", input: "svc_2", want: "Svc 2"},
		{name: "consecutive underscores collapse", input: "a__b", want: "A B"},
		{name: "trailing underscore", input: "a_", want: "A"},
		{name: "single char", input: "x", want: "X"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := displayNameFrom(tt.input); got != tt.want {
				t.Fatalf("displayNameFrom(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- helpers ---

// sentinelFiles 是中性哨兵：它们代表「派生项目作者自己拥有、initializer 绝不该碰」的文件。
//
// 刻意覆盖三种形态：根级文档、嵌套目录中的文档、以及非 Markdown 文件——因为最容易出现的
// 误删规则正是「删掉所有非白名单的根级 .md」，那会连派生项目作者自己的补充文档一起删掉。
//
// 名称必须保持中性：判据里一旦出现具体开发工具的目录名/文件名，检查规则本身就成了该工具链的标识。
var sentinelFiles = map[string]string{
	"NOTES.md":                  "# project notes\n\nlocal-only sentinel\n",
	"docs/design/overview.md":   "# design overview\n\nnested sentinel\n",
	"docs/design/data.json":     "{\"sentinel\":true}\n",
	"internal/keep/keep_me.txt": "sentinel inside internal\n",
}

// assertSentinelsIntact 断言全部中性哨兵仍存在且内容逐字节未变。
func assertSentinelsIntact(t *testing.T, root string) {
	t.Helper()
	for name, want := range sentinelFiles {
		full := filepath.Join(root, filepath.FromSlash(name))
		got, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("sentinel %s missing after run: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("sentinel %s modified:\ngot  %q\nwant %q", name, got, want)
		}
	}
}

// newStubbedInitializer 把全部外部依赖打桩，用于不需要真实 git 的用例。
func newStubbedInitializer(root string) *Initializer {
	return &Initializer{
		root:           root,
		workspaceClean: func(string) (bool, error) { return true, nil },
		selfCheck:      func(string) error { return nil },
		genAppmeta:     func(string) error { return nil },
		checkAppmeta:   func(string) error { return nil },
		lookPath:       func(string) (string, error) { return "/usr/bin/stub", nil },
		commitIdentity: func(string) error { return nil },
		gitInit:        func(string, string) error { return nil },
		gitAdd:         func(string) error { return nil },
		gitCommit:      func(string, string) error { return nil },
		warnf:          func(string, ...any) {},
	}
}

// newRealGitInitializer 用真实 git 命令，但仍打桩生成器与自检（它们需要完整仓库与工具链）。
func newRealGitInitializer(t *testing.T, root string) *Initializer {
	t.Helper()
	init := newStubbedInitializer(root)
	init.gitInit = func(r, b string) error { return gitInitBranch(r, b) }
	init.gitAdd = func(r string) error { return gitAddAll(r) }
	init.gitCommit = func(r, m string) error {
		// 测试环境未必配了全局身份，显式带上以免用例受本机配置影响。
		return runGit(r, "-c", "user.name=test", "-c", "user.email=test@example.com",
			"-c", "commit.gpgsign=false", "commit", "-m", m)
	}
	return init
}

const fixtureMakefile = "COVERAGE_MIN ?= 62.0\n\ncheck:\n\t@echo check\n"

const fixtureProjectMk = `# project.mk —— 应用身份的唯一来源
APP_NAME = gin_api
APP_DISPLAY_NAME = Gin API
`

func newFixture(t *testing.T, modulePath string) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"go.mod":      "module " + modulePath + "\n\ngo 1.26\n",
		projectMkPath: fixtureProjectMk,
		appmetaPath: `package appmeta

const (
	CommandName = "gin_api"
	RuntimeName = "Gin API"
)
`,
		"Makefile":       fixtureMakefile,
		".gitlab-ci.yml": "stages:\n  - deploy\n",
		"deployment.sh":  "#!/bin/bash\necho deploy\n",
	}
	maps.Copy(files, sentinelFiles)

	for name, content := range files {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	// deployment.sh 是可执行文件；快照恢复必须连 +x 一起还原。
	if err := os.Chmod(filepath.Join(root, "deployment.sh"), 0o755); err != nil {
		t.Fatalf("chmod deployment.sh: %v", err)
	}
	return root
}

// makeRealRepoWithHistory 在 root 建一个带一条真实提交的仓库，返回该提交的 subject。
func makeRealRepoWithHistory(t *testing.T, root string) string {
	t.Helper()
	const subject = "sentinel history commit"
	mustRun(t, root, "git", "init", "-b", "main")
	mustRun(t, root, "git", "add", "-A")
	mustRun(t, root, "git", "-c", "user.name=test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "commit", "-m", subject)
	return subject
}

// treeState 是项目树的一份内容+权限快照，用于断言「零文件改动」。
type treeState map[string]string

// snapshotTree 记录项目根下每个文件的相对路径 → "mode:content"，跳过 .git。
func snapshotTree(t *testing.T, root string) treeState {
	t.Helper()
	state := treeState{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		// .git 的内部状态由各用例单独断言，逐文件比对会因 git 自身的元数据变动而误报。
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			dest, lerr := os.Readlink(path)
			if lerr != nil {
				return lerr
			}
			state[rel] = "symlink:" + dest
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		state[rel] = fmt.Sprintf("%04o:%s", info.Mode().Perm(), content)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotTree: %v", err)
	}
	return state
}

// assertTreeUnchanged 断言项目树与 before 逐字节、逐权限位一致。
func assertTreeUnchanged(t *testing.T, root string, before treeState) {
	t.Helper()
	after := snapshotTree(t, root)
	for rel, want := range before {
		got, ok := after[rel]
		if !ok {
			t.Fatalf("file %s was deleted but should have been left untouched", rel)
		}
		if got != want {
			t.Fatalf("file %s was modified:\ngot  %q\nwant %q", rel, got, want)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Fatalf("file %s was created but no file should have been touched", rel)
		}
	}
}

func hasNoteContaining(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

func gitOutput(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(b)
}

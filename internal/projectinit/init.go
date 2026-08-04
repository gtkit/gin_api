// Package projectinit 实现脚手架母版的实例化收尾（gonew 改名后运行）：
// 把项目名写入应用身份的唯一来源、重新生成身份常量、删除母版专属部署入口、
// 按固定顺序重建 git，并做收尾自检。
//
// 固定执行顺序（不可调整）：
//
//	⓪ 输入校验（项目名 / 分支名 / 母版自身检测 / .git 形态 / 脏树 / 本地工具）
//	① 在项目根之外建立受影响文件与旧 Git 的快照
//	② 文件变换（写 project.mk → make gen-appmeta → 校验零差异 → 删除母版部署入口）
//	③ 收尾自检（go build + make check）
//	④ git init -b → 恢复本地 exclude → git add → git commit
//
// ⓪ 全部通过前不触碰任何文件；②③④ 各阶段失败均按 stageFailure 契约回滚。
//
// 删除职责的边界：本包只删除「被版本控制跟踪、且明确不适用于派生项目」的文件，
// 且每个删除目标都在需求中逐一列举。未被跟踪的文件不进 module zip，因而在派生项目里
// 根本不存在，针对它们的删除逻辑不可能生效；母版本地环境的清理由本地未跟踪工具负责。
// 同样禁止「非白名单根级文档一律删除」之类的启发式规则——那会误删派生项目作者自己的文档。
//
// 覆盖率门禁不在本包职责内：派生代码主体与母版相同，没有覆盖率下降的事实依据，
// 把门禁重置到更低的「起步地板」既无依据又违反棘轮方向。
package projectinit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// motherModulePath 是母版自身的模块路径；在其上直接运行 init 会误伤母版，故拒绝。
const motherModulePath = "github.com/gtkit/gin-api"

// projectMkPath 是应用身份的唯一来源。
const projectMkPath = "project.mk"

// appmetaPath 是由 project.mk 生成的身份常量文件。
const appmetaPath = "internal/appmeta/meta.go"

// motherDeploymentFiles 是母版所属环境的专属部署入口。
//
// 它们被 git 跟踪 → 进 module zip → 被 gonew 复制进派生项目，而其中绑定了母版环境的
// 进程管理服务名、部署根路径与分支等具体事实。派生项目二进制名不同，复制来的脚本必然
// 找不到目标 → 保留它们等于分发一条必然断裂的部署链。
//
// 这些文件本身是外部冻结契约：母版内不修改、不参数化，也不为派生项目内置替代 CD
// （没有目标环境事实的默认 CD 同样必然失败）。
var motherDeploymentFiles = []string{".gitlab-ci.yml", "deployment.sh"}

// laravelRemovalNote 收尾提示：Laravel 兼容层需按文档手动移除（不自动源码手术）。
const laravelRemovalNote = "如无需 Laravel 存量 token 兼容，请按 SCAFFOLD.md 的「移除 Laravel 兼容层」清单手动移除 Laravel 兼容层。"

// deploymentNote 说明派生项目为何不再包含母版的部署入口。
const deploymentNote = "已删除母版所属环境的专属部署入口（CI 配置与部署脚本）：它们绑定母版环境的进程管理服务名、部署根路径与分支，不存在参数化后通用的形态。派生项目需自备部署入口。"

// noCommitNote 提示 --no-commit 下尚未提交。
const noCommitNote = "已初始化仓库并暂存全部文件，但未提交：请自行 review 后执行 git commit。"

// Options 控制一次实例化。
type Options struct {
	// Name 是新项目身份名，写入 project.mk 并据以生成身份常量。
	Name string
	// Branch 是新仓库的初始分支名；为空时固定为 main（不读 init.defaultBranch）。
	Branch string
	// DryRun 为真时只预览将发生的改动，不落盘、不重置 git、不自检。
	DryRun bool
	// ForceReinitGit 允许在已有普通 .git 目录的项目上销毁并重建 Git 历史。
	// 不带该标志时，已有 .git 一律拒绝执行。
	ForceReinitGit bool
	// NoCommit 只初始化并暂存，不产生提交；该模式下不要求提交身份。
	NoCommit bool
}

// Result 汇报本次（或将要）发生的改动。
type Result struct {
	DryRun    bool
	Rewritten []string
	Deleted   []string
	Notes     []string
}

// Initializer 在某个项目根目录上执行实例化收尾。
type Initializer struct {
	root string
	// 下列注入点便于测试替换默认实现（git / 自检 / 生成器依赖外部环境）。
	workspaceClean func(root string) (bool, error)
	selfCheck      func(root string) error
	genAppmeta     func(root string) error
	checkAppmeta   func(root string) error
	lookPath       func(name string) (string, error)
	commitIdentity func(root string) error
	gitInit        func(root, branch string) error
	gitAdd         func(root string) error
	gitCommit      func(root, message string) error
	// warnf 输出非致命告警（如快照清理失败）。
	warnf func(format string, args ...any)
}

// New 从 startDir 向上查找包含 go.mod 的项目根，构造 Initializer。
// startDir 为空时使用当前工作目录。
func New(startDir string) (*Initializer, error) {
	if startDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
		startDir = wd
	}

	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	return &Initializer{
		root:           root,
		workspaceClean: gitWorkspaceClean,
		selfCheck:      goBuildAndCheck,
		genAppmeta:     runGenAppmeta,
		checkAppmeta:   runCheckAppmeta,
		lookPath:       exec.LookPath,
		commitIdentity: checkCommitIdentity,
		gitInit:        gitInitBranch,
		gitAdd:         gitAddAll,
		gitCommit:      gitCommit,
		warnf:          func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}, nil
}

// Run 执行实例化。opts.DryRun 为真时不做任何落盘/破坏性操作。
func (i *Initializer) Run(opts Options) (*Result, error) {
	branch, gitState, err := i.validateInputs(opts) // ⓪
	if err != nil {
		return nil, err
	}

	result := &Result{DryRun: opts.DryRun}
	if opts.DryRun {
		i.planDryRun(opts, result)
		return result, nil
	}

	// 旧 .git 的本地忽略文件必须在它被移走之前读出来。① 的快照会把旧 .git 整体 rename
	// 进快照目录，之后 `git rev-parse` 就找不到仓库了——顺序在这里是硬约束。
	var savedExclude localExclude
	if gitState == gitDirOK {
		if savedExclude, err = readLocalExclude(i.root); err != nil {
			return nil, err
		}
	}

	snap, err := i.takeSnapshot(gitState, opts) // ①
	if err != nil {
		return nil, err
	}
	defer i.cleanupSnapshot(snap)

	if err := i.transformFiles(opts, result); err != nil { // ②
		return nil, i.rollback(snap, err, "file transformation")
	}
	if err := i.selfCheck(i.root); err != nil { // ③
		return nil, i.rollback(snap, fmt.Errorf("post-init self-check failed: %w", err), "self-check")
	}
	if err := i.rebuildGit(snap, gitState, branch, savedExclude, opts, result); err != nil { // ④
		return nil, err
	}

	result.Notes = append(result.Notes, deploymentNote, laravelRemovalNote)
	if opts.NoCommit {
		result.Notes = append(result.Notes, noCommitNote)
	}
	return result, nil
}

// ⓪ validateInputs 在触碰任何文件之前完成全部输入校验。
// 任一项不通过即返回错误且零文件改动。
func (i *Initializer) validateInputs(opts Options) (branch string, gitState gitEntryState, err error) {
	if err = validateProjectName(strings.TrimSpace(opts.Name)); err != nil {
		return "", gitAbsent, err
	}

	branch = strings.TrimSpace(opts.Branch)
	if branch == "" {
		branch = defaultBranch
	}
	if err = validateBranchName(branch); err != nil {
		return "", gitAbsent, err
	}

	modulePath, err := readModulePath(i.root)
	if err != nil {
		return "", gitAbsent, err
	}
	if modulePath == motherModulePath {
		return "", gitAbsent, fmt.Errorf("refusing to run on the scaffold mother (module %q); run gonew to rename the module first", motherModulePath)
	}

	gitState, reason, err := inspectGitEntry(i.root)
	if err != nil {
		return "", gitAbsent, err
	}
	switch gitState {
	case gitRefused:
		// 即使带 --force-reinit-git 也拒绝：重建会遗弃 linked worktree 元数据，
		// 或经符号链接写到项目根之外。
		return "", gitState, fmt.Errorf("refusing to run: %s", reason)
	case gitDirOK:
		if !opts.ForceReinitGit {
			return "", gitState, errors.New(
				"refusing to run on an existing git repository: this command instantiates a scaffold and rebuilds git history from scratch; " +
					"pass --force-reinit-git to discard the existing history permanently")
		}
	case gitAbsent:
		// gonew 产物，默认支持的正常输入。
	}

	// dry-run 不落盘，故不要求纯净工作区。
	if !opts.DryRun && gitState == gitDirOK {
		clean, cerr := i.workspaceClean(i.root)
		if cerr != nil {
			return "", gitState, cerr
		}
		if !clean {
			return "", gitState, errors.New("refusing to run: working tree has uncommitted changes; commit or clean first")
		}
	}

	if opts.DryRun {
		return branch, gitState, nil
	}
	if err = checkRequiredTools(i.lookPath); err != nil {
		return "", gitState, err
	}
	if err = checkGitInitBranchSupport(); err != nil {
		return "", gitState, err
	}
	// --no-commit 不产生提交，因此不需要提交身份；强行要求会平白拦住合法用法。
	if !opts.NoCommit {
		if err = i.commitIdentity(i.root); err != nil {
			return "", gitState, err
		}
	}
	return branch, gitState, nil
}

// planDryRun 只列出将发生的改动，不触碰磁盘。
func (i *Initializer) planDryRun(opts Options, result *Result) {
	result.Rewritten = append(result.Rewritten, projectMkPath, appmetaPath)
	for _, rel := range motherDeploymentFiles {
		if _, err := os.Lstat(filepath.Join(i.root, rel)); err == nil {
			result.Deleted = append(result.Deleted, rel)
		}
	}
	result.Notes = append(result.Notes, deploymentNote, laravelRemovalNote)
	if opts.NoCommit {
		result.Notes = append(result.Notes, noCommitNote)
	}
}

// ① takeSnapshot 在项目根之外备份全部受影响路径与旧 Git。
func (i *Initializer) takeSnapshot(gitState gitEntryState, opts Options) (*snapshot, error) {
	snap, err := newSnapshot(i.root)
	if err != nil {
		return nil, err
	}

	affected := append([]string{projectMkPath, appmetaPath}, motherDeploymentFiles...)
	for _, rel := range affected {
		if err := snap.capture(rel); err != nil {
			_ = snap.cleanup()
			return nil, err
		}
	}

	if gitState == gitDirOK && opts.ForceReinitGit {
		if err := snap.saveGitDir(filepath.Join(i.root, ".git")); err != nil {
			_ = snap.cleanup()
			return nil, err
		}
	}
	return snap, nil
}

// ② transformFiles 写入身份来源、重新生成身份常量、删除母版部署入口。
//
// 身份改写走「写 project.mk → make gen-appmeta → 校验零差异」，不再正则重写 meta.go：
// 后者会形成第二个身份来源，且两者可以不一致。
func (i *Initializer) transformFiles(opts Options, result *Result) error {
	name := strings.TrimSpace(opts.Name)
	if err := i.writeProjectMk(name); err != nil {
		return err
	}
	result.Rewritten = append(result.Rewritten, projectMkPath)

	if err := i.genAppmeta(i.root); err != nil {
		return fmt.Errorf("generate appmeta from %s: %w", projectMkPath, err)
	}
	if err := i.checkAppmeta(i.root); err != nil {
		return fmt.Errorf("generated appmeta does not match %s: %w", projectMkPath, err)
	}
	result.Rewritten = append(result.Rewritten, appmetaPath)

	for _, rel := range motherDeploymentFiles {
		full := filepath.Join(i.root, rel)
		if _, err := os.Lstat(full); err != nil {
			continue // 已不存在（重复执行）——幂等。
		}
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("remove mother deployment entry %s: %w", rel, err)
		}
		result.Deleted = append(result.Deleted, rel)
	}
	return nil
}

// writeProjectMk 用新项目名重写身份来源文件的 APP_NAME 与 APP_DISPLAY_NAME。
//
// 值本身已在 ⓪ 阶段通过 projectNamePattern 校验，字符集内不含引号、反斜杠、换行
// 与 Make 元字符，因此写入是安全的；gen-appmeta 还会再校验一次。
func (i *Initializer) writeProjectMk(name string) error {
	path := filepath.Join(i.root, projectMkPath)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", projectMkPath, err)
	}
	updated := setMakeVar(string(content), "APP_NAME", name)
	updated = setMakeVar(updated, "APP_DISPLAY_NAME", displayNameFrom(name))
	return writeFileAtomic(path, []byte(updated), 0o644)
}

// ④ rebuildGit 初始化新仓库、恢复本地忽略文件、暂存并（可选）提交。
func (i *Initializer) rebuildGit(
	snap *snapshot, gitState gitEntryState, branch string, savedExclude localExclude, opts Options, result *Result,
) error {
	// 带 --force-reinit-git 时旧 .git 已在 ① 阶段被 rename 进快照，此处目录已不存在。
	if err := i.gitInit(i.root, branch); err != nil {
		return i.rollback(snap, err, "git init")
	}
	// 必须在 git add 之前写回：晚一步该文件就不再影响首个提交，等于没恢复。
	if err := writeLocalExclude(i.root, savedExclude); err != nil {
		return i.rollbackNewGit(snap, err, "restore local exclude")
	}
	if err := i.gitAdd(i.root); err != nil {
		return i.rollbackNewGit(snap, err, "git add")
	}

	if opts.NoCommit {
		// --no-commit 的成功语义到此为止：分支存在但无提交（unborn branch）是预期状态。
		// 因此该模式不存在「commit 失败」这一档。
		return nil
	}

	if err := i.gitCommit(i.root, "chore: initial commit"); err != nil {
		if gitState == gitDirOK {
			// 原本有普通 .git：绝不能丢弃旧历史。
			return i.rollbackNewGit(snap, err, "git commit")
		}
		// 原本无 .git（gonew 产物）：没有旧历史可丢，保留新 Git 与已暂存文件更有用。
		result.Notes = append(result.Notes,
			"仓库已初始化且文件已暂存，但提交失败；修复后可直接执行 git commit 完成收尾。")
		return fmt.Errorf("git commit failed after initializing a fresh repository (files remain staged): %w", err)
	}
	return nil
}

// rollback 恢复快照并把恢复错误与原始错误一起返回。
func (i *Initializer) rollback(snap *snapshot, cause error, stage string) error {
	if rerr := snap.restore(); rerr != nil {
		return fmt.Errorf("%s failed: %w; additionally the rollback did not fully succeed: %v", stage, cause, rerr)
	}
	return fmt.Errorf("%s failed (all changes rolled back): %w", stage, cause)
}

// rollbackNewGit 先删掉本次新建的 .git，再走普通回滚。
func (i *Initializer) rollbackNewGit(snap *snapshot, cause error, stage string) error {
	if err := os.RemoveAll(filepath.Join(i.root, ".git")); err != nil {
		return fmt.Errorf("%s failed: %w; additionally the new git dir could not be removed: %v", stage, cause, err)
	}
	return i.rollback(snap, cause, stage)
}

// cleanupSnapshot 在所有退出路径删除快照；失败只告警并打印绝对路径供人工处理，
// 不改变主流程结论。
func (i *Initializer) cleanupSnapshot(snap *snapshot) {
	dir := snap.dir
	if err := snap.cleanup(); err != nil {
		i.warnf("warning: could not remove snapshot directory %s: %v (remove it manually)", dir, err)
	}
}

func findProjectRoot(startDir string) (string, error) {
	dir := startDir
	for {
		if fileExists(filepath.Join(dir, "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %q upward", startDir)
		}
		dir = parent
	}
}

func readModulePath(root string) (string, error) {
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", errors.New("module path not found in go.mod")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// runGenAppmeta 调用 make 的身份生成入口。
//
// 走 make 而非直接调脚本：make 是身份生成的对外契约，派生项目改生成方式时
// 只需改 Makefile，实例化流程不必跟着改。
func runGenAppmeta(root string) error {
	return runMake(root, "gen-appmeta")
}

func runCheckAppmeta(root string) error {
	return runMake(root, "check-appmeta")
}

// isolatedEnv 是派生项目验证子命令的环境。
//
// **显式关闭 workspace**：父目录若存在 go.work（或其中的本地 replace），
// 派生项目的第一次验证跑的就不是它自己的依赖图——通过了也说明不了什么，
// 而失败时人会去查派生项目的代码，实际问题在上层目录。
func isolatedEnv() []string {
	return append(os.Environ(), "GOWORK=off")
}

func runMake(root, target string) error {
	cmd := exec.Command("make", target)
	cmd.Dir = root
	cmd.Env = isolatedEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("make %s: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// goBuildAndCheck 收尾自检：先 go build，再 make check。
func goBuildAndCheck(root string) error {
	build := exec.Command("go", "build", "./...")
	build.Env = isolatedEnv()
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w: %s", err, strings.TrimSpace(string(out)))
	}
	check := exec.Command("make", "check")
	check.Env = isolatedEnv()
	check.Dir = root
	if out, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("make check: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

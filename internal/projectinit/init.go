// Package projectinit 实现脚手架母版的实例化收尾（gonew 改名后运行）：
// 重写 appmeta 身份、重置覆盖率地板、重新初始化 git，并做收尾自检。
// 所有破坏性操作均带护栏（脏树拒绝、dry-run、幂等）。
//
// 删除职责的边界：本包只删除「被版本控制跟踪、且明确不适用于派生项目」的文件，
// 且每个删除目标都在需求中逐一列举。未被跟踪的文件不进 module zip，因而在派生项目里
// 根本不存在，针对它们的删除逻辑不可能生效；母版本地环境的清理由本地未跟踪工具负责。
// 同样禁止「非白名单根级文档一律删除」之类的启发式规则——那会误删派生项目作者自己的文档。
package projectinit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// motherModulePath 是母版自身的模块路径；在其上直接运行 init 会误伤母版，故拒绝。
const motherModulePath = "github.com/gtkit/gin-api"

// starterCoverageMin 是实例化后新项目的起步覆盖率地板（低于母版当前门禁，
// 给新项目早期添加功能留出余量；随项目成熟应逐步提高）。
const starterCoverageMin = "50.0"

// identityConsts 是 appmeta 中需按项目名重写的身份常量（按常量名匹配，与旧值无关）。
var identityConsts = []string{"CommandName", "RuntimeName", "ProjectName", "MigrateLockName"}

// legacyRemovalNote 收尾提示：Laravel 兼容层需按文档手动移除（不自动源码手术）。
const legacyRemovalNote = "如无需 Laravel 存量 token 兼容，请按 SCAFFOLD.md 的「移除 Laravel 兼容层」清单手动移除 Laravel-legacy JWT 层。"

// Options 控制一次实例化。
type Options struct {
	// Name 是新项目身份名，用于重写 appmeta 常量。
	Name string
	// DryRun 为真时只预览将发生的改动，不落盘、不重置 git、不自检。
	DryRun bool
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
	// 下列注入点便于测试替换默认实现（git / 自检依赖外部环境）。
	workspaceClean func(root string) (bool, error)
	gitReinit      func(root string) error
	selfCheck      func(root string) error
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
		gitReinit:      gitReinit,
		selfCheck:      goBuildAndCheck,
	}, nil
}

// Run 执行实例化。opts.DryRun 为真时不做任何落盘/破坏性操作。
func (i *Initializer) Run(opts Options) (*Result, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return nil, fmt.Errorf("project name is required")
	}

	modulePath, err := readModulePath(i.root)
	if err != nil {
		return nil, err
	}
	if modulePath == motherModulePath {
		return nil, fmt.Errorf("refusing to run on the scaffold mother (module %q); run gonew to rename the module first", motherModulePath)
	}

	// 护栏：工作区非纯净时拒绝（dry-run 除外，dry-run 不落盘故安全）。
	if !opts.DryRun {
		clean, err := i.workspaceClean(i.root)
		if err != nil {
			return nil, err
		}
		if !clean {
			return nil, fmt.Errorf("refusing to run: working tree has uncommitted changes; commit or clean first")
		}
	}

	result := &Result{DryRun: opts.DryRun}

	if err := i.rewriteIdentity(opts.Name, result); err != nil {
		return nil, err
	}
	if err := i.resetCoverageFloor(result); err != nil {
		return nil, err
	}

	result.Notes = append(result.Notes, legacyRemovalNote)

	if opts.DryRun {
		return result, nil
	}

	if err := i.gitReinit(i.root); err != nil {
		return nil, fmt.Errorf("reinitialize git: %w", err)
	}
	if err := i.selfCheck(i.root); err != nil {
		return nil, fmt.Errorf("post-init self-check failed: %w", err)
	}

	return result, nil
}

// rewriteIdentity 按项目名重写 internal/appmeta/meta.go 的身份常量。
func (i *Initializer) rewriteIdentity(name string, result *Result) error {
	metaPath := filepath.Join(i.root, "internal", "appmeta", "meta.go")
	content, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("read appmeta: %w", err)
	}

	updated := rewriteIdentityConsts(string(content), name)
	if updated == string(content) {
		return nil
	}

	rel := i.rel(metaPath)
	result.Rewritten = append(result.Rewritten, rel)
	if result.DryRun {
		return nil
	}
	if err := os.WriteFile(metaPath, []byte(updated), 0o644); err != nil { //nolint:gosec // project source files are expected to be user-readable.
		return fmt.Errorf("write appmeta: %w", err)
	}
	return nil
}

// resetCoverageFloor 把 Makefile 的 COVERAGE_MIN 重置到起步地板。
func (i *Initializer) resetCoverageFloor(result *Result) error {
	makefilePath := filepath.Join(i.root, "Makefile")
	content, err := os.ReadFile(makefilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read Makefile: %w", err)
	}

	re := regexp.MustCompile(`(?m)^(COVERAGE_MIN\s*\?=\s*).*$`)
	updated := re.ReplaceAllString(string(content), "${1}"+starterCoverageMin)
	if updated == string(content) {
		return nil
	}

	result.Rewritten = append(result.Rewritten, i.rel(makefilePath))
	if result.DryRun {
		return nil
	}
	if err := os.WriteFile(makefilePath, []byte(updated), 0o644); err != nil { //nolint:gosec // project files are expected to be user-readable.
		return fmt.Errorf("write Makefile: %w", err)
	}
	return nil
}

func (i *Initializer) rel(path string) string {
	if r, err := filepath.Rel(i.root, path); err == nil {
		return r
	}
	return path
}

// rewriteIdentityConsts 按常量名把 appmeta 身份常量的字符串字面量替换为项目名派生值。
func rewriteIdentityConsts(content, name string) string {
	lock := strings.ReplaceAll(name, "-", "_") + "_migrate"
	values := map[string]string{
		"CommandName":     name,
		"RuntimeName":     name,
		"ProjectName":     name,
		"MigrateLockName": lock,
	}
	for _, constName := range identityConsts {
		re := regexp.MustCompile(`(\b` + constName + `\s*=\s*)"[^"]*"`)
		content = re.ReplaceAllString(content, `${1}"`+values[constName]+`"`)
	}
	return content
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
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("module path not found in go.mod")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// gitWorkspaceClean 报告工作区是否干净；非 git 仓库（如刚 gonew 出来的目录）视为干净。
func gitWorkspaceClean(root string) (bool, error) {
	if !dirExists(filepath.Join(root, ".git")) {
		return true, nil
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(strings.TrimSpace(string(out))) == 0, nil
}

// gitReinit 丢弃母版 git 历史并为新项目建立首个提交。
func gitReinit(root string) error {
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		return fmt.Errorf("remove .git: %w", err)
	}
	steps := [][]string{
		{"init"},
		{"add", "-A"},
		{"commit", "-m", "chore: initial commit"},
	}
	for _, args := range steps {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// goBuildAndCheck 收尾自检：先 go build，再 make check。
func goBuildAndCheck(root string) error {
	build := exec.Command("go", "build", "./...")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w: %s", err, strings.TrimSpace(string(out)))
	}
	check := exec.Command("make", "check")
	check.Dir = root
	if out, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("make check: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

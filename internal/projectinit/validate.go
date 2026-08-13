//go:build scaffold

package projectinit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// projectNamePattern 是项目名的唯一判据。
//
// 与模块生成器的名字校验同源（`^[a-z][a-z0-9_]*$`），额外加 63 字符上界。
// 注意上界写作 {0,62} 而非 {1,63}：后者要求首字母之后至少还有一个字符，
// 会把合法的单字符名（如 x）也拒掉——那是无意的收紧。
//
// 为什么必须校验：该值会同时插进 Go 字符串字面量与 project.mk（Make KV）。
// 未经校验时，双引号/反斜杠/换行会破坏 meta.go，而 $(shell …)、反引号、`;`
// 会升级为 Make 表达式注入，在 `make gen-appmeta` 时执行。
var projectNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// defaultBranch 是省略 --branch 时的固定分支名。
//
// 刻意不读 init.defaultBranch：否则同一份母版在不同机器上产出不同分支名，
// 破坏"实例化结果可复现"。
const defaultBranch = "main"

// validateProjectName 校验项目名。
func validateProjectName(name string) error {
	if !projectNamePattern.MatchString(name) {
		return fmt.Errorf(
			"invalid project name %q: must match %s (lowercase letter first, then lowercase letters/digits/underscores, at most 63 chars)",
			name, projectNamePattern.String())
	}
	return nil
}

// validateBranchName 把分支名交给 Git 自己判定。
//
// 不自写正则：实测 `git check-ref-format --branch` 拒绝 `.foo`、`foo.lock` 而接受
// `feature/x`，用正则复刻这套规则既啰嗦又必然出错，而且 Git 的规则会演进。
func validateBranchName(branch string) error {
	if branch == "" {
		return errors.New("branch name must not be empty")
	}
	cmd := exec.Command("git", "check-ref-format", "--branch", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("invalid branch name %q (rejected by git check-ref-format): %s", branch, msg)
	}
	return nil
}

// gitEntryState 描述项目根下 .git 的形态。
type gitEntryState uint8

const (
	gitAbsent  gitEntryState = iota // 无 .git —— gonew 产物，默认支持的正常输入
	gitDirOK                        // 普通 .git 目录
	gitRefused                      // 普通文件（linked worktree）/符号链接/其他特殊类型
)

// inspectGitEntry 判定项目根下 .git 的形态。
//
// 一律 Lstat：Stat 会跟随符号链接，让 `.git -> /somewhere/else` 被误判成普通目录，
// 后续的移动与重建就会写到项目根之外。
func inspectGitEntry(root string) (gitEntryState, string, error) {
	full := filepath.Join(root, ".git")
	info, err := os.Lstat(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return gitAbsent, "", nil
		}
		return gitAbsent, "", fmt.Errorf("lstat .git: %w", err)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return gitRefused, ".git is a symlink; rebuilding it would write outside the project root", nil
	case info.IsDir():
		return gitDirOK, "", nil
	case info.Mode().IsRegular():
		// `.git` 为普通文件意味着 linked worktree（内容是 `gitdir: <path>`）。
		// 重建会遗弃 worktree 元数据，因此即使带强制标志也拒绝。
		return gitRefused, ".git is a regular file (linked worktree); rebuilding it would orphan the worktree metadata", nil
	default:
		return gitRefused, fmt.Sprintf(".git is neither a directory nor a regular file (%s)", info.Mode().Type()), nil
	}
}

// requiredTools 是实例化流程真正会调用的外部工具。
//
// 提前检查而不是等用到时才失败：文件变换已经发生一半再报"找不到 make"，
// 就要走一遍回滚；在 ⓪ 阶段查掉，代价是零。
var requiredTools = []string{"git", "make", "go"}

// checkRequiredTools 确认外部工具可用，且 Git 支持在初始化时指定分支。
//
// 明确不做：自动下载工具或依赖来"修复环境"。实例化必须能离线完成。
func checkRequiredTools(lookPath func(string) (string, error)) error {
	for _, tool := range requiredTools {
		if _, err := lookPath(tool); err != nil {
			return fmt.Errorf("required tool %q not found in PATH: %w", tool, err)
		}
	}
	return nil
}

// checkGitInitBranchSupport 确认本机 Git 支持 `git init -b <branch>`。
//
// 该能力是固定分支名的前提；旧版 Git 不支持时必须在改任何文件之前失败，
// 否则会在 ④ 阶段才发现，白白走一遍回滚。
func checkGitInitBranchSupport() error {
	cmd := exec.Command("git", "init", "-h")
	out, _ := cmd.CombinedOutput() // `git init -h` 以非零码退出属正常，只看输出内容。
	if !strings.Contains(string(out), "-b ") && !strings.Contains(string(out), "--initial-branch") {
		return errors.New("this git version does not support `git init -b <branch>`; upgrade git")
	}
	return nil
}

// checkCommitIdentity 以只读方式验证提交身份可用。
//
// 只在会产生提交时调用：--no-commit 不需要身份，强行要求会平白拦住合法用法。
// 用 `git var` 而非读 config：它按 Git 自己的优先级解析并在缺失时报错，与真实 commit 一致。
func checkCommitIdentity(root string) error {
	for _, which := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		cmd := exec.Command("git", "var", which)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git identity is not configured (%s): %s", which, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

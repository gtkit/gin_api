package projectinit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// localExclude 是旧仓库本地忽略文件的一份拷贝。
//
// 存在的理由：`git init` 重建后，旧 .git/info/exclude 随旧 .git 一起消失。
// 该文件位于 .git 内、不会进入提交，但它是唯一能阻止本地私有文件被**首个提交**
// 意外纳入的规则来源——丢了它，紧随其后的 `git add -A` 就会把本地私有文件一并提交。
type localExclude struct {
	present bool
	content []byte
	mode    fs.FileMode
}

// readLocalExclude 在旧 .git 被移走**之前**读取其本地忽略文件。
//
// 路径经 `git rev-parse --git-path` 获得而非拼接：worktree、GIT_DIR 覆盖等形态下
// 拼出来的路径是错的。
//
// 该文件本身是符号链接时拒绝：跟随它会读到项目根之外，恢复时又写回去。
func readLocalExclude(root string) (localExclude, error) {
	path, err := gitPath(root, "info/exclude")
	if err != nil {
		return localExclude{}, err
	}

	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return localExclude{present: false}, nil
		}
		return localExclude{}, fmt.Errorf("lstat local exclude: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return localExclude{}, errors.New("refusing to handle a symlinked .git/info/exclude: it may point outside the project root")
	}
	if !info.Mode().IsRegular() {
		return localExclude{}, fmt.Errorf("refusing to handle .git/info/exclude of type %s", info.Mode().Type())
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return localExclude{}, fmt.Errorf("read local exclude: %w", err)
	}
	return localExclude{present: true, content: content, mode: info.Mode().Perm()}, nil
}

// writeLocalExclude 把旧的本地忽略文件逐字写回新仓库。
//
// 调用时机是硬约束：必须在 `git init` 之后、`git add -A` **之前**。
// 晚一步该文件就不再影响首个提交，等于没恢复。
func writeLocalExclude(root string, ex localExclude) error {
	if !ex.present {
		return nil
	}
	path, err := gitPath(root, "info/exclude")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create git info dir: %w", err)
	}
	return writeFileAtomic(path, ex.content, ex.mode)
}

// gitPath 让 Git 自己解析 .git 内某个路径，不拼接。
func gitPath(root, rel string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-path", rel)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-path %s: %w", rel, err)
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("git rev-parse --git-path %s returned empty path", rel)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return p, nil
}

// runGit 在项目根执行一条 git 命令，失败时把 Git 自己的输出带进错误。
func runGit(root string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// gitInitBranch 以指定分支名初始化仓库。
//
// 分支在 init 时确定，因此 --no-commit 下该分支存在但无提交（unborn branch）——
// 这是预期状态，不是错误。
func gitInitBranch(root, branch string) error {
	return runGit(root, "init", "-b", branch)
}

func gitAddAll(root string) error { return runGit(root, "add", "-A") }

func gitCommit(root, message string) error { return runGit(root, "commit", "-m", message) }

// gitWorkspaceClean 报告工作区是否干净；非 git 仓库（如刚 gonew 出来的目录）视为干净。
func gitWorkspaceClean(root string) (bool, error) {
	state, _, err := inspectGitEntry(root)
	if err != nil {
		return false, err
	}
	if state == gitAbsent {
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

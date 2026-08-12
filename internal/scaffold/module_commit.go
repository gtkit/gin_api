// module_commit.go 承载生成结果的暂存与统一落盘：要么全部、要么零落盘，
// 单文件写入原子生效，失败回滚只清除本次产物。
package scaffold

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

func (g *ModuleGenerator) writeManagedFile(file generatedFile, content []byte, force, dryRun bool, result *GenerateResult, plan *[]pendingWrite) error {
	relPath := g.rel(file.path)
	existing, err := os.ReadFile(file.path)
	if err == nil {
		if bytes.Equal(existing, content) {
			result.Skipped = append(result.Skipped, relPath)
			return nil
		}
		if file.immutable {
			// 迁移不可变：跳过必须**可见**——静默跳过会让使用者以为
			// schema 变更已随 force 生效，实际迁移文件纹丝不动。
			result.Skipped = append(result.Skipped, relPath)
			result.Notes = append(result.Notes, fmt.Sprintf(
				"%s 已存在且与本次模板产出不一致，未覆盖（迁移不可变，--force 也不例外）；"+
					"schema 变更请另建 alter migration", relPath))
			return nil
		}
		if !force || !file.allowOverwrite {
			result.Skipped = append(result.Skipped, relPath)
			return nil
		}
		if dryRun {
			result.Updated = append(result.Updated, relPath)
			return nil
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", relPath, err)
	} else if dryRun {
		result.Created = append(result.Created, relPath)
		return nil
	}

	// 只暂存不落盘：全部预检通过后由 commitWrites 统一写入（要么全部、要么零落盘）。
	existed := err == nil
	*plan = append(*plan, pendingWrite{path: file.path, content: content, existed: existed, original: existing})

	if existed {
		result.Updated = append(result.Updated, relPath)
		return nil
	}

	result.Created = append(result.Created, relPath)
	return nil
}

// pendingWrite 是一次待提交的文件写入，携带恢复所需的原始状态。
type pendingWrite struct {
	path    string
	content []byte
	// existed 为真时 original 是提交前的文件内容，恢复时写回；
	// 为假时该文件是本次新建，恢复时删除。
	existed  bool
	original []byte
}

// writeGeneratedFile 是提交阶段的写入缝，测试用它注入写入失败。
var writeGeneratedFile = atomicWriteFile

// tempFileWriter 是原子写入内部的临时文件写入缝，测试用它模拟部分写失败。
var tempFileWriter = func(f *os.File, data []byte) (int, error) { return f.Write(data) }

// atomicWriteFile 经同目录临时文件写入后重命名覆盖目标。
//
// 直接截断写（os.WriteFile 的 O_TRUNC）在磁盘错误时会把使用者的既有文件
// 毁成半截，而该文件因"未写成功"不在已写清单里、不会被回滚恢复。
// 临时文件 + rename 让目标在任意失败点上要么是完整新内容、要么原样不动；
// 失败路径上清除本次的临时文件，不留 .tmp 残骸。
func atomicWriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err = tempFileWriter(tmp, data); err != nil {
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	// CreateTemp 固定 0600，目标权限单独授予。
	if err = os.Chmod(tmp.Name(), perm); err != nil {
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename temp file to %s: %w", path, err)
	}
	return nil
}

// acquireGenerationLock 获取项目级建议锁。
//
// 锁文件放系统临时目录（按项目根路径散列命名）：不污染工作区、不入库。
// 拿不到锁立即报错而非等待：并发生成没有合并语义，等待只是把覆盖推迟。
// flock 随进程退出自动释放，崩溃不留死锁；NFS 上语义不可靠——锁是防护带，
// 正确性兜底是 verifyPlanUnchanged 的乐观校验，两者叠加。
func acquireGenerationLock(root string) (release func(), err error) {
	sum := sha256.Sum256([]byte(root))
	path := filepath.Join(os.TempDir(), fmt.Sprintf("gin-api-scaffold-%x.lock", sum[:8]))

	lock := flock.New(path)
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquire generation lock %s: %w", path, err)
	}
	if !ok {
		return nil, fmt.Errorf("another generation is already running for this project "+
			"(lock %s is held); wait for it to finish and re-run", path)
	}
	return func() { _ = lock.Unlock() }, nil
}

// verifyPlanUnchanged 校验全部目标自规划读取以来未被并发改动
// （module-scaffold-wiring spec 的乐观并发契约）：更新目标的当前内容必须
// 与规划快照一致、新建目标必须仍不存在。任一漂移即中止（零写入）——
// 规划与提交之间的窗口里，另一个生成进程的写入会被本进程用陈旧内容覆盖，
// 回滚还会把对方的产物还原成本进程的"原始"快照。
func verifyPlanUnchanged(plan []pendingWrite) error {
	for _, w := range plan {
		current, err := os.ReadFile(w.path)
		switch {
		case w.existed:
			if err != nil {
				return fmt.Errorf("verify %s before commit: %w", w.path, err)
			}
			if !bytes.Equal(current, w.original) {
				return fmt.Errorf("refusing to commit: %s changed since it was read "+
					"(another generator run?); re-run the generation", w.path)
			}
		case err == nil:
			return fmt.Errorf("refusing to commit: %s was created since planning "+
				"(another generator run?); re-run the generation", w.path)
		case !os.IsNotExist(err):
			return fmt.Errorf("verify %s before commit: %w", w.path, err)
		}
	}
	return nil
}

// commitWrites 统一落盘全部暂存写入；任一失败时逆序恢复本次已写入的文件与
// 新建的目录，主错误在前、恢复错误聚合在后。
func commitWrites(plan []pendingWrite) error {
	if err := verifyPlanUnchanged(plan); err != nil {
		return err
	}

	written := make([]pendingWrite, 0, len(plan))
	var createdDirs []string
	for _, w := range plan {
		dir := filepath.Dir(w.path)
		// 在 MkdirAll 之前记录将要新建的最外层目录：恢复必须把工作区还原到
		// 生成之前的样子，残留的空目录树同样是"失败留下的痕迹"。
		if root := firstMissingAncestor(dir); root != "" {
			createdDirs = append(createdDirs, root)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return errors.Join(
				fmt.Errorf("mkdir %s: %w", dir, err), rollbackWrites(written, createdDirs))
		}
		if err := writeGeneratedFile(w.path, w.content, 0o644); err != nil {
			return errors.Join(
				fmt.Errorf("write %s: %w", w.path, err), rollbackWrites(written, createdDirs))
		}
		written = append(written, w)
	}
	return nil
}

// firstMissingAncestor 返回为创建 dir 而将要新建的最外层目录；dir 已存在时返回空串。
func firstMissingAncestor(dir string) string {
	if _, err := os.Stat(dir); err == nil {
		return ""
	}
	missing := dir
	for {
		parent := filepath.Dir(missing)
		if parent == missing {
			return missing
		}
		if _, err := os.Stat(parent); err == nil {
			return missing
		}
		missing = parent
	}
}

// rollbackWrites 逆序恢复已写文件（更新的写回原内容，新建的删除），
// 再移除本次新建目录树中已空的目录。
//
// 不用 RemoveAll：回滚窗口内并发出现在新建目录里的使用者文件不是本工具
// 可以删除的东西——恢复工作区的义务止于清除本次产物。本次的文件在上一步
// 已逐个移除，之后仍非空的目录只可能装着别人的东西，保留它不是回滚失败。
func rollbackWrites(written []pendingWrite, createdDirs []string) error {
	var errs []error
	for i := len(written) - 1; i >= 0; i-- {
		w := written[i]
		if w.existed {
			if err := writeGeneratedFile(w.path, w.original, 0o644); err != nil {
				errs = append(errs, fmt.Errorf("restore %s: %w", w.path, err))
			}
			continue
		}
		if err := os.Remove(w.path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", w.path, err))
		}
	}
	for i := len(createdDirs) - 1; i >= 0; i-- {
		if err := removeEmptyDirTree(createdDirs[i]); err != nil {
			errs = append(errs, fmt.Errorf("remove created dir %s: %w", createdDirs[i], err))
		}
	}
	return errors.Join(errs...)
}

// removeEmptyDirTree 后序移除 root 下已空的目录；含内容的目录静默保留。
func removeEmptyDirTree(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("walk %s: %w", root, err)
	}

	// WalkDir 前序产出，倒序即先子后父。
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Remove(dirs[i]); err == nil {
			continue
		} else if entries, readErr := os.ReadDir(dirs[i]); readErr == nil && len(entries) > 0 {
			continue // 装着非本次产物：保留是义务，不是失败。
		} else {
			return fmt.Errorf("remove %s: %w", dirs[i], err)
		}
	}
	return nil
}

//go:build scaffold

package projectinit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// entryKind 记录快照条目在拍快照那一刻的真实类型。
//
// 为什么必须记类型而不只记内容：恢复要还原的是"执行前的状态"，而"原本不存在"与
// "原本是空文件"是两种不同的状态——只记内容无法区分，恢复时会凭空造出一个空文件。
type entryKind uint8

const (
	entryAbsent entryKind = iota // 原本不存在
	entryRegular
	entryDir
	entrySymlink
)

// snapshotEntry 是单个受影响路径的快照。
type snapshotEntry struct {
	rel      string
	kind     entryKind
	mode     fs.FileMode
	content  []byte // 仅 entryRegular
	linkDest string // 仅 entrySymlink
}

// snapshot 持有一次实例化中全部受影响文件与旧 Git 的备份。
//
// 位置硬约束：必须在项目根**之外**。放根内会被 ④ 阶段的 `git add -A` 纳入首个提交。
// 权限硬约束：0700。旧 .git 可能含凭据缓存与 hook 脚本，不能对同机其他用户可读。
type snapshot struct {
	dir      string // 项目根之外的临时目录，0700
	root     string
	entries  []snapshotEntry
	gitSaved bool   // 旧 .git 是否已被 rename 进快照
	gitDir   string // 快照内旧 .git 的绝对路径
	preserve bool   // a failed rollback retains the on-disk recovery copy
}

// newSnapshot 在项目根的**同级目录**下创建快照目录。
//
// 选同级而非系统临时目录：rename 要求同一文件系统，而 /tmp 常常是另一个挂载点。
// 旧 .git 用 rename 移入快照（原子、不递归复制），跨设备会直接失败——这正是需要同级的原因。
func newSnapshot(root string) (*snapshot, error) {
	parent := filepath.Dir(root)
	dir, err := os.MkdirTemp(parent, ".projectinit-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("create snapshot dir next to project root: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, errors.Join(fmt.Errorf("chmod snapshot dir: %w", err), os.RemoveAll(dir))
	}
	return &snapshot{dir: dir, root: root}, nil
}

// persistRecovery writes original bytes and presence/type semantics before any
// project mutation. The private file stays in the already protected directory.
func (s *snapshot) persistRecovery() error {
	type recoveryEntry struct {
		Path       string      `json:"path"`
		Kind       entryKind   `json:"kind"`
		Mode       fs.FileMode `json:"mode"`
		Content    []byte      `json:"content"`
		LinkTarget string      `json:"link_target"`
	}
	entries := make([]recoveryEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, recoveryEntry{Path: entry.rel, Kind: entry.kind, Mode: entry.mode, Content: entry.content, LinkTarget: entry.linkDest})
	}
	data, err := json.Marshal(struct {
		Root    string          `json:"root"`
		Entries []recoveryEntry `json:"entries"`
	}{Root: s.root, Entries: entries})
	if err != nil {
		return fmt.Errorf("encode project recovery copy: %w", err)
	}
	return writeFileAtomic(filepath.Join(s.dir, "recovery.json"), data, 0o600)
}

// capture 记录一个受影响路径的当前状态。路径不存在也要记（entryAbsent），
// 否则回滚时无法把"执行中新建的文件"删回去。
//
// 一律使用 Lstat：跟随符号链接会让快照读到项目根之外的内容，恢复时又写回去，
// 等于把项目外的文件卷进来。
func (s *snapshot) capture(rel string) error {
	full := filepath.Join(s.root, rel)
	info, err := os.Lstat(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.entries = append(s.entries, snapshotEntry{rel: rel, kind: entryAbsent})
			return nil
		}
		return fmt.Errorf("lstat %s: %w", rel, err)
	}

	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		dest, err := os.Readlink(full)
		if err != nil {
			return fmt.Errorf("readlink %s: %w", rel, err)
		}
		s.entries = append(s.entries, snapshotEntry{rel: rel, kind: entrySymlink, mode: info.Mode(), linkDest: dest})
	case info.IsDir():
		s.entries = append(s.entries, snapshotEntry{rel: rel, kind: entryDir, mode: info.Mode().Perm()})
	case info.Mode().IsRegular():
		content, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		// 权限位必须一起记：deployment.sh 是可执行文件，恢复丢掉 +x 就等于损坏它。
		s.entries = append(s.entries, snapshotEntry{rel: rel, kind: entryRegular, mode: info.Mode().Perm(), content: content})
	default:
		return fmt.Errorf("refusing to snapshot %s: unsupported file type %s", rel, info.Mode().Type())
	}
	return nil
}

// saveGitDir 把旧 .git 用 rename 移入快照。
//
// 用 rename 而非递归复制：原子、快、且不会在中途留下半份 .git。跨设备错误在文件变换
// **之前**就暴露出来（此时尚未改任何文件），不退化成非原子复制。
func (s *snapshot) saveGitDir(gitPath string) error {
	dest := filepath.Join(s.dir, "git-dir")
	if err := os.Rename(gitPath, dest); err != nil {
		return fmt.Errorf("move existing git dir into snapshot (must be same filesystem): %w", err)
	}
	s.gitSaved = true
	s.gitDir = dest
	return nil
}

// restore 把全部受影响路径与旧 Git 还原到拍快照时的状态。
// 单个条目失败不中断其余恢复，最后聚合错误——回滚是尽力而为的收尾，
// 提前 return 会让剩下的文件停在半改状态。
func (s *snapshot) restore() error {
	var errs []error
	for _, e := range s.entries {
		if err := s.restoreEntry(e); err != nil {
			errs = append(errs, err)
		}
	}
	if s.gitSaved {
		target := filepath.Join(s.root, ".git")
		// 先移除本次可能新建的 .git，再把旧的原子换回去。
		if err := os.RemoveAll(target); err != nil {
			errs = append(errs, fmt.Errorf("remove new git dir before restore: %w", err))
		} else if err := os.Rename(s.gitDir, target); err != nil {
			errs = append(errs, fmt.Errorf("restore original git dir: %w", err))
		} else {
			s.gitSaved = false
		}
	}
	return errors.Join(errs...)
}

func (s *snapshot) restoreEntry(e snapshotEntry) error {
	full := filepath.Join(s.root, e.rel)
	switch e.kind {
	case entryAbsent:
		if err := os.RemoveAll(full); err != nil {
			return fmt.Errorf("restore(remove) %s: %w", e.rel, err)
		}
		return nil
	case entryRegular:
		if err := os.RemoveAll(full); err != nil {
			return fmt.Errorf("restore(clear) %s: %w", e.rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("restore(mkdir) %s: %w", e.rel, err)
		}
		return writeFileAtomic(full, e.content, e.mode)
	case entryDir:
		if err := os.MkdirAll(full, e.mode); err != nil {
			return fmt.Errorf("restore(mkdir) %s: %w", e.rel, err)
		}
		return os.Chmod(full, e.mode)
	case entrySymlink:
		if err := os.RemoveAll(full); err != nil {
			return fmt.Errorf("restore(clear) %s: %w", e.rel, err)
		}
		if err := os.Symlink(e.linkDest, full); err != nil {
			return fmt.Errorf("restore(symlink) %s: %w", e.rel, err)
		}
		return nil
	default:
		return fmt.Errorf("restore %s: unknown snapshot kind %d", e.rel, e.kind)
	}
}

// cleanup 删除快照目录。
//
// 只删自己创建的那个带随机后缀的目录，绝不递归删父目录。
// 失败回滚的恢复副本保留；其他清理失败返回确切路径，不能掩盖为成功。
func (s *snapshot) cleanup() error {
	if s.dir == "" || s.preserve {
		return nil
	}
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("remove snapshot dir %s: %w", s.dir, err)
	}
	s.dir = ""
	return nil
}

// writeFileAtomic 用同目录临时文件 + chmod + rename 写入，既恢复字节也恢复权限位。
//
// 不直接 os.WriteFile：它在目标已存在时按原 mode 写，恢复不出原始权限；
// 且非原子，中途失败会留下截断的文件。
func writeFileAtomic(path string, content []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".projectinit-restore-")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	closed, renamed := false, false
	defer func() {
		if !closed {
			if closeErr := tmp.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close temporary file %s: %w", tmpName, closeErr))
			}
		}
		if !renamed {
			if removeErr := os.Remove(tmpName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove temporary file %s: %w", tmpName, removeErr))
			}
		}
	}()

	n, err := tmp.Write(content)
	if err == nil && n != len(content) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file into %s: %w", path, err)
	}
	renamed = true
	return nil
}

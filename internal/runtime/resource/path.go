package resource

import (
	"os"
	"path/filepath"
	"runtime"
)

// ResolvePath 把配置里的相对路径解析为实际存在的绝对路径。
//
// 绝对路径**原样返回**：它已经完全指定了目标，再拼任何前缀都会指向另一个位置
// （filepath.Join("/app", "/var/log/x") == "/app/var/log/x"）。
// 配置本身允许绝对路径（生产日志路径校验就要求绝对路径位于 APP_ROOT 下），
// 因此这条分支不是防御性代码，而是真实会走到的路径。
func ResolvePath(rel string) string {
	rel = filepath.Clean(rel)
	if filepath.IsAbs(rel) {
		return rel
	}
	candidates := buildPathCandidates(rel)

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return candidates[0]
}

func buildPathCandidates(rel string) []string {
	candidates := make([]string, 0, 6)

	// 最高优先级：环境变量 APP_ROOT 指定项目根目录
	if root := os.Getenv("APP_ROOT"); root != "" {
		candidates = append(candidates,
			filepath.Join(root, rel),
			filepath.Join(root, "v2", rel),
		)
	}

	// 可执行文件的上级目录（适配 bin/gin_api 部署结构）
	if root := execRoot(); root != "" {
		candidates = append(candidates,
			filepath.Join(root, rel),
			filepath.Join(root, "v2", rel),
		)
	}

	// 开发环境回退到源码目录
	if root := sourceRoot(); root != "" {
		candidates = append(candidates,
			filepath.Join(root, rel),
			filepath.Join(root, "v2", rel),
		)
	}

	// 最后回退到相对路径（依赖工作目录）
	candidates = append(candidates,
		rel,
		filepath.Join("v2", rel),
	)

	return uniquePaths(candidates)
}

// execRoot 返回可执行文件的上级目录（即项目根目录）。
// 部署结构为 bin/gin_api，config/ 与 bin/ 同级。
func execRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(filepath.Dir(exe))
}

// sourceRoot 通过编译时源码路径推断项目根目录，仅开发环境有效。
func sourceRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))

	for _, path := range paths {
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}

		seen[path] = struct{}{}
		result = append(result, path)
	}

	return result
}

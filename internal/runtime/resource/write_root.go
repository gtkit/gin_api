package resource

import (
	"os"
	"path/filepath"
)

// WriteRoot 返回运行期写文件（日志等）的锚定根目录。
//
// 与 ResolvePath 不同：写路径的目标（如日志目录）在进程启动时通常尚不存在，
// 不能沿用"拼接后探测存在"的读路径策略，否则全部候选落空后行为退化为依赖 cwd
// （包测试的 cwd 是包目录，曾把 logs/ 刷进 internal/router 源码树）。
// 这里只选定一个稳定的根，由调用方自行 Join 相对路径。
//
// 部署红线：候选列表不含 runtime.Caller 之类的编译期源码路径，
// 构建机的本机路径永远不会成为服务器上的写入目标。
//
// 候选顺序：
//  1. APP_ROOT 环境变量（与 ResolvePath 一致，运维可显式指定）
//  2. 可执行文件上级目录（bin/gin_api 部署结构），须含 config/ 标志物；
//     go run / go test 的临时可执行文件无标志物，自然落空
//  3. 从 cwd 向上寻找 go.mod（开发与包测试环境命中项目根）
//  4. cwd 兜底
func WriteRoot() string {
	return pickWriteRoot(os.Getenv("APP_ROOT"), execRoot())
}

// pickWriteRoot 是 WriteRoot 的纯函数内核，便于按部署布局做表驱动测试。
func pickWriteRoot(appRoot, execRootDir string) string {
	if appRoot != "" {
		return filepath.Clean(appRoot)
	}
	if execRootDir != "" && hasRootMarker(execRootDir) {
		return execRootDir
	}
	if root := goModRoot(); root != "" {
		return root
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}

	return "."
}

// hasRootMarker 判断目录是否像项目根：以 config/ 目录为标志物，
// 与 deployment.sh 的部署布局（bin/ 与 config/ 同级）一致。
func hasRootMarker(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "config"))
	return err == nil && info.IsDir()
}

// goModRoot 从当前工作目录向上寻找含 go.mod 的目录。
// 仅开发/测试环境命中；服务器以二进制运行且 execRoot 候选已在其之前命中。
func goModRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

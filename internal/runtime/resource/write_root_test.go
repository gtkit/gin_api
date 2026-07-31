package resource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPickWriteRootAppRootWins(t *testing.T) {
	if got := pickWriteRoot("/data/dynamic/app", "/somewhere/else"); got != "/data/dynamic/app" {
		t.Fatalf("APP_ROOT 应有最高优先级，得到 %q", got)
	}
}

func TestPickWriteRootServerLayout(t *testing.T) {
	// 模拟服务器部署布局：RootPath 下 bin/ 与 config/ 同级
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := pickWriteRoot("", root); got != root {
		t.Fatalf("含 config/ 标志物的 execRoot 应被选中，期望 %q 得到 %q", root, got)
	}
}

func TestPickWriteRootExecWithoutMarkerFallsToGoMod(t *testing.T) {
	// go run / go test 场景：可执行文件在临时目录，无 config/ 标志物，
	// 应回退到向上寻找 go.mod 的项目根，而不是采用临时目录
	noMarker := t.TempDir()

	got := pickWriteRoot("", noMarker)
	if got == noMarker {
		t.Fatalf("无标志物的 execRoot 不应被选中: %q", got)
	}
	if _, err := os.Stat(filepath.Join(got, "go.mod")); err != nil {
		t.Fatalf("回退结果应为含 go.mod 的项目根，得到 %q", got)
	}
}

func TestWriteRootNeverReturnsSourcePath(t *testing.T) {
	// 部署红线回归项：WriteRoot 的实现不得引入 runtime.Caller 编译期路径候选。
	// 在测试环境它应锚定到项目根（go.mod 所在目录），且结果目录真实存在。
	t.Setenv("APP_ROOT", "")
	root := WriteRoot()
	if strings.TrimSpace(root) == "" {
		t.Fatal("WriteRoot 不应返回空")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("WriteRoot 结果应为真实存在的目录: %v", err)
	}
}

func TestWriteRootHonorsAppRootEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APP_ROOT", dir)
	if got := WriteRoot(); got != filepath.Clean(dir) {
		t.Fatalf("APP_ROOT 生效失败，期望 %q 得到 %q", dir, got)
	}
}

package bootstrap

import (
	"os"
	"strings"
	"testing"
)

// TestMigrationRuntimeDoesNotWireUnrelatedInfra 守住最小装配的边界。
//
// 这条契约有真实来由：迁移原本走完整 Runtime，而 Redis 连不上会直接 panic，
// 于是"Redis 挂了 ⇒ 数据库迁移也跑不了"——两个无关依赖被绑死，
// 而迁移恰恰是故障恢复时最需要能执行的操作。
//
// 用源码断言而非运行时断言：装配路径的依赖是编译期事实，
// 运行时用例要么需要真库、要么只能靠 mock 掩盖真实调用链。
func TestMigrationRuntimeDoesNotWireUnrelatedInfra(t *testing.T) {
	t.Parallel()

	src := readSourceFile(t, "migrateruntime.go")

	// 迁移路径不该出现的装配调用。每一项都对应一个"与迁移无关、却会阻断迁移"的依赖。
	forbidden := []struct {
		symbol string
		why    string
	}{
		{"NewResources", "Resources 会连带建立 Redis 等与迁移无关的连接"},
		{"initJWTWithConfig", "令牌签发器与迁移无关"},
		{"initProcessRuntime", "完整装配会连带初始化全部进程级设施"},
		{"NewRuntime", "完整 Runtime 正是此前阻断迁移的原因"},
		{"buildMailClient", "邮件客户端与迁移无关"},
		{"initFileWithConfig", "文件能力与迁移无关"},
		{"initNewsWithConfig", "通知能力与迁移无关"},
	}
	for _, f := range forbidden {
		t.Run(f.symbol, func(t *testing.T) {
			t.Parallel()
			if strings.Contains(src, f.symbol) {
				t.Fatalf("migration runtime must not wire %s: %s", f.symbol, f.why)
			}
		})
	}
}

// TestMigrationRuntimeWiresRequiredParts 反向锁住：最小装配确实包含它该有的三件事。
//
// 只断言"不含 X"是不够的——一个空实现也能通过那些断言。
func TestMigrationRuntimeWiresRequiredParts(t *testing.T) {
	t.Parallel()

	src := readSourceFile(t, "migrateruntime.go")

	required := []struct {
		symbol string
		what   string
	}{
		{"config.Load", "配置"},
		{"initLogWithConfig", "日志"},
		{"openDBForMigration", "数据库"},
		{"UseMigrationCredentials: true", "迁移凭据优先"},
	}
	for _, r := range required {
		t.Run(r.what, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(src, r.symbol) {
				t.Fatalf("migration runtime must wire %s (%s)", r.symbol, r.what)
			}
		})
	}
}

// readSourceFile 读取本包内的源文件，供装配边界断言使用。
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

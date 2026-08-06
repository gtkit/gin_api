package diagnostics

import (
	"context"
	"testing"
)

// TestDeepProbeUnavailableFailsExitCode 钉住深度诊断在探测能力建不起来时退出码非零。
//
// 此前的行为：`--deep` 下探针构建失败（数据库连不上导致 Runtime 起不来）只打印
// 一行告警，Probes 保持 nil，于是依赖检查落入 "no probe wired" 的 skipped 分支，
// 而 ExitCode 把 skipped 与 pass 同等对待 —— **退出码 0**。
// 运维脚本按 0/2/1 契约判定，于是一台连不上数据库的机器被判为全通。
//
// skipped 按通过计入退出码本身是对的（没开启的能力不该让退出码非零），
// 错的是把"连不上"表达成了"这项没开启"。
func TestDeepProbeUnavailableFailsExitCode(t *testing.T) {
	t.Parallel()

	cfg := validConfig()

	report := Run(context.Background(), cfg, Options{
		Deep:              true,
		ProbesUnavailable: true,
	})

	if got := report.ExitCode(); got != ExitFailed {
		t.Fatalf("ExitCode() = %d, want %d：探测能力不可用被当成了未开启", got, ExitFailed)
	}

	db := findCheck(t, report, "database")
	if db.Status != StatusFail {
		t.Errorf("database.Status = %q, want %q", db.Status, StatusFail)
	}
	if db.Detail == "" {
		t.Error("database.Detail 为空：使用者无法得知为什么失败")
	}
}

// TestNonDeepProbeStillSkipped 回归保护：非深度模式下依赖检查仍按未开启处理。
//
// 深度模式是使用者显式要求的连通性检查；默认模式没要求过，
// 不能因为"没探测"就让退出码非零——doctor 会被放进 project init 这类
// 连不上生产依赖的路径。
func TestNonDeepProbeStillSkipped(t *testing.T) {
	report := Run(context.Background(), configWithRealKeys(t), Options{Deep: false})

	db := findCheck(t, report, "database")
	if db.Status != StatusSkipped {
		t.Errorf("database.Status = %q, want %q", db.Status, StatusSkipped)
	}
	if got := report.ExitCode(); got != ExitOK {
		t.Errorf("ExitCode() = %d, want %d", got, ExitOK)
	}
}

// TestDeepWithoutProbesNorFailureStillSkipped 覆盖"确实没接探针"这条既有语义。
//
// 与探测能力建立失败区分开：没接探针是装配选择，不是依赖故障。
func TestDeepWithoutProbesNorFailureStillSkipped(t *testing.T) {
	t.Parallel()

	report := Run(context.Background(), validConfig(), Options{Deep: true})

	db := findCheck(t, report, "database")
	if db.Status != StatusSkipped {
		t.Errorf("database.Status = %q, want %q", db.Status, StatusSkipped)
	}
}

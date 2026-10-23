package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/shared/env"
)

// validConfig 返回一份能通过严格校验的最小配置。
// configWithRealKeys 返回一份密钥文件真实存在的配置。
//
// jwt_keys 会 stat 活跃密钥；fixture 指向不存在的路径时，任何断言"整体通过"
// 的测试都会因为一项真实失败而失败——而那项失败与被测行为无关。
func configWithRealKeys(t *testing.T) *config.Config {
	t.Helper()

	dir := t.TempDir()
	priv := filepath.Join(dir, "active.private.pem")
	pub := filepath.Join(dir, "active.public.pem")
	for _, p := range []string{priv, pub} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	cfg := validConfig()
	cfg.Log.Path = filepath.Join(dir, "logs", "app_client")
	cfg.JWT.Active.PrivateKeyPath = priv
	cfg.JWT.Active.PublicKeyPath = pub
	return cfg
}

func validConfig() *config.Config {
	return &config.Config{
		Env: env.Dev,
		App: config.AppConfig{Name: "app_client", Host: "localhost", Port: "8008"},
		// 认证模式没有默认值。取 external：它不要求 Redis，
		// 不给这些与认证无关的用例强加一个依赖。
		Auth: config.AuthConfig{Mode: config.AuthModeExternal},
		Database: config.DatabaseConfig{
			DBType: "mysql", Host: "127.0.0.1", Port: "3306", Name: "sleep", Username: "root",
		},
		Redis: config.RedisConfig{Addr: "127.0.0.1:6379", DBs: []int{0, 1, 2}},
		Log:   config.LogConfig{Level: "info", Path: "./logs/app_client"},
		JWT: config.JWTConfig{
			Active: config.JWTKeyConfig{
				KID:            "k2026a",
				PrivateKeyPath: "config/pem/k2026a.private.pem",
				PublicKeyPath:  "config/pem/k2026a.public.pem",
			},
			Issuer:    "gin-api",
			Audiences: []string{"app"},
		},
	}
}

func findCheck(t *testing.T, report Report, name string) CheckResult {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("report has no check named %q: %+v", name, report.Checks)
	return CheckResult{}
}

// TestRunWithoutDeepSkipsIO 钉住默认行为：不加 --deep 时不做任何网络/写盘 I/O。
//
// doctor 会在连不上生产依赖的环境里被执行（如 project init 的收尾自检），
// 默认就打真实连接会让它在完全正常的情况下报错，最终结果是没人再跑它。
func TestRunWithoutDeepSkipsIO(t *testing.T) {
	t.Parallel()

	var probed bool
	report := Run(context.Background(), configWithRealKeys(t), Options{
		Deep: false,
		Probes: Probes{
			PingDB:    func(context.Context) error { probed = true; return nil },
			PingRedis: func(context.Context) error { probed = true; return nil },
		},
	})

	if probed {
		t.Fatal("probes ran without --deep")
	}
	for _, name := range []string{"database", "redis", "log_dir_writable"} {
		got := findCheck(t, report, name)
		if got.Status != StatusSkipped {
			t.Fatalf("check %q status = %q, want skipped without --deep", name, got.Status)
		}
		if got.ErrorCode != CodeNotRequestedDeep {
			t.Fatalf("check %q error code = %q, want %q", name, got.ErrorCode, CodeNotRequestedDeep)
		}
	}
	if code := report.ExitCode(); code != ExitOK {
		t.Fatalf("exit code = %d, want %d: skipped checks alone must not fail", code, ExitOK)
	}
}

// TestExitCodeContract 钉住 0/2/1 三档退出码。
//
// 用普通布尔"成功/失败"会让"仅 optional 降级"与"required 失败"退成同一个码，
// 于是部署脚本要么把降级当致命、要么把致命当降级。
func TestExitCodeContract(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		checks []CheckResult
		want   int
	}{
		{name: "all pass", checks: []CheckResult{{Status: StatusPass}}, want: ExitOK},
		{name: "pass with skipped", checks: []CheckResult{{Status: StatusPass}, {Status: StatusSkipped}}, want: ExitOK},
		{name: "degraded only", checks: []CheckResult{{Status: StatusPass}, {Status: StatusDegraded}}, want: ExitDegraded},
		{name: "any failure wins", checks: []CheckResult{{Status: StatusDegraded}, {Status: StatusFail}}, want: ExitFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := (Report{Checks: tc.checks}).ExitCode(); got != tc.want {
				t.Fatalf("ExitCode() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestDeepProbeFailureSeverityFollowsRequiredness 验证 required/optional 的分档：
// 数据库不可达是 fail（退出 1），Redis 不可达是 degraded（退出 2）。
func TestDeepProbeFailureSeverityFollowsRequiredness(t *testing.T) {
	t.Parallel()

	down := func(context.Context) error { return errors.New("dial tcp 10.0.0.5:3306: connection refused") }

	t.Run("database down fails", func(t *testing.T) {
		t.Parallel()
		report := Run(context.Background(), configWithRealKeys(t), Options{
			Deep:   true,
			Probes: Probes{PingDB: down, PingRedis: func(context.Context) error { return nil }},
		})
		if got := findCheck(t, report, "database").Status; got != StatusFail {
			t.Fatalf("database status = %q, want fail", got)
		}
		if code := report.ExitCode(); code != ExitFailed {
			t.Fatalf("exit code = %d, want %d", code, ExitFailed)
		}
	})

	t.Run("redis down degrades when optional", func(t *testing.T) {
		t.Parallel()
		report := Run(context.Background(), configWithRealKeys(t), Options{
			Deep:   true,
			Probes: Probes{PingDB: func(context.Context) error { return nil }, PingRedis: down},
		})
		if got := findCheck(t, report, "redis").Status; got != StatusDegraded {
			t.Fatalf("redis status = %q, want degraded", got)
		}
		if code := report.ExitCode(); code != ExitDegraded {
			t.Fatalf("exit code = %d, want %d", code, ExitDegraded)
		}
	})

	// redis 必需时必须是 fail 而不是 degraded。
	//
	// 这条缺失的后果具体而危险：auth.mode: local 的部署里 Redis 是必需的
	// （刷新令牌、撤销表、租约都在它上面）。此前 redis 检查把 required 写死为
	// false，Redis 挂掉时 doctor --deep 报退出码 2「仅降级」，部署脚本据此放行，
	// 紧接着服务因必需 Redis 失败而根本起不来——doctor 在它最该拦住的那一刻放了行。
	//
	// 必需性由调用方注入，与装配层的能力推导同源（bootstrap.RedisRequired）。
	t.Run("redis down fails when required", func(t *testing.T) {
		t.Parallel()
		report := Run(context.Background(), configWithRealKeys(t), Options{
			Deep:          true,
			RedisRequired: true,
			Probes:        Probes{PingDB: func(context.Context) error { return nil }, PingRedis: down},
		})
		if got := findCheck(t, report, "redis").Status; got != StatusFail {
			t.Fatalf("redis status = %q, want fail：必需依赖不可达不能记成降级", got)
		}
		if code := report.ExitCode(); code != ExitFailed {
			t.Fatalf("exit code = %d, want %d：运维脚本按退出码放行，2 会让它放过一台起不来的机器",
				code, ExitFailed)
		}
	})
}

// TestProbeErrorsAreNotEchoed 钉住脱敏：原始错误常带完整 DSN（含用户名，有时含密码），
// 而 doctor 的输出经常被直接粘进工单。
func TestProbeErrorsAreNotEchoed(t *testing.T) {
	t.Parallel()

	const secret = "dial tcp root:hunter2@10.0.0.5:3306: connection refused"
	report := Run(context.Background(), configWithRealKeys(t), Options{
		Deep: true,
		Probes: Probes{
			PingDB:    func(context.Context) error { return errors.New(secret) },
			PingRedis: func(context.Context) error { return errors.New(secret) },
		},
	})

	var buf strings.Builder
	if err := WriteJSON(&buf, report); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	for _, leak := range []string{"hunter2", "10.0.0.5", "root:"} {
		if strings.Contains(buf.String(), leak) {
			t.Fatalf("report leaked %q: %s", leak, buf.String())
		}
	}
}

// TestPerCheckTimeoutIsIndependent 验证单项超时互不影响：
// 一个卡住的检查不能吃掉其余检查的预算。
func TestPerCheckTimeoutIsIndependent(t *testing.T) {
	t.Parallel()

	block := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

	start := time.Now()
	report := Run(context.Background(), configWithRealKeys(t), Options{
		Deep:    true,
		Timeout: 100 * time.Millisecond,
		Probes:  Probes{PingDB: block, PingRedis: block},
	})
	elapsed := time.Since(start)

	// 两项都超时；若串行，耗时会接近 2 倍单项预算。留足余量只证明"并发且有界"。
	if elapsed > time.Second {
		t.Fatalf("elapsed = %s, want checks to run concurrently under their own timeouts", elapsed)
	}
	if got := findCheck(t, report, "database").ErrorCode; got != CodeTimeout {
		t.Fatalf("database error code = %q, want %q", got, CodeTimeout)
	}
}

// TestReportIsStableAndMachineReadable 验证输出 schema 固定、检查按名稳定排序。
//
// 顺序抖动会让下游的 diff 与告警去重全部失效。
func TestReportIsStableAndMachineReadable(t *testing.T) {
	t.Parallel()

	report := Run(context.Background(), configWithRealKeys(t), Options{})

	var buf strings.Builder
	if err := WriteJSON(&buf, report); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	var decoded Report
	if err := json.Unmarshal([]byte(buf.String()), &decoded); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if decoded.SchemaVersion != reportSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", decoded.SchemaVersion, reportSchemaVersion)
	}

	names := make([]string, 0, len(decoded.Checks))
	for _, c := range decoded.Checks {
		names = append(names, c.Name)
		if c.Name == "" || c.Status == "" || c.ErrorCode == "" {
			t.Fatalf("check %+v has an empty required field", c)
		}
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("checks are not sorted by name: %v", names)
		}
	}
}

// TestNilConfigFailsClosed 验证配置缺失时报 fail 而不是"没有检查项所以全通过"。
func TestNilConfigFailsClosed(t *testing.T) {
	t.Parallel()

	report := Run(context.Background(), nil, Options{})
	if code := report.ExitCode(); code != ExitFailed {
		t.Fatalf("exit code = %d, want %d for a missing config", code, ExitFailed)
	}
	if got := findCheck(t, report, "config"); got.ErrorCode != CodeConfigInvalid {
		t.Fatalf("config error code = %q, want %q", got.ErrorCode, CodeConfigInvalid)
	}
}

// TestPoolConsistencyCrossValidation 验证跨字段校验。
//
// 严格解码只保证"字段名认识"，不保证组合有意义：max_idle > max_open 时
// 每个字段单看都合法，组合起来却让连接池的空闲目标永远无法达成。
func TestPoolConsistencyCrossValidation(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Database.MaxOpenConn = 25
	cfg.Database.MaxIdleConn = 50
	cfg.Redis.PoolSize = 10
	cfg.Redis.MinIdleConns = 20

	got := checkPoolConsistency(cfg)
	if got.Status != StatusFail {
		t.Fatalf("status = %q, want fail", got.Status)
	}
	if got.ErrorCode != CodeInconsistent {
		t.Fatalf("error code = %q, want %q", got.ErrorCode, CodeInconsistent)
	}
	for _, want := range []string{"maxidleconn", "min_idle_conns"} {
		if !strings.Contains(got.Detail, want) {
			t.Fatalf("detail %q does not mention %q", got.Detail, want)
		}
	}
}

// TestJWTKeyPermissions 验证密钥文件齐备时通过、缺失时被检出。
//
// 只看元数据，绝不读内容：私钥一旦出现在任何一份日志或工单里就等于泄漏。
//
// "权限过宽应被检出"那一段不在这里，见 keyperm_unix_test.go：Unix 权限位在
// Windows 上没有对应语义。整条用例加平台约束会让 Windows 连存在性检出也一起
// 失去覆盖——而 checkJWTKeys 的注释记着，正是那一项曾经在密钥缺失时误报 pass。
func TestJWTKeyPermissions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	priv := filepath.Join(dir, "priv.pem")
	pub := filepath.Join(dir, "pub.pem")

	if err := os.WriteFile(priv, []byte("x"), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	if err := os.WriteFile(pub, []byte("x"), 0o600); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	cfg := validConfig()
	cfg.JWT.Active.PrivateKeyPath = priv
	cfg.JWT.Active.PublicKeyPath = pub

	if got := checkJWTKeys(cfg); got.Status != StatusPass {
		t.Fatalf("status = %q (%s), want pass for 0600 keys", got.Status, got.Detail)
	}

	// 文件缺失必须被检出。
	if err := os.Remove(priv); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := checkJWTKeys(cfg); got.ErrorCode != CodeMissing {
		t.Fatalf("error code = %q, want %q for a missing key file", got.ErrorCode, CodeMissing)
	}
}

// TestJWTKeyCheckReadsTheActiveKeyNotTheRetiredFields 是防回归断言。
//
// 这项检查曾经读 cfg.JWT.PrivateKeyPath / PublicKeyPath——密钥轮换落地前的
// 单密钥字段。它们如今恒为空，空路径被循环跳过，于是**密钥完全缺失时
// 仍然报 pass**。而当时的测试同样在设置那两个废弃字段，因此一路全绿。
//
// 断言的形态刻意是"只填废弃字段"：如果哪天实现又退回去读它们，这里会变绿，
// 从而暴露回归。
func TestJWTKeyCheckReadsTheActiveKeyNotTheRetiredFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	retired := filepath.Join(dir, "retired.pem")
	if err := os.WriteFile(retired, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := validConfig()
	cfg.JWT.PrivateKeyPath = retired
	cfg.JWT.PublicKeyPath = retired
	cfg.JWT.Active = config.JWTKeyConfig{}

	got := checkJWTKeys(cfg)
	if got.Status != StatusFail || got.ErrorCode != CodeMissing {
		t.Fatalf("status=%q code=%q: an unconfigured active key must fail even when "+
			"the retired single-key fields point at real files", got.Status, got.ErrorCode)
	}
}

// TestJWTKeyCheckCoversRetiredPublicKeys 验证退役公钥也被检查。
//
// 它们仍参与验签：缺失会让持有旧令牌的用户在轮换后集体失效，
// 而那正是重叠窗口要避免的事。
func TestJWTKeyCheckCoversRetiredPublicKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	priv := filepath.Join(dir, "active.priv")
	pub := filepath.Join(dir, "active.pub")
	for _, p := range []string{priv, pub} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	cfg := validConfig()
	cfg.JWT.Active = config.JWTKeyConfig{PrivateKeyPath: priv, PublicKeyPath: pub}
	cfg.JWT.Previous = []config.JWTKeyConfig{{PublicKeyPath: filepath.Join(dir, "gone.pub")}}

	if got := checkJWTKeys(cfg); got.ErrorCode != CodeMissing {
		t.Fatalf("error code = %q, want %q for a missing retired public key",
			got.ErrorCode, CodeMissing)
	}
}

// TestLogDirWritableProbeLeavesNothingBehind 验证 doctor 只读不改：
// 可写性探针创建的临时文件必须被删除。
func TestLogDirWritableProbeLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := validConfig()
	cfg.Log.Path = filepath.Join(dir, "app.log")

	if got := checkLogDir(cfg, true); got.Status != StatusPass {
		t.Fatalf("status = %q (%s), want pass for a writable directory", got.Status, got.Detail)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe left %d file(s) behind: %v", len(entries), entries)
	}
}

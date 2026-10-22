package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/ormx"
)

func TestPublicOpenRejectsInvalidMinimumVersionBeforeDial(t *testing.T) {
	t.Parallel()

	for _, migration := range []bool{false, true} {
		t.Run(map[bool]string{false: "server", true: "migration"}[migration], func(t *testing.T) {
			t.Parallel()

			open := OpenDB
			if migration {
				open = OpenMigrationDB
			}
			client, err := open(t.Context(), config.DatabaseConfig{MinVersion: "invalid"}, config.DBCredentials{}, nil)
			if client != nil {
				if err := client.Close(); err != nil {
					t.Errorf("client.Close(): %v", err)
				}
				t.Fatal("invalid minimum version published a client")
			}
			if err == nil || !strings.Contains(err.Error(), "invalid database.min_version") {
				t.Fatalf("configuration must fail before dialing: %v", err)
			}
		})
	}
}

// TestCloseAndWrapClosesExactlyOnce 守住最容易泄漏的那条路径：
// OpenDB 在 return 成功之前仍然拥有连接，post-open 校验失败时若直接 return，
// 调用方拿到的是 nil，没有可关闭的句柄，连接池就泄漏了。
func TestCloseAndWrapClosesExactlyOnce(t *testing.T) {
	t.Parallel()

	cause := errors.New("version check failed")
	calls := 0
	err := closeAndWrap(func() error { calls++; return nil }, cause)

	if calls != 1 {
		t.Fatalf("closer called %d times, want exactly 1", calls)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("root cause must remain matchable by errors.Is, got %v", err)
	}
}

// TestCloseAndWrapKeepsRootCauseWhenCloseFails 锁住主次关系：
// 关闭失败只是附加信息，不能盖掉真正的失败原因——
// 反过来会让调用方误以为问题出在关闭上。
func TestCloseAndWrapKeepsRootCauseWhenCloseFails(t *testing.T) {
	t.Parallel()

	cause := errors.New("session semantics mismatch")
	closeErr := errors.New("driver already shut down")
	err := closeAndWrap(func() error { return closeErr }, cause)

	if !errors.Is(err, cause) {
		t.Fatalf("root cause must still be matchable by errors.Is, got %v", err)
	}
	if !strings.Contains(err.Error(), closeErr.Error()) {
		t.Fatalf("close failure should be reported as additional information, got %v", err)
	}
}

func TestCloseAndWrapReturnsCauseUnchangedOnCleanClose(t *testing.T) {
	t.Parallel()

	cause := errors.New("boom")
	if got := closeAndWrap(func() error { return nil }, cause); got.Error() != cause.Error() {
		t.Fatalf("closeAndWrap() = %q, want the cause unchanged (%q)", got, cause)
	}
}

// testDBConfig 是超时断言用的最小配置，只填影响被断言项的字段。
func testDBConfig(maxLifeSeconds int) config.DatabaseConfig {
	return config.DatabaseConfig{
		Host:               "db.invalid",
		Port:               "3306",
		Name:               "app",
		MaxOpenConn:        20,
		MaxIdleConn:        10,
		MaxLifeSeconds:     maxLifeSeconds,
		MaxIdleTimeSeconds: 60,
	}
}

// resolvedIOTimeouts 把选项应用到 ormx 配置上再读回实际生效的 I/O deadline。
//
// 断言必须落在**生效值**而不是本包算出的中间变量上：选项是闭包，写错了传哪个
// 字段（read 写成 write、漏传一个）在中间变量上完全看不出来，而那正是这里要防的。
func resolvedIOTimeouts(opts []ormx.Option) (read, write time.Duration) {
	cfg := ormx.NewConfig(opts...)
	return cfg.MySQL.ReadTimeout, cfg.MySQL.WriteTimeout
}

// TestServerIOTimeoutTakesTheSmallerOfLifetimeAndCap 锁住服务路径的两条分支。
//
// 两条都必须有用例：`min(maxLife, 30s)` 里"较小值"那条只在 maxLifeSeconds < 30s
// 时生效，是重构时最容易被简化掉的分支，而简化后在默认配置下毫无症状。
func TestServerIOTimeoutTakesTheSmallerOfLifetimeAndCap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		maxLifeSeconds int
		want           time.Duration
	}{
		{"寿命大于上限时取 30s 上限", 3600, 30 * time.Second},
		{"寿命小于上限时取寿命", 10, 10 * time.Second},
		{"寿命恰好等于上限", 30, 30 * time.Second},
		{"寿命未配置时取默认寿命与上限的较小值", 0, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			read, write := resolvedIOTimeouts(
				serverOpenOptions(testDBConfig(tt.maxLifeSeconds), config.DBCredentials{}))
			if read != tt.want {
				t.Fatalf("read timeout = %v, want %v", read, tt.want)
			}
			if write != tt.want {
				t.Fatalf("write timeout = %v, want %v", write, tt.want)
			}
		})
	}
}

// TestMigrationIOTimeoutIsUnset 是本 change 的反证测试。
//
// 迁移路径若退回服务路径的公式，这个用例必须失败：驱动的读 deadline 作用于
// "发出语句后等待响应包"，对 DDL 等价于给单条语句的执行时长封顶，而 MySQL 的
// DDL 不随客户端断连回滚——超时会留下 schema 已变更、版本表未记录的静默分叉。
//
// 断言 0 而不是某个大值：0 是"不设 deadline"在驱动层的表达（go-sql-driver 在
// readTimeout <= 0 时不设读 deadline），任何有限值被撞上时都会重新产生该分叉。
func TestMigrationIOTimeoutIsUnset(t *testing.T) {
	t.Parallel()

	// 连接寿命取值不影响结论：迁移路径与该配置项无关。
	for _, maxLifeSeconds := range []int{0, 10, 3600} {
		read, write := resolvedIOTimeouts(
			migrationOpenOptions(testDBConfig(maxLifeSeconds), config.DBCredentials{}))
		if read != 0 {
			t.Fatalf("maxLifeSeconds=%d: migration read timeout = %v, want 0 (no driver deadline)",
				maxLifeSeconds, read)
		}
		if write != 0 {
			t.Fatalf("maxLifeSeconds=%d: migration write timeout = %v, want 0 (no driver deadline)",
				maxLifeSeconds, write)
		}
	}
}

// TestOnlyIOTimeoutDiffersBetweenPaths 锁住"只改超时"这个声明本身。
//
// 没有它，"两条路径除超时外完全一致"只是声称：给其中一个入口顺手加一个选项
// （改字符集、改时区、改连接池），其余测试全都不会失败——而那些项的不一致
// 同样是静默的（时间偏移、排序差异、连接数打满）。
//
// 断言落在**两个入口各自产出的选项**上，而不是拿手写的超时值去调公共函数：
// 后者的"其余项一致"由共用代码结构保证，测不出任何东西；真正要防的是日后
// 有人只给一个入口追加选项，那只有比较入口才能发现。
//
// 比对 DSN 而不是逐字段比结构体：DSN 是真正交给驱动的东西，且不必依赖
// ormx 内部字段的可比较性与导出情况。
func TestOnlyIOTimeoutDiffersBetweenPaths(t *testing.T) {
	t.Parallel()

	dbCfg := testDBConfig(3600)
	creds := config.DBCredentials{Username: "u", Password: "p"}

	serverDSN, err := ormx.NewConfig(serverOpenOptions(dbCfg, creds)...).RedactedDSN()
	if err != nil {
		t.Fatalf("build server DSN: %v", err)
	}
	migrateDSN, err := ormx.NewConfig(migrationOpenOptions(dbCfg, creds)...).RedactedDSN()
	if err != nil {
		t.Fatalf("build migration DSN: %v", err)
	}

	if stripIOTimeouts(serverDSN) != stripIOTimeouts(migrateDSN) {
		t.Fatalf("两条路径除 I/O deadline 外必须一致\nserver:  %s\nmigrate: %s",
			serverDSN, migrateDSN)
	}
	// 同时确认这个测试没有把差异一起抹掉：归一化前两者必须不同。
	if serverDSN == migrateDSN {
		t.Fatal("两条路径的 DSN 完全相同，说明 I/O deadline 的区分没有生效")
	}
}

// TestMigrationOpenStillHonorsStartupBudget 锁住"去掉驱动 deadline 不等于失去上界"。
//
// 这是 OpenMigrationDB 的关键前提：驱动 deadline 只兜不受 ctx 约束的路径，而连接
// 建立阶段（Ping 与两次会话校验）本就走 ctx。若哪天有人在这条路径上改成
// context.Background() 或 WithoutCancel，迁移就会在库不可达时无限挂起，而
// migration-baseline 的「数据库不可达退出码非零」将静默失效。
//
// 不依赖真实数据库：用已到期的 ctx，断言错误可被 errors.Is 认出是 ctx 到期，
// 从而证明约束来自 ctx 而不是某个巧合的网络失败。
func TestMigrationOpenStillHonorsStartupBudget(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	client, err := OpenMigrationDB(ctx, testDBConfig(3600), config.DBCredentials{}, nil)
	if err == nil {
		if client != nil {
			if err := client.Close(); err != nil {
				t.Errorf("client.Close(): %v", err)
			}
		}
		t.Fatal("expired context must fail the migration open, not proceed")
	}
	if client != nil {
		t.Fatal("failed open must not hand back a client (nothing for the caller to close)")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error must be traceable to the caller's budget, got %v", err)
	}
}

// stripIOTimeouts 去掉 DSN 里的读写 deadline 参数，用于比较其余部分。
func stripIOTimeouts(dsn string) string {
	var kept []string
	base, params, found := strings.Cut(dsn, "?")
	if !found {
		return dsn
	}
	for param := range strings.SplitSeq(params, "&") {
		if strings.HasPrefix(param, "readTimeout=") || strings.HasPrefix(param, "writeTimeout=") {
			continue
		}
		kept = append(kept, param)
	}
	return base + "?" + strings.Join(kept, "&")
}

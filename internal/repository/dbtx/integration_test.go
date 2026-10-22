//go:build integration

// 真实 MySQL 集成测试：事务管理器的 context 绑定。
//
// 覆盖的轴——**只有真实数据库能回答的那个**：`BeginTx` 是否真的收到了调用方的 ctx。
// mock 里"传了 ctx"与"BEGIN 受 ctx 约束"看起来一样；只有真实连接会在
// ctx 已取消时让 BEGIN 立刻失败。绑定漏了的后果是半绑定的故障形态——
// 请求侧已超时返回，goroutine 仍挂在连接池上堆积，而观测上看着像"超时正常生效"。
//
// 安全设计：回调内只做只读查询，不写任何业务表。
//
// 运行方式（DB 必须与本仓迁移状态一致，建议单开一个库，见 docs/testing.md）：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) APP_DATABASE__NAME=gin_api_itest \
//	  go test -tags integration -run TestIntegration ./internal/repository/dbtx/
package dbtx_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	defaultconfig "github.com/gtkit/gin-api/internal/plugin/builtin/config"

	"github.com/gtkit/gin-api/internal/repository/dbtx"

	"github.com/gtkit/ormx"
)

// skipOrFail 统一集成测试的逃生口语义：
// 默认（本地无 DB）Skip；CI 设 REQUIRE_DB_INTEGRATION=1 时 Skip 变 Fatal。
func skipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("REQUIRE_DB_INTEGRATION") == "1" {
		t.Fatalf("REQUIRE_DB_INTEGRATION=1 但集成前置不满足: "+format, args...)
	}
	t.Skipf(format, args...)
}

func openTestDB(t *testing.T) *ormx.Client {
	t.Helper()

	cfg, err := defaultconfig.Load()
	if err != nil {
		skipOrFail(t, "config load failed (set APP_ROOT to repo root): %v", err)
		return nil
	}

	defer func() {
		if r := recover(); r != nil {
			skipOrFail(t, "mysql open panicked (db unreachable?): %v", r)
		}
	}()

	client := ormx.MustOpen(t.Context(),
		ormx.WithHost(cfg.Database.Host),
		ormx.WithPort(cfg.Database.Port),
		ormx.WithDatabase(cfg.Database.Name),
		ormx.WithUser(cfg.Database.Username),
		ormx.WithPassword(cfg.Database.Password),
	)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close integration db: %v", err)
		}
	})
	return client
}

// A single connection makes an accidental independent nested Begin observable.
// The temporary table belongs only to this test connection and disappears on Close.
func TestIntegrationNestedTransactionsAreAtomic(t *testing.T) {
	for _, mode := range []string{"outer-failure", "handled-inner-failure", "success", "panic"} {
		t.Run(mode, func(t *testing.T) {
			client := openTestDB(t)
			if client == nil {
				return
			}
			client.SQLDB().SetMaxOpenConns(1)
			client.SQLDB().SetMaxIdleConns(1)
			db := client.DB()
			if err := db.WithContext(t.Context()).Exec("CREATE TEMPORARY TABLE remediation_tx (id INT PRIMARY KEY) ENGINE=InnoDB").Error; err != nil {
				t.Fatal(err)
			}
			manager := dbtx.NewManager(db)
			failure := errors.New("requested rollback")
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			var err error
			var panicked any
			func() {
				defer func() { panicked = recover() }()
				err = manager.Do(ctx, func(outer context.Context) error {
					if err := dbtx.DB(outer, db).Exec("INSERT INTO remediation_tx VALUES (1)").Error; err != nil {
						return err
					}
					innerErr := manager.Do(outer, func(inner context.Context) error {
						if err := dbtx.DB(inner, db).Exec("INSERT INTO remediation_tx VALUES (2)").Error; err != nil {
							return err
						}
						switch mode {
						case "handled-inner-failure":
							return failure
						case "panic":
							panic(failure)
						default:
							return nil
						}
					})
					if mode == "handled-inner-failure" {
						if !errors.Is(innerErr, failure) {
							return errors.Join(errors.New("missing inner failure"), innerErr)
						}
						return dbtx.DB(outer, db).Exec("INSERT INTO remediation_tx VALUES (3)").Error
					}
					if innerErr != nil {
						return innerErr
					}
					if mode == "outer-failure" {
						return failure
					}
					return nil
				})
			}()
			if mode == "panic" {
				if panicked != failure {
					t.Fatalf("panic=%v", panicked)
				}
			} else if mode == "outer-failure" {
				if !errors.Is(err, failure) {
					t.Fatalf("rollback error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var count, sum int
			if err := db.WithContext(ctx).Raw("SELECT COUNT(*), COALESCE(SUM(id),0) FROM remediation_tx").Row().Scan(&count, &sum); err != nil {
				t.Fatal(err)
			}
			wantCount, wantSum := 0, 0
			if mode == "success" {
				wantCount, wantSum = 2, 3
			}
			if mode == "handled-inner-failure" {
				wantCount, wantSum = 2, 4
			}
			if count != wantCount || sum != wantSum {
				t.Fatalf("count=%d sum=%d, want %d/%d", count, sum, wantCount, wantSum)
			}
			t.Logf("max_open=1 mode=%s count=%d sum=%d", mode, count, sum)
		})
	}
}

func TestIntegrationPoolWaitRespectsDeadline(t *testing.T) {
	client := openTestDB(t)
	if client == nil {
		return
	}
	client.SQLDB().SetMaxOpenConns(1)
	conn, err := client.SQLDB().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("release pool slot: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	called := false
	err = dbtx.NewManager(client.DB()).Do(ctx, func(context.Context) error { called = true; return nil })
	if called || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

// TestIntegrationDoBindsCallerContext 钉住事务入口受调用方 ctx 约束。
//
// 这是 mock-only 单测抓不到的一类缺陷：`Manager.Do` 曾直接在底座 db 上开事务，
// 于是 GORM 用 background context 执行 BeginTx——事务**内**的查询绑定了 ctx、
// 会按时超时，而取连接与 BEGIN/COMMIT/ROLLBACK 不受任何约束。
// 连接池耗尽时请求侧超时已经返回，goroutine 仍挂在池上堆积，
// 而观测上看到的是"超时正常生效"，与真实原因相反。
//
// 断言方式：传入一个**已取消**的 ctx。绑定正确时 BeginTx 立刻以 ctx 错误失败、
// 回调根本不执行；未绑定时 BEGIN 会照常成功并执行回调。
//
// 运行方式：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) go test -tags integration \
//	  -run TestIntegration ./internal/repository/dbtx/
func TestIntegrationDoBindsCallerContext(t *testing.T) {
	client := openTestDB(t)
	if client == nil {
		return
	}

	manager := dbtx.NewManager(client.DB())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	err := manager.Do(ctx, func(context.Context) error {
		called = true
		return nil
	})

	if called {
		t.Fatal("回调被执行了：事务入口没有绑定调用方 ctx，" +
			"已取消的请求仍然开出了事务")
	}
	if err == nil {
		t.Fatal("Do 返回 nil：已取消的 ctx 应当让事务建立失败")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestIntegrationDoCommitsWithLiveContext 回归保护：正常 ctx 下事务照常提交。
//
// 只断言"能跑通并提交"，不写任何业务表——回调内只做一次只读查询。
func TestIntegrationDoCommitsWithLiveContext(t *testing.T) {
	client := openTestDB(t)
	if client == nil {
		return
	}

	manager := dbtx.NewManager(client.DB())

	var got int
	err := manager.Do(context.Background(), func(ctx context.Context) error {
		tx, ok := dbtx.From(ctx)
		if !ok {
			t.Fatal("回调内取不到事务句柄")
		}
		return tx.WithContext(ctx).Raw("SELECT 1").Scan(&got).Error
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", got)
	}
}

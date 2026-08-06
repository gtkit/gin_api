//go:build integration

package dbtx_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
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

	cfg, err := config.Load()
	if err != nil {
		skipOrFail(t, "config load failed (set APP_ROOT to repo root): %v", err)
		return nil
	}

	defer func() {
		if r := recover(); r != nil {
			skipOrFail(t, "mysql open panicked (db unreachable?): %v", r)
		}
	}()

	return ormx.MustOpen(context.Background(),
		ormx.WithHost(cfg.Database.Host),
		ormx.WithPort(cfg.Database.Port),
		ormx.WithDatabase(cfg.Database.Name),
		ormx.WithUser(cfg.Database.Username),
		ormx.WithPassword(cfg.Database.Password),
	)
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

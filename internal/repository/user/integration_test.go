//go:build integration

// 真实 MySQL 集成测试：user 仓储与真实 schema 的一致性。
//
// 覆盖的轴——**只有真实数据库能回答的那个**：更新用的列名与真实 users 表对不对得上
// （`nick_name` vs `nickname` 这类）。mock 按调用方给的列名回答，永远一致；
// 真实 MySQL 会返回 Unknown column。
//
// 安全设计：所有写操作包在事务里并强制回滚，零持久化，不污染库数据；
// 只开 MySQL、不拉 Redis；DB 连不上时 Skip 而非 panic。
//
// 运行方式（DB 必须与本仓迁移状态一致，建议单开一个库，见 docs/testing.md）：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) APP_DATABASE__NAME=gin_api_itest \
//	  go test -tags integration -run TestIntegration ./internal/repository/user/
package user_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	"github.com/gtkit/gin-api/internal/repository/user"

	"github.com/gtkit/ormx"
)

// skipOrFail 统一集成测试的逃生口语义：
// 默认（本地无 DB）Skip；CI 设 REQUIRE_DB_INTEGRATION=1 时 Skip 变 Fatal——
// 强制 job 里环境异常必须是红灯，不允许绿灯里藏 SKIP。
func skipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("REQUIRE_DB_INTEGRATION") == "1" {
		t.Fatalf("REQUIRE_DB_INTEGRATION=1 但集成前置不满足: "+format, args...)
	}
	t.Skipf(format, args...)
}

// TestIntegrationUserUpdateNicknameRealSchema 用真实 MySQL 验证 profile 更新的列名
// 与真实 users 表 schema 一致——这是 mock-only 单测抓不到的一类 bug（nick_name vs nickname）。
//
// 安全设计：所有写操作都包在事务里，结束时强制回滚，零持久化，不污染库数据。
// 只开 MySQL、不拉 Redis；DB 连不上时 skip 而非 panic。
//
// 运行方式（DB 必须是可丢弃的开发/测试库，配置取自 config/env/env.yml）：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) go test -tags integration \
//	  -run TestIntegration ./internal/repository/user/
func TestIntegrationUserUpdateNicknameRealSchema(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		skipOrFail(t, "config load failed (set APP_ROOT to repo root): %v", err)
		return
	}

	defer func() {
		if r := recover(); r != nil {
			skipOrFail(t, "mysql open panicked (db unreachable?): %v", r)
		}
	}()
	client := ormx.MustOpen(context.Background(),
		ormx.WithHost(cfg.Database.Host),
		ormx.WithPort(cfg.Database.Port),
		ormx.WithDatabase(cfg.Database.Name),
		ormx.WithUser(cfg.Database.Username),
		ormx.WithPassword(cfg.Database.Password),
	)
	db := client.DB()

	repo := user.New(db)
	mgr := dbtx.NewManager(db)
	rollback := errors.New("intentional rollback: integration test never persists")

	err = mgr.Do(context.Background(), func(ctx context.Context) error {
		// 列名写错(如 nick_name)时真实 MySQL 会返回 Unknown column，测试即失败。
		if e := repo.Update(ctx, 1, map[string]any{"nickname": "itest_nickname"}); e != nil {
			return e
		}
		return rollback // 强制回滚，绝不提交
	})
	if err != nil && !errors.Is(err, rollback) {
		t.Fatalf("nickname update against real schema failed: %v", err)
	}
}

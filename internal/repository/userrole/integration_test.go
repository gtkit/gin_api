//go:build integration

// 真实 MySQL 集成测试：角色仓储与真实 schema 的一致性。
//
// 覆盖的轴——**只有真实数据库能回答的那个**：查询用的列名与真实 user_roles 表
// 对不对得上（`user_id` vs `userid` 这类）。它同时是权威角色源的读路径，
// 列名错了表现为"所有人都没有权限"，而那与"确实没配角色"在业务上无法区分。
//
// 安全设计：写操作包在事务里并强制回滚，零持久化，不污染库数据。
//
// 运行方式（DB 必须与本仓迁移状态一致，建议单开一个库，见 docs/testing.md）：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) APP_DATABASE__NAME=gin_api_itest \
//	  go test -tags integration -run TestIntegration ./internal/repository/userrole/
package userrole_test

import (
	"context"
	"errors"
	"os"
	"testing"

	defaultconfig "github.com/gtkit/gin-api/internal/plugin/builtin/config"

	"github.com/gtkit/ormx"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	"github.com/gtkit/gin-api/internal/repository/userrole"
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

// TestIntegrationHasRoleRealSchema 用真实 MySQL 验证角色查询的列名与 user_roles 的
// 真实 schema 一致——这是 mock-only 单测抓不到的一类 bug（user_id vs userid）。
//
// 安全设计：写操作包在事务里并强制回滚，零持久化，不污染库数据。
//
// 运行方式（DB 必须是可丢弃的开发/测试库，配置取自 config/env/env.yml）：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) go test -tags integration \
//	  -run TestIntegration ./internal/repository/userrole/
func TestIntegrationHasRoleRealSchema(t *testing.T) {
	cfg, err := defaultconfig.Load()
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

	repo := userrole.New(db)
	mgr := dbtx.NewManager(db)
	rollback := errors.New("intentional rollback: integration test never persists")

	const probeUserID int64 = 1
	const probeRole = "itest_role"

	err = mgr.Do(context.Background(), func(ctx context.Context) error {
		// 授予前：必须查不到。列名写错时真实 MySQL 会返回 Unknown column，测试即失败。
		has, e := repo.HasRole(ctx, probeUserID, probeRole)
		if e != nil {
			return e
		}
		if has {
			return errors.New("角色尚未授予却查到了")
		}

		// 授予后：必须查得到。这一步同时验证唯一索引的列顺序可用于该查询。
		if e := dbtx.DB(ctx, db).Create(&models.UserRole{
			UserID: probeUserID,
			Role:   probeRole,
		}).Error; e != nil {
			return e
		}

		has, e = repo.HasRole(ctx, probeUserID, probeRole)
		if e != nil {
			return e
		}
		if !has {
			return errors.New("角色已授予却查不到 —— 查询条件与真实 schema 不一致")
		}

		return rollback // 强制回滚，绝不提交
	})
	if err != nil && !errors.Is(err, rollback) {
		t.Fatalf("user_roles query against real schema failed: %v", err)
	}
}

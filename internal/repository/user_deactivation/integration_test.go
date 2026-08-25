//go:build integration

// 真实 MySQL 集成测试：注销记录的保留期清理。
//
// 覆盖的轴——**只有真实数据库能回答的那些**：列名与真实 schema 一致，
// 以及 `RowsAffected` 是否等于实际删除行数——**分批循环的终止条件全靠它**。
// mock 里那个返回值是测试自己写的，它证明不了真实驱动会那样回答。
//
// 安全设计：写入与删除都在事务内，结束时强制回滚，零持久化。
//
// 运行方式（DB 必须与本仓迁移状态一致，建议单开一个库，见 docs/testing.md）：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) APP_DATABASE__NAME=gin_api_itest \
//	  go test -tags integration -run TestIntegration ./internal/repository/user_deactivation/
package userdeactivation_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	userdeactivation "github.com/gtkit/gin-api/internal/repository/user_deactivation"

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

func openTestDB(t *testing.T) *dbtx.Manager {
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
	client := ormx.MustOpen(context.Background(),
		ormx.WithHost(cfg.Database.Host),
		ormx.WithPort(cfg.Database.Port),
		ormx.WithDatabase(cfg.Database.Name),
		ormx.WithUser(cfg.Database.Username),
		ormx.WithPassword(cfg.Database.Password),
	)
	mgr := dbtx.NewManager(client.DB())
	t.Cleanup(func() { _ = client.Close() })
	return &mgr
}

// TestIntegrationDeleteOlderThanAgainstRealSchema 用真实 MySQL 验证批量删除。
//
// mock 证明不了的三件事，只有真实 SQL 能回答：
//  1. `Unscoped + Limit + Delete` 在 MySQL 上是否真的生成带 LIMIT 的 DELETE
//     （GORM 对某些方言会忽略 Limit，那会让"分批"退化成一次删光）；
//  2. 列名 created_at 与真实 schema 是否一致；
//  3. 返回的 RowsAffected 是否等于实际删除行数——分批循环的终止条件全靠它。
//
// 安全设计：写入与删除都在事务内，结束时强制回滚，零持久化。
//
// 运行方式（DB 必须是可丢弃的开发/测试库）：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) go test -tags integration \
//	  -run TestIntegration ./internal/repository/user_deactivation/
func TestIntegrationDeleteOlderThanAgainstRealSchema(t *testing.T) {
	mgr := openTestDB(t)
	if mgr == nil {
		return
	}

	rollback := errors.New("intentional rollback: integration test never persists")
	now := time.Now()

	err := mgr.Do(context.Background(), func(ctx context.Context) error {
		repo := userdeactivationFromCtx(ctx, t)

		tx, _ := dbtx.From(ctx)

		cutoff := now.Add(-30 * 24 * time.Hour)

		// **先把基线清零**：真实库里可能已存在早于 cutoff 的历史档案，而下面
		// 第二批删除不带 LIMIT，会把它们一并算进删除数，使计数断言失真——
		// 本测试此前只在空库上成立，一旦库里有一条旧记录就会红。
		// 清理发生在事务内，最终随 rollback 一并撤销，真实数据不受影响。
		for {
			purged, e := repo.DeleteOlderThan(ctx, cutoff, 1000)
			if e != nil {
				return e
			}
			if purged == 0 {
				break
			}
		}

		// 造 5 条旧档案 + 2 条新档案。
		//
		// created_at 由 GORM 在 Create 时自动填当前时刻，因此"旧记录"要显式
		// 改写——直接在字面量里设置会被自动填充覆盖，那样三条断言全都会失真。
		oldIDs := make([]int64, 0, 5)
		for i := range 5 {
			record := &models.UserDeactivation{UserID: int64(900000 + i), Channel: "itest"}
			if e := repo.Create(ctx, record); e != nil {
				return e
			}
			oldIDs = append(oldIDs, record.ID)
		}
		if e := tx.Exec(
			"UPDATE user_deactivations SET created_at = ? WHERE id IN ?",
			now.Add(-100*24*time.Hour), oldIDs,
		).Error; e != nil {
			return e
		}

		for i := range 2 {
			if e := repo.Create(ctx, &models.UserDeactivation{
				UserID: int64(910000 + i), Channel: "itest",
			}); e != nil {
				return e
			}
		}

		// 第一批限 2 行：**必须恰好删 2 行**。
		// LIMIT 被忽略时这里会返回 5，分批循环因此失去意义。
		deleted, e := repo.DeleteOlderThan(ctx, cutoff, 2)
		if e != nil {
			return e
		}
		if deleted != 2 {
			t.Errorf("first batch deleted %d rows, want exactly 2: the LIMIT is not taking effect", deleted)
		}

		// 删完剩余的旧档案。
		deleted, e = repo.DeleteOlderThan(ctx, cutoff, 100)
		if e != nil {
			return e
		}
		if deleted != 3 {
			t.Errorf("second batch deleted %d rows, want the remaining 3", deleted)
		}

		// 新档案不该被碰：截止时刻之后的记录必须留下。
		deleted, e = repo.DeleteOlderThan(ctx, cutoff, 100)
		if e != nil {
			return e
		}
		if deleted != 0 {
			t.Errorf("a third pass deleted %d rows, want 0: records newer than the cutoff must survive", deleted)
		}

		return rollback
	})
	if err != nil && !errors.Is(err, rollback) {
		t.Fatalf("delete against real schema failed: %v", err)
	}
}

// TestIntegrationDeleteOlderThanRejectsNonPositiveBatch 验证批量下界在真实链路上也生效。
func TestIntegrationDeleteOlderThanRejectsNonPositiveBatch(t *testing.T) {
	mgr := openTestDB(t)
	if mgr == nil {
		return
	}

	rollback := errors.New("intentional rollback")
	err := mgr.Do(context.Background(), func(ctx context.Context) error {
		repo := userdeactivationFromCtx(ctx, t)
		if _, e := repo.DeleteOlderThan(ctx, time.Now(), 0); e == nil {
			t.Error("a zero batch limit was accepted: an unbounded DELETE defeats batching")
		}
		return rollback
	})
	if err != nil && !errors.Is(err, rollback) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// userdeactivationFromCtx 在事务内构造仓储。
//
// 仓储经 dbtx 感知事务，因此这里传的底座 db 不影响它落到同一个事务上。
func userdeactivationFromCtx(ctx context.Context, t *testing.T) *userdeactivation.Repository {
	t.Helper()

	tx, ok := dbtx.From(ctx)
	if !ok {
		t.Fatal("no transaction in context: the test would persist data")
	}
	return userdeactivation.New(tx)
}

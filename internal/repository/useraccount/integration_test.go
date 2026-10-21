//go:build integration

// 真实 MySQL 集成测试：账号状态查询与真实 users 表的一致性。
//
// 覆盖的轴——**只有真实数据库能回答的那个**：查询用的列名与真实 users 表
// 对不对得上（`vip_expire_at` vs `vipexpireat` 这类）。这两个方法在鉴权链路上：
// ValidateUser 是**每个带令牌的请求**都要走的，列名错了表现为全体用户 503；
// ValidateVIP 的投影列写错则表现为全体会员突然失去权益。
//
// 安全设计：写操作包在事务里并强制回滚，零持久化，不污染库数据。
//
// 运行方式（DB 必须与本仓迁移状态一致，建议单开一个库，见 docs/testing.md）：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) APP_DATABASE__NAME=gin_api_itest \
//	  go test -tags integration -run TestIntegration ./internal/repository/useraccount/
package useraccount_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	defaultconfig "github.com/gtkit/gin-api/internal/plugin/builtin/config"

	"github.com/gtkit/ormx"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	"github.com/gtkit/gin-api/internal/repository/useraccount"
)

func skipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("REQUIRE_DB_INTEGRATION") == "1" {
		t.Fatalf("REQUIRE_DB_INTEGRATION=1 但集成前置不满足: "+format, args...)
	}
	t.Skipf(format, args...)
}

// TestIntegrationAccountStateAgainstRealSchema 验证账号状态与会员判定的读路径。
func TestIntegrationAccountStateAgainstRealSchema(t *testing.T) {
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

	// 固定时钟：会员到期边界是最该被测到的一条分支，而没有注入点时它只能
	// 靠 sleep 或改系统时间去碰。
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	repo := useraccount.NewWithClock(db, func() time.Time { return now })
	mgr := dbtx.NewManager(db)
	rollback := errors.New("intentional rollback: integration test never persists")

	err = mgr.Do(context.Background(), func(ctx context.Context) error {
		expired := now.Add(-time.Hour)
		active := now.Add(time.Hour)

		normal := &models.User{
			Nickname: "itest", Phone: "19000000001", Password: "x",
			Status: models.UserStatusNormal, VipStatus: models.VipStatusActive,
			VipExpireAt: &active,
		}
		if e := dbtx.DB(ctx, db).Create(normal).Error; e != nil {
			return e
		}
		disabled := &models.User{
			Nickname: "itest2", Phone: "19000000002", Password: "x",
			Status: models.UserStatusDisable, VipStatus: models.VipStatusActive,
		}
		if e := dbtx.DB(ctx, db).Create(disabled).Error; e != nil {
			return e
		}

		ok, e := repo.ValidateUser(ctx, normal.ID)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("可用账号被判为不可用 —— 每个带令牌的请求都会 401")
		}

		ok, e = repo.ValidateUser(ctx, disabled.ID)
		if e != nil {
			return e
		}
		if ok {
			return errors.New("被禁用的账号仍然通过了鉴权")
		}

		ok, e = repo.ValidateUser(ctx, 0)
		if e != nil {
			return errors.New("非法用户 ID 被报成了数据源故障（会被映射成 503）")
		}
		if ok {
			return errors.New("用户 ID 0 通过了鉴权")
		}

		ok, e = repo.ValidateVIP(ctx, normal.ID)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("未到期的会员被判为非会员 —— 投影列与真实 schema 不一致")
		}

		// 到期时刻本身算已过期：用 Before 判定会让这一瞬间仍然放行。
		if e := dbtx.DB(ctx, db).Model(normal).
			Update("vip_expire_at", now).Error; e != nil {
			return e
		}
		ok, e = repo.ValidateVIP(ctx, normal.ID)
		if e != nil {
			return e
		}
		if ok {
			return errors.New("恰好到期的会员仍被判为有效")
		}

		if e := dbtx.DB(ctx, db).Model(normal).
			Update("vip_expire_at", expired).Error; e != nil {
			return e
		}
		ok, e = repo.ValidateVIP(ctx, normal.ID)
		if e != nil {
			return e
		}
		if ok {
			return errors.New("已过期的会员仍被判为有效")
		}

		// 被禁用的账号一律不是会员：会员判定必须先过账号状态这一关。
		ok, e = repo.ValidateVIP(ctx, disabled.ID)
		if e != nil {
			return e
		}
		if ok {
			return errors.New("被禁用的账号仍被判为有效会员")
		}

		return rollback
	})
	if err != nil && !errors.Is(err, rollback) {
		t.Fatalf("account state query against real schema failed: %v", err)
	}
}

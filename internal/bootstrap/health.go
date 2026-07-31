package bootstrap

import (
	"context"
	"fmt"

	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/router"
)

// runtimeHealthChecker 实现 router.HealthChecker，基于运行时 DAO 层做探活。
type runtimeHealthChecker struct {
	dao data.Dao
}

// 编译时接口实现检查。
var _ router.HealthChecker = (*runtimeHealthChecker)(nil)

func newHealthChecker(dao data.Dao) router.HealthChecker {
	return &runtimeHealthChecker{dao: dao}
}

func (h *runtimeHealthChecker) PingDB(ctx context.Context) error {
	if h.dao == nil {
		return fmt.Errorf("database not initialized")
	}
	mdb := h.dao.Mdb()
	if mdb == nil {
		return fmt.Errorf("mysql connection not available")
	}

	sqlDB, err := mdb.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB: %w", err)
	}

	return sqlDB.PingContext(ctx)
}

func (h *runtimeHealthChecker) PingRedis(ctx context.Context) error {
	if h.dao == nil {
		return fmt.Errorf("redis not initialized")
	}
	rdb := h.dao.Rdb(0)
	if rdb == nil {
		return fmt.Errorf("redis connection not available")
	}

	return rdb.Ping(ctx).Err()
}

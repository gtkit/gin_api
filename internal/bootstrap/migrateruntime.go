package bootstrap

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	logger "github.com/gtkit/logger/v2"
	"github.com/gtkit/ormx"

	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
)

// openDBForMigration 是注入点，便于测试断言迁移路径只建数据库连接、不碰其他基础设施。
var openDBForMigration = data.OpenDB

// defaultMigrationStartupTimeout 是迁移建立数据库连接的启动预算。
const defaultMigrationStartupTimeout = 15 * time.Second

// MigrationRuntime 是迁移专用的最小装配：配置 + 日志 + 数据库。
//
// 刻意**不**初始化缓存、令牌签发器等与迁移无关的基础设施。
// 原因很实际：把无关依赖绑进迁移路径，会让其中任何一个不可用时连数据库迁移都跑不了——
// 而迁移恰恰是故障恢复时最需要能执行的操作。此前 migrate 走完整 Runtime，
// 而 Redis 连不上会直接 panic，于是"Redis 挂了 ⇒ 无法迁移数据库"。
type MigrationRuntime struct {
	cfg    *config.Config
	client *ormx.Client
	// closed 保证日志刷写恰好一次（Close 在 defer 路径上可能被多次调用）。
	closed atomic.Bool
}

// NewMigrationRuntime 装配迁移运行时。
//
// 凭据选择与 server 保持同一套规则：配置了迁移专用账号就用它，
// 未配置则回退运行凭据并打印不含凭据的告警。
func NewMigrationRuntime(ctx context.Context) (*MigrationRuntime, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	applyRuntimeTimezone(cfg)
	if err := initLogWithConfig(cfg); err != nil {
		return nil, fmt.Errorf("init logging: %w", err)
	}

	openCtx, cancel := context.WithTimeout(ctx, defaultMigrationStartupTimeout)
	defer cancel()

	creds := resolveDBCredentials(cfg, Options{UseMigrationCredentials: true})
	client, err := openDBForMigration(openCtx, cfg.Database, creds, data.NewGormLogger(cfg))
	if err != nil {
		// 日志已就绪：失败必须先刷写再返回（resource-lifecycle spec），
		// 否则"迁移为什么起不来"既不落盘也不告警。
		closeProcessRuntime(nil)
		return nil, fmt.Errorf("open database for migration: %w", err)
	}

	logger.Infof("migration runtime initialized with env=%s", cfg.Env)
	return &MigrationRuntime{cfg: cfg, client: client}, nil
}

// Config 返回迁移运行时的配置。
func (r *MigrationRuntime) Config() *config.Config { return r.cfg }

// DB 返回迁移使用的 GORM 句柄。
func (r *MigrationRuntime) DB() *ormx.Client { return r.client }

// Close 释放数据库连接并刷写日志与告警队列。幂等。
//
// **返回错误而不是只记日志**：连接关闭失败是迁移命令的退出事实，只记日志
// 会让一次带故障的收尾表现成正常退出，而命令的退出码正是 CI 与部署脚本
// 唯一看的东西。日志刷写不可省：迁移进程通常在 Close 后立即退出，不刷写
// 则迁移期间的错误日志与告警随进程一起消失——而迁移失败最需要留痕。
func (r *MigrationRuntime) Close() error {
	if r == nil {
		return nil
	}
	var err error
	if r.client != nil {
		if closeErr := r.client.Close(); closeErr != nil {
			err = fmt.Errorf("close migration database connection: %w", closeErr)
			logger.Errorf("%v", err)
		}
	}
	if r.closed.CompareAndSwap(false, true) {
		closeProcessRuntime(nil)
	}
	return err
}

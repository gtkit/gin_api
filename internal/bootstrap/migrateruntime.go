package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	logger "github.com/gtkit/logger/v2"
	"github.com/gtkit/ormx"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/plugin"
	gormlogger "gorm.io/gorm/logger"
)

// openDBForMigration 是注入点，便于测试断言迁移路径只建数据库连接、不碰其他基础设施。
//
// 指向 OpenMigrationDB 而不是 OpenDB：后者会给连接装上"单次网络操作上界"的
// 驱动 I/O deadline（连接寿命与 30s 的较小值），那对 DDL 等价于给单条语句的
// 执行时长封顶。大表建索引撞上它时客户端断连、迁移记为失败，而 MySQL 侧的
// DDL 不随断连回滚、继续执行到完成——留下 schema 已变而版本表未记的静默分叉。
var openDBForMigration = data.OpenMigrationDB

// defaultMigrationStartupTimeout 是迁移建立数据库连接的启动预算。
const defaultMigrationStartupTimeout = 15 * time.Second

// MigrationRuntime 是迁移专用的最小装配：配置 + 日志 + 数据库。
//
// 刻意**不**初始化缓存、令牌签发器等与迁移无关的基础设施。
// 原因很实际：把无关依赖绑进迁移路径，会让其中任何一个不可用时连数据库迁移都跑不了——
// 而迁移恰恰是故障恢复时最需要能执行的操作。此前 migrate 走完整 Runtime，
// 而 Redis 连不上会直接 panic，于是"Redis 挂了 ⇒ 无法迁移数据库"。
type MigrationRuntime struct {
	cfg     *config.Config
	client  *ormx.Client
	plugins *core.PluginRegistry
	// closeOnce 保证插件停止、连接关闭和日志刷写恰好一次（Close 在 defer
	// 路径上可能被多次调用，也可能与命令错误路径并发到达）。
	closeOnce sync.Once
	closeErr  error
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
	registry := core.NewPluginRegistry()
	var client *ormx.Client
	mysqlPlugin := plugin.NewMySQLPlugin(plugin.MySQLOptions{
		Config:      cfg.Database,
		Credentials: creds,
		GormLogger:  data.NewGormLogger(cfg),
		Open: func(
			ctx context.Context,
			cfg config.DatabaseConfig,
			creds config.DBCredentials,
			gormLogger gormlogger.Interface,
		) (*ormx.Client, error) {
			opened, openErr := openDBForMigration(ctx, cfg, creds, gormLogger)
			if openErr == nil {
				client = opened
			}
			return opened, openErr
		},
	})
	if err := registry.Register(mysqlPlugin); err != nil {
		closeProcessRuntime(nil)
		return nil, fmt.Errorf("register migration mysql plugin: %w", err)
	}
	if err := registry.Init(openCtx); err != nil {
		return nil, closeMigrationRegistryOnFailure(registry,
			fmt.Errorf("init migration mysql plugin: %w", err), openCtx)
	}
	if err := registry.Start(openCtx); err != nil {
		return nil, closeMigrationRegistryOnFailure(registry,
			fmt.Errorf("start migration mysql plugin: %w", err), openCtx)
	}
	if client == nil {
		return nil, closeMigrationRegistryOnFailure(registry,
			errors.New("migration mysql plugin initialized without a client"), openCtx)
	}

	logger.Infof("migration runtime initialized with env=%s", cfg.Env)
	return &MigrationRuntime{cfg: cfg, client: client, plugins: registry}, nil
}

func closeMigrationRegistryOnFailure(
	registry *core.PluginRegistry, cause error, ctx context.Context,
) error {
	var stopErr error
	if registry != nil {
		stopErr = registry.Stop(context.WithoutCancel(ctx))
	}
	closeProcessRuntime(nil)
	return errors.Join(cause, stopErr)
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
func (r *MigrationRuntime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.plugins != nil {
			if err := r.plugins.Stop(context.WithoutCancel(ctx)); err != nil {
				r.closeErr = fmt.Errorf("close migration plugin registry: %w", err)
			}
		} else if r.client != nil {
			// 保留手工构造 MigrationRuntime 的兼容路径；正常构造始终走
			// PluginRegistry，避免迁移连接出现第二个关闭责任。
			if err := r.client.Close(); err != nil {
				r.closeErr = fmt.Errorf("close migration database connection: %w", err)
			}
		}
		if r.closeErr != nil {
			logger.Errorf("%v", r.closeErr)
		}
		closeProcessRuntime(nil)
	})
	return r.closeErr
}

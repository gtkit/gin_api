package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/httpcache"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/platform/logging"
	"github.com/gtkit/gin-api/internal/platform/mysql"
	"github.com/gtkit/gin-api/internal/platform/notification/dispatch"
	"github.com/gtkit/gin-api/internal/platform/notification/mail"
	"github.com/gtkit/gin-api/internal/platform/notification/robot"
	"github.com/gtkit/gin-api/internal/platform/notification/sms"
	"github.com/gtkit/gin-api/internal/platform/redis"
	"github.com/gtkit/gin-api/internal/platform/responsecache"
	gormlogger "gorm.io/gorm/logger"
)

const runtimeStartupTimeout = 30 * time.Second
const resourceStartupTimeout = 10 * time.Second

// These consumer interfaces are test seams for resource acquisition and cleanup.
// Production always uses the fixed constructors below.
type databaseOwner interface {
	data.DatabaseHandle
	Close(context.Context) error
}
type redisOwner interface {
	data.RedisProvider
	Close(context.Context) error
}
type cacheOwner interface {
	httpcache.ResponseCache
	Close(context.Context) error
}
type logOwner interface {
	Drain(context.Context) error
	Close(context.Context) error
}

// runtimeFactories is private: commands choose a runtime, never register a graph.
type runtimeFactories struct {
	loadConfig func(context.Context) (*config.Config, error)
	logs       func(context.Context, *config.Config) (logOwner, error)
	database   func(context.Context, *config.Config, bool) (databaseOwner, error)
	redis      func(context.Context, *config.Config) (redisOwner, error)
	cache      func(context.Context, *config.Config, data.RedisClientProvider) (cacheOwner, error)
}

func defaultRuntimeFactories() runtimeFactories {
	return runtimeFactories{
		loadConfig: func(ctx context.Context) (*config.Config, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			cfg, err := config.Load()
			if err != nil {
				return nil, err
			}
			return cfg, ctx.Err()
		},
		logs: openProcessLogs,
		database: func(ctx context.Context, cfg *config.Config, migration bool) (databaseOwner, error) {
			opts := mysql.Options{Config: cfg}
			if migration {
				opts.SelectCredentials = migrationCredentials
				opts.Open = func(ctx context.Context, c config.DatabaseConfig, creds config.DBCredentials, log gormlogger.Interface) (mysql.Client, error) {
					client, err := mysql.OpenMigrationDB(ctx, c, creds, log)
					if client == nil {
						return nil, err
					}
					return client, err
				}
			}
			owner, err := mysql.Open(ctx, opts)
			if owner == nil {
				return nil, err
			}
			return owner, err
		},
		redis: func(ctx context.Context, cfg *config.Config) (redisOwner, error) {
			owner, err := redis.Open(ctx, redis.Options{Config: cfg, Required: RedisRequired})
			if owner == nil {
				return nil, err
			}
			return owner, err
		},
		cache: func(ctx context.Context, cfg *config.Config, r data.RedisClientProvider) (cacheOwner, error) {
			owner, err := responsecache.Open(ctx, responsecache.Options{Config: cfg, Redis: r})
			if owner == nil {
				return nil, err
			}
			return owner, err
		},
	}
}

// runtimeResources owns a fixed set in acquisition order: logs, DB, Redis, cache.
// No dependency discovery or lifecycle protocol runs on these resources.
type runtimeResources struct {
	logs     logOwner
	database databaseOwner
	redis    redisOwner
	cache    cacheOwner
	force    <-chan struct{}
	closeMu  sync.Mutex
	closed   chan struct{}
	closeErr error
}

func openRuntimeResources(ctx context.Context, cfg *config.Config, f runtimeFactories, migration bool, budget *rollbackBudget) (_ *runtimeResources, err error) {
	r := &runtimeResources{force: budget.force}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close(budget.Context(ctx)))
		}
	}()
	r.logs, err = openResource(ctx, "logging", func(ctx context.Context) (logOwner, error) { return f.logs(ctx, cfg) })
	if err != nil {
		return nil, err
	}
	r.database, err = openResource(ctx, "database", func(ctx context.Context) (databaseOwner, error) { return f.database(ctx, cfg, migration) })
	if err != nil {
		return nil, err
	}
	if migration {
		return r, nil
	}
	r.redis, err = openResource(ctx, "redis", func(ctx context.Context) (redisOwner, error) { return f.redis(ctx, cfg) })
	if err != nil {
		return nil, err
	}
	r.cache, err = openResource(ctx, "cache", func(ctx context.Context) (cacheOwner, error) { return f.cache(ctx, cfg, r.redis) })
	if err != nil {
		return nil, err
	}
	return r, nil
}

func openResource[T any](ctx context.Context, name string, open func(context.Context) (T, error)) (value T, err error) {
	ctx, cancel := context.WithTimeout(ctx, resourceStartupTimeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("open %s: %w: panic: %v", name, core.ErrComponentState, recovered)
		}
		err = errors.Join(err, ctx.Err())
		if err == nil && nilResource(value) {
			err = fmt.Errorf("open %s: %w: nil resource", name, core.ErrComponentState)
		}
		if err != nil {
			err = fmt.Errorf("open %s: %w", name, err)
		}
	}()
	if err = ctx.Err(); err != nil {
		return value, err
	}
	return open(ctx)
}

func nilResource(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func closeResource(ctx context.Context, name string, closeFn func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: panic: %v", core.ErrComponentState, recovered)
		}
		err = errors.Join(err, ctx.Err())
		if err != nil {
			err = fmt.Errorf("close %s: %w", name, err)
		}
	}()
	return closeFn(ctx)
}

func (r *runtimeResources) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.closeMu.Lock()
	if done := r.closed; done != nil {
		r.closeMu.Unlock()
		select {
		case <-done:
			return r.closeErr
		default:
		}
		select {
		case <-done:
			return r.closeErr
		case <-ctx.Done():
			return ctx.Err()
		case <-r.force:
			return context.Canceled
		}
	}
	r.closed = make(chan struct{})
	done := r.closed
	r.closeMu.Unlock()
	ctx, finish := cleanupContext(ctx, r.force)
	defer finish()
	var errs []error
	if !nilResource(r.cache) {
		errs = append(errs, closeResource(ctx, "cache", r.cache.Close))
	}
	if !nilResource(r.redis) {
		errs = append(errs, closeResource(ctx, "redis", r.redis.Close))
	}
	if !nilResource(r.database) {
		errs = append(errs, closeResource(ctx, "database", r.database.Close))
	}
	if !nilResource(r.logs) {
		errs = append(errs, closeResource(ctx, "logging", r.logs.Close))
	}
	// Observe a force signal synchronously before publishing the shared result;
	// its context observer may still be waiting for a scheduler turn.
	select {
	case <-r.force:
		errs = append(errs, context.Canceled)
	default:
	}
	err := errors.Join(errs...)
	r.closeMu.Lock()
	r.closeErr = err
	close(done)
	r.closeMu.Unlock()
	return err
}

// processLogs keeps notification owners alive until the logger finishes draining.
type processLogs struct {
	logger       *logging.Owner
	notification *dispatch.Owner
	robot        *robot.Owner
	mail         *mail.Owner
	sms          *sms.Owner
}

func openProcessLogs(ctx context.Context, cfg *config.Config) (_ logOwner, err error) {
	p := &processLogs{}

	p.robot, err = robot.Open(ctx, robot.Options{Config: cfg})
	if err != nil {
		return p, err
	}
	p.mail, err = mail.Open(ctx, mail.Options{Config: cfg})
	if err != nil {
		return p, err
	}
	p.sms, err = sms.Open(ctx, sms.Options{Config: cfg})
	if err != nil {
		return p, err
	}
	p.notification, err = dispatch.Open(ctx, dispatch.Options{Config: cfg, Robot: p.robot, Mail: p.mail, SMS: p.sms})
	if err != nil {
		return p, err
	}
	p.logger, err = logging.Open(ctx, logging.Options{Config: cfg, Notification: p.notification})
	if err != nil {
		return p, err
	}
	return p, nil
}

func (p *processLogs) Drain(ctx context.Context) error {
	if p.logger == nil {
		return nil
	}
	return p.logger.Drain(ctx)
}

func (p *processLogs) Close(ctx context.Context) error {
	var errs []error
	if p.logger != nil {
		errs = append(errs, closeResource(ctx, "logger", p.logger.Close))
	}
	if p.notification != nil {
		errs = append(errs, closeResource(ctx, "notification", p.notification.Close))
	}
	if p.sms != nil {
		errs = append(errs, closeResource(ctx, "sms", p.sms.Close))
	}
	if p.mail != nil {
		errs = append(errs, closeResource(ctx, "mail", p.mail.Close))
	}
	if p.robot != nil {
		errs = append(errs, closeResource(ctx, "robot", p.robot.Close))
	}
	return errors.Join(errs...)
}

func migrationCredentials(cfg config.DatabaseConfig) config.DBCredentials {
	if creds, ok := cfg.MigrationCredentials(); ok {
		return creds
	}
	applog.Warn(migrationFallbackWarning)
	return cfg.RuntimeCredentials()
}

const migrationFallbackWarning = "migration is using the runtime database credentials: " +
	"set database.migrate_username to grant DDL only to a dedicated account"

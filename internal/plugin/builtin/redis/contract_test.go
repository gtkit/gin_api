package redis

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	goredis "github.com/redis/go-redis/v9"
)

type valueConfiguration struct {
	cfg   *config.Config
	err   error
	reads int
}

func (c *valueConfiguration) Value() (*config.Config, error) { c.reads++; return c.cfg, c.err }

type ownedRedis struct {
	clients  map[int]*goredis.Client
	closes   atomic.Int32
	closeErr error
}

func (c *ownedRedis) GetClient(db int) (*goredis.Client, bool) {
	value, ok := c.clients[db]
	return value, ok
}
func (c *ownedRedis) DefaultClient() *goredis.Client        { return c.clients[0] }
func (c *ownedRedis) HealthCheck(ctx context.Context) error { return ctx.Err() }
func (c *ownedRedis) PoolStats() map[int]*goredis.PoolStats {
	stats := make(map[int]*goredis.PoolStats, len(c.clients))
	for db, client := range c.clients {
		stats[db] = client.PoolStats()
	}
	return stats
}
func (c *ownedRedis) Close() error {
	c.closes.Add(1)
	var errs []error
	for _, client := range c.clients {
		errs = append(errs, client.Close())
	}
	return errors.Join(append(errs, c.closeErr)...)
}
func TestResourceAndCancellationContracts(t *testing.T) {
	root := errors.New("unreachable")
	closeFailure := errors.New("close failed")
	for _, tc := range []struct {
		name                                                  string
		required, partial, lateCancel, typedNil, closeFailure bool
		openErr                                               error
		wantErr                                               bool
	}{
		{name: "success"},
		{name: "optional unreachable", openErr: root},
		{name: "required unreachable", required: true, openErr: root, wantErr: true},
		{name: "optional partial", partial: true, openErr: root},
		{name: "required partial", partial: true, required: true, openErr: root, wantErr: true},
		{name: "partial close fails", partial: true, openErr: root, closeFailure: true, wantErr: true},
		{name: "canceled error", openErr: context.Canceled, wantErr: true},
		{name: "deadline error", openErr: context.DeadlineExceeded, wantErr: true},
		{name: "late cancel", partial: true, lateCancel: true, wantErr: true},
		{name: "typed nil", typedNil: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cfg := &valueConfiguration{cfg: &config.Config{Redis: config.RedisConfig{Addr: "unused", DBs: []int{0, 3}}}}
			owned := &ownedRedis{clients: map[int]*goredis.Client{0: goredis.NewClient(&goredis.Options{Addr: "unused", DB: 0}), 3: goredis.NewClient(&goredis.Options{Addr: "unused", DB: 3})}}
			if tc.closeFailure {
				owned.closeErr = closeFailure
			}
			t.Cleanup(func() {
				if owned.closes.Load() == 0 {
					_ = owned.Close()
				}
			})
			opens := 0
			p := New(Options{Configuration: cfg, Required: func(*config.Config) bool { return tc.required }, Open: func(callCtx context.Context, c config.RedisConfig) (Client, error) {
				opens++
				if callCtx != ctx || len(c.DBs) != 2 || c.DBs[1] != 3 {
					t.Fatal("factory configuration changed")
				}
				if tc.lateCancel {
					cancel()
				}
				if tc.typedNil {
					var absent *ownedRedis
					return absent, nil
				}
				if tc.partial || tc.openErr == nil {
					return owned, tc.openErr
				}
				return nil, tc.openErr
			}})
			if cfg.reads != 0 || opens != 0 {
				t.Fatal("constructor performed work")
			}
			err := p.Init(ctx)
			if (err != nil) != tc.wantErr {
				t.Fatalf("init=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr && tc.openErr != nil && !errors.Is(err, tc.openErr) {
				t.Fatalf("lost root: %v", err)
			}
			if tc.lateCancel && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if tc.typedNil && !errors.Is(err, core.ErrPluginContractViolation) {
				t.Fatal(err)
			}
			if tc.closeFailure && !errors.Is(err, closeFailure) {
				t.Fatal(err)
			}
			if !p.Configured() {
				t.Fatal("configured state lost")
			}
			again := p.Init(t.Context())
			if tc.wantErr && again == nil {
				t.Fatal("failure was not cached")
			}
			if cfg.reads != 1 || opens != 1 {
				t.Fatal("reopened on Init")
			}
			if !tc.wantErr {
				if err := p.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if err == nil && tc.openErr == nil {
				if p.Client(3) != owned.clients[3] || p.DefaultClient() != owned.clients[0] || len(p.PoolStats()) != 2 {
					t.Fatal("logical DB views changed")
				}
				if err := p.HealthCheck(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if p.DefaultClient() != nil {
				t.Fatal("failed connection published")
			}
			var group sync.WaitGroup
			for range 16 {
				group.Go(func() { _ = p.Stop(context.Background()) })
			}
			group.Wait()
			used := tc.partial || tc.openErr == nil && !tc.typedNil
			if used && owned.closes.Load() != 1 {
				t.Fatalf("close count=%d", owned.closes.Load())
			}
			if p.Client(0) != nil || p.DefaultClient() != nil || p.PoolStats() != nil {
				t.Fatal("closed handle published")
			}
		})
	}
}
func TestConfigurationAndUnconfiguredRequired(t *testing.T) {
	root := errors.New("configuration failed")
	for _, tc := range []struct {
		name     string
		provider *valueConfiguration
		required bool
		wantErr  bool
	}{
		{"missing", nil, false, true}, {"failed", &valueConfiguration{err: root}, false, true}, {"nil config", &valueConfiguration{}, false, true}, {"unconfigured optional", &valueConfiguration{cfg: &config.Config{}}, false, false}, {"unconfigured required", &valueConfiguration{cfg: &config.Config{}}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{Required: func(*config.Config) bool { return tc.required }, Open: func(context.Context, config.RedisConfig) (Client, error) {
				t.Fatal("opener must not be called")
				return nil, nil
			}}
			if tc.provider != nil {
				opts.Configuration = tc.provider
			}
			p := New(opts)
			err := p.Init(t.Context())
			if (err != nil) != tc.wantErr {
				t.Fatalf("init=%v", err)
			}
			_ = p.Stop(t.Context())
			if err := p.Start(t.Context()); err == nil {
				t.Fatal("started after stop")
			}
		})
	}
	p := New(Options{})
	_ = p.Stop(t.Context())
	if !errors.Is(p.Init(t.Context()), core.ErrPluginContractViolation) {
		t.Fatal("init after stop")
	}
}

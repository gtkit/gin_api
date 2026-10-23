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
	root, closeFailure := errors.New("unreachable"), errors.New("close failed")
	for _, tc := range []struct {
		name                                                          string
		required, partial, lateCancel, typedNil, closeFailure, panics bool
		openErr                                                       error
		wantErr                                                       bool
	}{
		{name: "success"}, {name: "normal close error", closeFailure: true}, {name: "optional unreachable", openErr: root}, {name: "required unreachable", required: true, openErr: root, wantErr: true},
		{name: "optional partial", partial: true, openErr: root}, {name: "required partial", partial: true, required: true, openErr: root, wantErr: true},
		{name: "partial close fails", partial: true, openErr: root, closeFailure: true, wantErr: true}, {name: "canceled error", openErr: context.Canceled, wantErr: true},
		{name: "deadline error", openErr: context.DeadlineExceeded, wantErr: true}, {name: "late cancel", partial: true, lateCancel: true, wantErr: true}, {name: "typed nil", typedNil: true, wantErr: true}, {name: "panic", panics: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cfg := &config.Config{Redis: config.RedisConfig{Addr: "unused", DBs: []int{0, 3}}}
			owned := &ownedRedis{clients: map[int]*goredis.Client{0: goredis.NewClient(&goredis.Options{Addr: "unused", DB: 0}), 3: goredis.NewClient(&goredis.Options{Addr: "unused", DB: 3})}}
			if tc.closeFailure {
				owned.closeErr = closeFailure
			}
			t.Cleanup(func() {
				if owned.closes.Load() == 0 {
					if err := owned.Close(); err != nil {
						t.Error(err)
					}
				}
			})
			opens := 0
			p, err := Open(ctx, Options{Config: cfg, Required: func(*config.Config) bool { return tc.required }, Open: func(callCtx context.Context, c config.RedisConfig) (Client, error) {
				opens++
				if callCtx != ctx || len(c.DBs) != 2 || c.DBs[1] != 3 {
					t.Fatal("factory arguments drifted")
				}
				if tc.panics {
					panic("factory")
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
			if (err != nil) != tc.wantErr {
				t.Fatalf("open=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr && p != nil {
				t.Fatal("failed owner published")
			}
			if tc.wantErr && tc.openErr != nil && !errors.Is(err, tc.openErr) {
				t.Fatalf("lost cause: %v", err)
			}
			if tc.lateCancel && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if (tc.typedNil || tc.panics) && !errors.Is(err, core.ErrComponentState) {
				t.Fatal(err)
			}
			if tc.partial && tc.closeFailure && !errors.Is(err, closeFailure) {
				t.Fatal(err)
			}
			if p != nil {
				if !p.Configured() {
					t.Fatal("configured state lost")
				}
				if tc.openErr == nil {
					if p.Client(3) != owned.clients[3] || p.DefaultClient() != owned.clients[0] || len(p.PoolStats()) != 2 {
						t.Fatal("logical DB views changed")
					}
					if err := p.HealthCheck(t.Context()); err != nil {
						t.Fatal(err)
					}
				} else if p.DefaultClient() != nil {
					t.Fatal("degraded client published")
				}
				var group sync.WaitGroup
				for range 16 {
					group.Go(func() {
						closeResult := p.Close(context.WithoutCancel(t.Context()))
						if tc.closeFailure {
							if !errors.Is(closeResult, closeFailure) {
								t.Errorf("lost close error: %v", closeResult)
							}
						} else if closeResult != nil {
							t.Error(closeResult)
						}
					})
				}
				group.Wait()
				if p.Client(0) != nil || p.DefaultClient() != nil || p.PoolStats() != nil {
					t.Fatal("closed handle published")
				}
			}
			if opens != 1 {
				t.Fatalf("opens=%d", opens)
			}
			if (tc.partial || tc.openErr == nil && !tc.typedNil && !tc.panics) && owned.closes.Load() != 1 {
				t.Fatalf("closes=%d", owned.closes.Load())
			}
		})
	}
}
func TestConfigurationAndUnconfiguredRequired(t *testing.T) {
	for _, tc := range []struct {
		name              string
		cfg               *config.Config
		required, wantErr bool
	}{
		{name: "missing", wantErr: true}, {name: "unconfigured optional", cfg: &config.Config{}}, {name: "unconfigured required", cfg: &config.Config{}, required: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Open(t.Context(), Options{Config: tc.cfg, Required: func(*config.Config) bool { return tc.required }, Open: func(context.Context, config.RedisConfig) (Client, error) {
				t.Fatal("unexpected opener")
				return nil, nil
			}})
			if (err != nil) != tc.wantErr {
				t.Fatal(err)
			}
			if p != nil {
				if p.Configured() || p.DefaultClient() != nil {
					t.Fatal("unconfigured state drifted")
				}
				if err := p.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

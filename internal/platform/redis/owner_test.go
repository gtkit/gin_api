package redis

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	goredis "github.com/redis/go-redis/v9"
)

func TestOpenCancellationPrecedesFactory(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p, err := Open(ctx, Options{Config: &config.Config{}})
	if p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("owner=%v err=%v", p, err)
	}
}

func newRecoveryClient() *ownedRedis {
	return &ownedRedis{clients: map[int]*goredis.Client{
		0: goredis.NewClient(&goredis.Options{Addr: "unused", DB: 0}),
		3: goredis.NewClient(&goredis.Options{Addr: "unused", DB: 3}),
	}}
}

func TestOptionalRecoveryWaitsForTracingAndPublishesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		client := newRecoveryClient()
		p, err := Open(t.Context(), Options{
			Config: &config.Config{Redis: config.RedisConfig{Addr: "unused", DBs: []int{0, 3}}},
			Open: func(ctx context.Context, _ config.RedisConfig) (Client, error) {
				n := calls.Add(1)
				if n == 1 {
					return nil, errors.New("initial failure")
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) != recoveryTimeout {
					t.Error("recovery attempt has no shared ten-second budget")
				}
				if n == 2 {
					time.Sleep(time.Second)
					return nil, context.DeadlineExceeded
				}
				return client, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Second)
		if calls.Load() != 1 {
			t.Fatal("recovery started before tracing")
		}
		hooks := map[*goredis.Client]int{}
		p.instrument = func(c *goredis.Client) error { hooks[c]++; return nil }
		for range 2 {
			if err := p.ConfigureTracing(); err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		time.Sleep(6 * time.Second)
		synctest.Wait()
		if calls.Load() != 2 || p.DefaultClient() != nil {
			t.Fatal("failed attempt published or retried without its delay")
		}
		time.Sleep(recoveryInterval)
		synctest.Wait()
		if calls.Load() != 3 || p.Client(3) != client.clients[3] || len(p.PoolStats()) != 2 {
			t.Fatal("complete client was not published")
		}
		for _, c := range client.clients {
			if hooks[c] != 1 {
				t.Fatalf("tracing hooks=%d, want one per client", hooks[c])
			}
		}
		time.Sleep(20 * time.Second)
		if calls.Load() != 3 {
			t.Fatal("recovery continued after success")
		}
		if err := p.Close(t.Context()); err != nil || client.closes.Load() != 1 {
			t.Fatalf("close=%v calls=%d", err, client.closes.Load())
		}
		if p.ConfigureTracing() == nil || p.DefaultClient() != nil {
			t.Fatal("closed owner accepted tracing or published a client")
		}
	})
}

func TestRecoveryCloseWaitsForLateCandidateAndKeepsFinalError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		client := newRecoveryClient()
		failure := errors.New("late candidate close failure")
		client.closeErr = failure
		var calls atomic.Int32
		p, err := Open(t.Context(), Options{
			Config: &config.Config{Redis: config.RedisConfig{Addr: "unused"}},
			Open: func(context.Context, config.RedisConfig) (Client, error) {
				if calls.Add(1) == 1 {
					return nil, errors.New("offline")
				}
				close(entered)
				<-release // Deliberately observe cancellation only after returning.
				return client, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.ConfigureTracing(); err != nil {
			t.Fatal(err)
		}
		<-entered
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := p.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("first close=%v", err)
		}
		if p.DefaultClient() != nil {
			t.Fatal("close did not seal publication")
		}
		close(release)
		for range 2 {
			if err := p.Close(t.Context()); !errors.Is(err, failure) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("final close=%v", err)
			}
		}
		if client.closes.Load() != 1 || p.DefaultClient() != nil {
			t.Fatal("late candidate was published or closed more than once")
		}
	})
}

func TestRecoveryContractFailuresStopRetrying(t *testing.T) {
	for _, mode := range []string{"panic", "typed-nil", "missing-default", "tracing-error", "tracing-panic", "partial-close-error"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := newRecoveryClient()
				failure := errors.New("candidate failure")
				if mode == "missing-default" {
					if err := client.clients[0].Close(); err != nil {
						t.Fatal(err)
					}
					delete(client.clients, 0)
				}
				var calls atomic.Int32
				p, err := Open(t.Context(), Options{
					Config: &config.Config{Redis: config.RedisConfig{Addr: "unused"}},
					Open: func(context.Context, config.RedisConfig) (Client, error) {
						if calls.Add(1) == 1 {
							return nil, errors.New("offline")
						}
						switch mode {
						case "panic":
							panic("opener")
						case "typed-nil":
							var absent *ownedRedis
							return absent, nil
						case "partial-close-error":
							client.closeErr = failure
							return client, errors.New("partial")
						default:
							return client, nil
						}
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				p.instrument = func(*goredis.Client) error {
					if mode == "tracing-panic" {
						panic("tracing")
					}
					if mode == "tracing-error" {
						return failure
					}
					return nil
				}
				if err := p.ConfigureTracing(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				time.Sleep(recoveryInterval)
				synctest.Wait()
				time.Sleep(20 * time.Second)
				if calls.Load() != 2 || p.DefaultClient() != nil {
					t.Fatal("terminal candidate retried or published")
				}
				want := error(core.ErrComponentState)
				if mode == "tracing-error" || mode == "partial-close-error" {
					want = failure
				}
				if err := p.Close(t.Context()); !errors.Is(err, want) {
					t.Fatalf("terminal cause=%v", err)
				}
				if mode == "panic" || mode == "typed-nil" {
					if err := client.Close(); err != nil {
						t.Fatal(err)
					}
				} else if client.closes.Load() != 1 {
					t.Fatalf("closes=%d", client.closes.Load())
				}
			})
		})
	}
}
func TestUnavailableAccessorsAreSafe(t *testing.T) {
	p := new(Owner)
	if p.Client(0) != nil || p.DefaultClient() != nil || p.PoolStats() != nil || p.Configured() || p.HealthCheck(t.Context()) == nil {
		t.Fatal("unavailable owner published resources")
	}
	if client, ok := p.GetClient(0); client != nil || ok {
		t.Fatal("missing DB available")
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

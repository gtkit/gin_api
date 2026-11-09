package redis

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
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
				want := core.ErrComponentState
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

func TestRecoveryOutcomeLogs(t *testing.T) {
	for _, mode := range []string{"recovered", "terminal", "canceled", "canceled-cleanup-failure"} {
		t.Run(mode, func(t *testing.T) {
			// Capture the real console encoder outside the virtual-time bubble.
			// File rotation owns global goroutines and must not cross bubbles.
			path := filepath.Join(t.TempDir(), "recovery.jsonl")
			output, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := output.Close(); err != nil {
					t.Error(err)
				}
			})
			instance, err := func() (*logger.Logger, error) {
				previous := os.Stdout
				os.Stdout = output
				defer func() { os.Stdout = previous }()
				return logger.New(logger.WithConsole(true), logger.WithFile(false), logger.WithOutJSON(true), logger.WithLevel("info"), logger.WithReplaceGlobals(false))
			}()
			if err != nil {
				t.Fatal(err)
			}
			previous := logger.Default()
			logger.SetDefault(instance)
			t.Cleanup(func() { logger.SetDefault(previous); instance.Sync() })

			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				client := &ownedRedis{clients: make(map[int]*goredis.Client)}
				for _, db := range []int{0, 3} {
					client.clients[db] = goredis.NewClient(&goredis.Options{
						Addr: "127.0.0.1:0", DB: db,
						Dialer: func(context.Context, string, string) (net.Conn, error) {
							return nil, errors.New("network disabled in recovery log test")
						},
					})
				}
				failure := errors.New("candidate cleanup failed")
				entered, release := make(chan struct{}), make(chan struct{})
				p, err := Open(t.Context(), Options{
					Config: &config.Config{Redis: config.RedisConfig{Addr: "unused"}},
					Open: func(context.Context, config.RedisConfig) (Client, error) {
						if calls.Add(1) <= 2 {
							return nil, errors.New("temporarily offline")
						}
						if mode == "terminal" {
							return nil, nil
						}
						if mode == "canceled-cleanup-failure" {
							client.closeErr = failure
							close(entered)
							<-release
						}
						return client, nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				p.instrument = func(*goredis.Client) error { return nil }
				if err := p.ConfigureTracing(); err != nil {
					t.Fatal(err)
				}
				synctest.Sleep(recoveryInterval)
				if calls.Load() != 2 || p.DefaultClient() != nil {
					t.Fatal("transient attempt changed publication or retry timing")
				}
				if mode == "canceled-cleanup-failure" {
					<-entered
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					if err := p.Close(ctx); !errors.Is(err, context.Canceled) {
						t.Fatalf("pending close=%v", err)
					}
					close(release)
				} else if mode != "canceled" {
					synctest.Sleep(recoveryInterval)
				}
				closeErr := p.Close(t.Context())
				switch mode {
				case "terminal":
					if !errors.Is(closeErr, core.ErrComponentState) {
						t.Fatalf("terminal cause=%v", closeErr)
					}
				case "canceled-cleanup-failure":
					if !errors.Is(closeErr, failure) {
						t.Fatalf("cleanup cause=%v", closeErr)
					}
				default:
					if closeErr != nil {
						t.Fatal(closeErr)
					}
				}
				if mode == "canceled" || mode == "terminal" {
					if err := client.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
			instance.Sync()
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			type logRecord struct {
				Message string `json:"msg"`
				Level   string `json:"level"`
				DBs     []int  `json:"dbs"`
			}
			var records []logRecord
			for line := range strings.Lines(string(contents)) {
				var record logRecord
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatal(err)
				}
				records = append(records, record)
			}
			wantRecords := 2
			if mode == "recovered" {
				wantRecords = 3
			}
			if len(records) != wantRecords || records[0].Level != "WARN" {
				t.Fatalf("want startup warning, one outcome and closure only after recovery, got %s", contents)
			}
			if mode == "recovered" && (records[2].Level != "INFO" || records[2].Message != "\x1b[0;31mredis connection closed\x1b[0m \x1b[0;96mdbs=[0 3]\x1b[0m" || !slices.Equal(records[2].DBs, []int{0, 3})) {
				t.Fatalf("unexpected closure log: %s", contents)
			}
			wantLevel, wantMessage := "INFO", "redis recovery succeeded"
			switch mode {
			case "terminal":
				wantLevel, wantMessage = "ERROR", "redis recovery stopped after terminal failure:"
			case "canceled":
				wantMessage = "redis recovery stopped: context canceled"
			case "canceled-cleanup-failure":
				wantLevel, wantMessage = "ERROR", "candidate cleanup failed"
			}
			if records[1].Level != wantLevel || !strings.Contains(records[1].Message, wantMessage) {
				t.Fatalf("unexpected outcome: %s", contents)
			}
		})
	}
}

// clearingLogRedis checks that closure logs use the pre-close database snapshot.
type clearingLogRedis struct{ *ownedRedis }

func (c *clearingLogRedis) Close() error {
	err := c.ownedRedis.Close()
	clear(c.clients)
	return err
}

func TestConnectionLogsUseActualDatabases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		actual  []int
		want    []int
		display string
	}{
		{name: "single", actual: []int{0}, want: []int{0}, display: "dbs=[0]"},
		{name: "multiple sorted", actual: []int{5, 0, 3}, want: []int{0, 3, 5}, display: "dbs=[0 3 5]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "connections.jsonl")
			output, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := output.Close(); err != nil {
					t.Error(err)
				}
			})
			instance, err := func() (*logger.Logger, error) {
				previous := os.Stdout
				os.Stdout = output
				defer func() { os.Stdout = previous }()
				return logger.New(logger.WithConsole(true), logger.WithFile(false), logger.WithOutJSON(true), logger.WithLevel("info"), logger.WithReplaceGlobals(false))
			}()
			if err != nil {
				t.Fatal(err)
			}
			previous := logger.Default()
			logger.SetDefault(instance)
			t.Cleanup(func() { logger.SetDefault(previous); instance.Sync() })

			client := &clearingLogRedis{ownedRedis: &ownedRedis{clients: make(map[int]*goredis.Client)}}
			for _, db := range tc.actual {
				client.clients[db] = goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:0", DB: db})
			}
			p, err := Open(t.Context(), Options{
				Config: &config.Config{Redis: config.RedisConfig{Addr: "127.0.0.1:0", DBs: []int{9}}},
				Open: func(context.Context, config.RedisConfig) (Client, error) {
					return client, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
			})
			for range 2 {
				if err := p.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			instance.Sync()
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var messages []string
			for line := range strings.Lines(string(content)) {
				var record struct {
					Message string `json:"msg"`
					Level   string `json:"level"`
					DBs     []int  `json:"dbs"`
				}
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatal(err)
				}
				if record.Level != "INFO" || !slices.Equal(record.DBs, tc.want) {
					t.Fatalf("unexpected connection log: %s", line)
				}
				messages = append(messages, record.Message)
			}
			wantMessages := []string{
				"\x1b[0;32mredis connection established\x1b[0m \x1b[0;96m" + tc.display + "\x1b[0m",
				"\x1b[0;31mredis connection closed\x1b[0m \x1b[0;96m" + tc.display + "\x1b[0m",
			}
			if !slices.Equal(messages, wantMessages) || client.closes.Load() != 1 || len(client.clients) != 0 {
				t.Fatalf("messages=%q closes=%d remaining clients=%d", messages, client.closes.Load(), len(client.clients))
			}
		})
	}
}

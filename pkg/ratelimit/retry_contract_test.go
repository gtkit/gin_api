package ratelimit

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
)

func TestDeductionDoesNotReplayAfterServerLosesResponse(t *testing.T) {
	requireLoopback(t)
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr(), MaxRetries: 3, MinRetryBackoff: time.Millisecond, MaxRetryBackoff: time.Millisecond})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	backend, err := NewRedisBackend(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	var deductions, reads atomic.Int32
	srv.Server().SetPreHook(func(peer *server.Peer, command string, _ ...string) bool {
		switch strings.ToLower(command) {
		case "evalsha":
			// A controlled protocol peer records the side effect, then loses the reply.
			deductions.Add(1)
			peer.Close()
			return true
		case "get":
			reads.Add(1)
			peer.Close()
			return true
		}
		return false
	})
	if _, err := backend.check(t.Context(), redisTestPolicy(), "uncertain-deduction"); err == nil {
		t.Fatal("lost deduction response was reported as success")
	}
	if deductions.Load() != 1 {
		t.Fatalf("uncertain deduction executed %d times, want exactly 1", deductions.Load())
	}
	if err := client.Get(t.Context(), "retryable-read").Err(); err == nil || reads.Load() != 4 {
		t.Fatalf("unrelated read retry policy changed: attempts=%d err=%v", reads.Load(), err)
	}
}

func TestNoScriptReloadIsTheOnlyDeductionReplay(t *testing.T) {
	backend, srv := newLuaBackend(t)
	var attempts, executions, loads atomic.Int32
	srv.Server().SetPreHook(func(peer *server.Peer, command string, args ...string) bool {
		if strings.EqualFold(command, "script") && len(args) > 0 && strings.EqualFold(args[0], "load") {
			loads.Add(1)
		}
		if strings.EqualFold(command, "evalsha") {
			if attempts.Add(1) == 1 {
				peer.WriteError("NOSCRIPT script cache deliberately missing")
				return true
			}
			executions.Add(1)
		}
		return false
	})
	policy := redisTestPolicy()
	policy.Burst = 1
	decision, err := backend.check(t.Context(), policy, "noscript-deduction")
	if err != nil || !decision.Allowed() || attempts.Load() != 2 || executions.Load() != 1 || loads.Load() != 1 {
		t.Fatalf("decision=%+v err=%v attempts=%d executions=%d reloads=%d", decision, err, attempts.Load(), executions.Load(), loads.Load())
	}
	decision, err = backend.check(context.WithoutCancel(t.Context()), policy, "noscript-deduction")
	if err != nil || decision.Outcome != OutcomeRateLimited {
		t.Fatalf("deduction did not consume exactly one token: decision=%+v err=%v", decision, err)
	}
}

func TestRedisPolicyRefillTTLBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		rate  float64
		burst int
		store Store
		valid bool
		ttl   time.Duration
	}{
		{"exact day cap", 1, 86340, StoreRedis, true, 24 * time.Hour},
		{"one second beyond", 1, 86341, StoreRedis, false, 0},
		{"ceiling boundary", 2, 172680, StoreRedis, true, 24 * time.Hour},
		{"ceiling exceeds cap", 2, 172681, StoreRedis, false, 0},
		{"subsecond refill rounds upward", 2, 1, StoreRedis, true, 61 * time.Second},
		{"fixed point rounded slow rate accepted", 1.0 / 60, 1439, StoreRedis, true, 86399 * time.Second},
		{"fixed point rounded slow rate rejected", 1.0 / 60, 1440, StoreRedis, false, 0},
		{"minimum rate cannot refill in a day", 0.000001, 1, StoreRedis, false, 0},
		{"local store has no Redis residence cap", 0.000001, 1, StoreLocal, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy := redisTestPolicy()
			policy.Rate, policy.Burst, policy.Store = tc.rate, tc.burst, tc.store
			if err := policy.Validate(); (err == nil) != tc.valid {
				t.Fatalf("policy valid=%t err=%v", tc.valid, err)
			}
			if tc.store == StoreRedis {
				backend := &RedisBackend{}
				if _, err := NewRedisLimiter(backend, policy); (err == nil) != tc.valid {
					t.Fatalf("RedisLimiter bypassed refill validation: %v", err)
				}
				fallback, err := NewFallbackBackend(backend, policy)
				if (err == nil) != tc.valid {
					t.Fatalf("FallbackBackend bypassed refill validation: %v", err)
				}
				if fallback != nil {
					t.Cleanup(fallback.Close)
				}
				if tc.valid && bucketTTL(policy) != tc.ttl {
					t.Fatalf("bucket TTL=%s want=%s", bucketTTL(policy), tc.ttl)
				}
			}
		})
	}
}

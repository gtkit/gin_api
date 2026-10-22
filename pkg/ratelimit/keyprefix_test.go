package ratelimit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
)

// keyCapturingClient 记录送达后端的键。
type keyCapturingClient struct {
	mu   sync.Mutex
	keys []string
}

func (c *keyCapturingClient) ScriptLoad(ctx context.Context, _ string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	cmd.SetVal("sha")
	return cmd
}

func (c *keyCapturingClient) EvalSha(
	ctx context.Context, _ string, keys []string, _ ...any,
) *redis.Cmd {
	c.mu.Lock()
	c.keys = append(c.keys, keys...)
	c.mu.Unlock()

	cmd := redis.NewCmd(ctx)
	cmd.SetErr(errors.New("probe stops here"))
	return cmd
}

func (c *keyCapturingClient) Del(ctx context.Context, _ ...string) *redis.IntCmd {
	return redis.NewIntCmd(ctx)
}

func (c *keyCapturingClient) captured() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.keys...)
}

// TestKeyPrefixReachesTheBackend 钉住前缀真正参与真实判定的键。
//
// 此前它只用在启动探针上，真实请求把原始键直接透传——于是 WithKeyPrefix 对
// 生产流量完全无效，而选项文档明确承诺隔离。多个服务共用同一个 Redis 时，
// 同名策略共享同一个桶，各自的限额都不是配置里写的那个值。
//
// 这条测试断言的是**送达后端的键**，而不是某个内部字段：字段存对了但没用上，
// 正是上一版的失效方式。
func TestKeyPrefixReachesTheBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prefix  string
		rawKey  string
		wantKey string
	}{
		{
			name: "a configured prefix is applied", prefix: "svc-a",
			rawKey: "rl:policy:ip:abc", wantKey: "svc-a:rl:policy:ip:abc",
		},
		{
			// 未配置时保持原样：多数部署只有一个服务用这个 Redis，
			// 强制加前缀只会让键变长而不带来隔离。
			name: "no prefix leaves the key untouched", prefix: "",
			rawKey: "rl:policy:ip:abc", wantKey: "rl:policy:ip:abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &keyCapturingClient{}
			backend := &RedisBackend{
				client:    client,
				keyPrefix: tt.prefix,
				health:    NewHealthState(DefaultHealthPolicy(), nil),
				observer:  nopObserver{},
			}
			sha := "sha"
			backend.sha.Store(&sha)

			policy := Policy{
				ID: "prefix.probe", Rate: 10, Burst: 5,
				Scope: ScopeIP, Store: StoreRedis,
				FailureMode: FailureClosed, MaxKeys: 100,
			}
			if _, err := backend.check(context.Background(), policy, tt.rawKey); err == nil || !strings.Contains(err.Error(), "probe stops here") {
				t.Fatalf("capture fixture error: %v", err)
			}

			keys := client.captured()
			if len(keys) == 0 {
				t.Fatal("no key reached the backend")
			}
			if keys[0] != tt.wantKey {
				t.Fatalf("backend received %q, want %q", keys[0], tt.wantKey)
			}
		})
	}
}

// TestBucketTTLRespectsItsOwnCap 钉住上限对边界策略也成立。
//
// 此前先夹上限再加余量，于是结果比上限多出一个余量——一个自己就违反声称值的
// 上限，无法作为容量估算的依据。
func TestBucketTTLRespectsItsOwnCap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		rate  float64
		burst int
	}{
		// 补满恰好差一秒到上限：加上余量后会越界，正是此前失败的那组。
		{name: "one second short of the cap", rate: 1, burst: 86399},
		{name: "exactly at the cap", rate: 1, burst: 86400},
		{name: "well beyond the cap", rate: 1, burst: 1_000_000},
		{name: "ordinary policy", rate: 10, burst: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ttl := bucketTTL(Policy{
				ID: "cap.probe", Rate: tt.rate, Burst: tt.burst,
				Scope: ScopeIP, Store: StoreRedis,
				FailureMode: FailureClosed, MaxKeys: 100,
			})

			if ttl <= 0 {
				t.Fatalf("bucketTTL = %v, want positive", ttl)
			}
			if ttl > maxBucketTTL {
				t.Fatalf("bucketTTL = %v exceeds the declared cap %v", ttl, maxBucketTTL)
			}
		})
	}
}

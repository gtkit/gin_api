package ratelimit

import (
	"context"
	"errors"
	"fmt"
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

func (c *keyCapturingClient) Process(_ context.Context, cmd redis.Cmder) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	keyCount, ok := cmd.Args()[2].(int)
	if !ok {
		return fmt.Errorf("unexpected key count %T", cmd.Args()[2])
	}
	for _, key := range cmd.Args()[3 : 3+keyCount] {
		value, ok := key.(string)
		if !ok {
			return fmt.Errorf("unexpected key type %T", key)
		}
		c.keys = append(c.keys, value)
	}

	err := errors.New("probe stops here")
	cmd.SetErr(err)
	return err
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

// TestKeyBuilderComposition 验证键的四段组成与主体 HMAC。
func TestKeyBuilderComposition(t *testing.T) {
	t.Parallel()

	const secret = "rate-limit-test-secret-not-a-real-credential"
	builder, err := NewKeyBuilder(secret, "svc")
	if err != nil {
		t.Fatalf("NewKeyBuilder: %v", err)
	}

	policy := testPolicy(nil)
	ipSubject := Subject{Scope: ScopeIP, Value: "203.0.113.7"}

	key := builder.Build(policy, "GET", "/api/v1/users/:id", ipSubject)

	for _, want := range []string{"svc", policy.ID, "GET", "/api/v1/users/:id", "ip"} {
		if !strings.Contains(key, want) {
			t.Fatalf("key %q does not contain %q", key, want)
		}
	}
	// 主体原文绝不能出现在键里：Redis 的读权限不该等于"拿到所有被限流的 IP"。
	if strings.Contains(key, ipSubject.Value) {
		t.Fatalf("key %q leaks the raw subject identifier", key)
	}

	// 方法与路由模式必须参与组键，否则不同代价的操作会共用一个桶。
	if key == builder.Build(policy, "POST", "/api/v1/users/:id", ipSubject) {
		t.Fatal("GET and POST produced the same key")
	}
	if key == builder.Build(policy, "GET", "/api/v1/orders/:id", ipSubject) {
		t.Fatal("two different route patterns produced the same key")
	}
	// 不同策略必须有独立的桶。
	other := testPolicy(func(p *Policy) { p.ID = "other.policy" })
	if key == builder.Build(other, "GET", "/api/v1/users/:id", ipSubject) {
		t.Fatal("two different policies produced the same key")
	}
}

// TestKeyBuilderSecretIsMandatoryAndLongEnough 钉住密钥长度下限。
//
// 短密钥让 HMAC 可暴力破解，键空间重新变得可枚举——那等于回到普通哈希。
func TestKeyBuilderSecretIsMandatoryAndLongEnough(t *testing.T) {
	t.Parallel()

	if _, err := NewKeyBuilder("", "svc"); err == nil {
		t.Fatal("NewKeyBuilder accepted an empty secret")
	}
	if _, err := NewKeyBuilder(strings.Repeat("a", minSecretLength-1), "svc"); err == nil {
		t.Fatal("NewKeyBuilder accepted a too-short secret")
	}
	if _, err := NewKeyBuilder(strings.Repeat("a", minSecretLength), ""); err == nil {
		t.Fatal("NewKeyBuilder accepted an empty prefix")
	}
}

// TestKeyBuilderUsesFixedFallbackRouteKeys 验证 404/405 用固定常量而非原始路径。
//
// 原始路径是客户端完全可控的高基数输入：用它组键会让键空间无限膨胀，
// 而扫描器恰好是最会制造大量不同路径的那类客户端。
func TestKeyBuilderUsesFixedFallbackRouteKeys(t *testing.T) {
	t.Parallel()

	builder, err := NewKeyBuilder(strings.Repeat("k", minSecretLength), "svc")
	if err != nil {
		t.Fatalf("NewKeyBuilder: %v", err)
	}

	policy := testPolicy(nil)
	subject := Subject{Scope: ScopeIP, Value: "203.0.113.7"}

	// FullPath() 在未命中路由时为空。
	key := builder.Build(policy, "GET", "", subject)
	if !strings.Contains(key, notFoundRouteKey) {
		t.Fatalf("key %q does not use the fixed not-found route token", key)
	}

}

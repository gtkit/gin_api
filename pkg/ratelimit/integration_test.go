//go:build integration

// 真实 Redis 集成测试：令牌桶脚本。
//
// 覆盖的轴——**只有真实 Redis 能回答的那些**：
//   - Lua 返回值的**编排契约**。脚本返回三元组，Go 侧按 `[]any` 三元素 + 逐个
//     `toInt64` 解析，对不上就报 "unexpected rate limit script result shape"。
//     miniredis 跑的是 gopher-lua，它的类型转换与真实 Redis 是两套实现——
//     只在内嵌实例上验过，等于没验过真实那套。
//   - 桶在真实服务上的行为：突发额度用尽后确实被拒，且给出可用的重试间隔。
//   - SCRIPT FLUSH 之后的自愈（opt-in，见 requireScriptFlushPermission）。
//     主从切换与 SCRIPT FLUSH 之后脚本缓存就是空的；缺了那条重载分支，
//     一次故障切换会让整片限流持续报错，而根因只是缓存没了。
//
// 本文件自带连接与跳过的辅助函数，**不复用 internal/testutil**：
// 本包对外发布，禁止 import 本仓 internal（那会让外部使用者编译失败）。
// 这点重复是边界的代价，不是疏忽。
//
// 安全设计：连专用 db（默认 15），键带本次运行独占前缀，结束只删自己的键。
//
// 运行方式：
//
//	go test -tags integration -run TestIntegration ./pkg/ratelimit/
//	ALLOW_REDIS_SCRIPT_FLUSH=1 go test -tags integration ./pkg/ratelimit/
package ratelimit_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gtkit/gin-api/pkg/ratelimit"

	"github.com/redis/go-redis/v9"
)

// testPolicy 是一条突发额度很小的策略：额度小才能在几次调用内跑到拒绝分支。
func testPolicy() ratelimit.Policy {
	return ratelimit.Policy{
		ID: "itest",
		// 速率取极低值，确保测试期间几乎不会有令牌回填，
		// 否则"用尽后被拒"会变成一条依赖时序的用例。
		Rate:        0.01,
		Burst:       2,
		Scope:       ratelimit.ScopeGlobal,
		Store:       ratelimit.StoreRedis,
		FailureMode: ratelimit.FailureOpen,
		MaxKeys:     1000,
	}
}

func newLimiter(t *testing.T) (*ratelimit.RedisLimiter, *redis.Client) {
	t.Helper()

	client := realRedisOrSkip(t)
	prefix := uniqueKeyPrefix(t, client)

	backend, err := ratelimit.NewRedisBackend(context.Background(), client,
		ratelimit.WithKeyPrefix(prefix))
	if err != nil {
		t.Fatalf("NewRedisBackend against real redis: %v", err)
	}
	limiter, err := ratelimit.NewRedisLimiter(backend, testPolicy())
	if err != nil {
		t.Fatalf("NewRedisLimiter: %v", err)
	}
	return limiter, client
}

// TestIntegrationTokenBucketAllowsThenRejects 钉住桶的行为与返回值编排。
//
// 判定次数刚好走完 Burst：前 Burst 次放行，第 Burst+1 次被拒并给出重试间隔。
// 重试间隔必须为正——它会被中间件写进 Retry-After，为 0 等于告诉客户端立刻重试。
func TestIntegrationTokenBucketAllowsThenRejects(t *testing.T) {
	ctx := context.Background()
	limiter, _ := newLimiter(t)
	burst := limiter.Policy().Burst

	for i := 1; i <= burst; i++ {
		decision, err := limiter.Check(ctx, "subject-a")
		if err != nil {
			t.Fatalf("Check %d: %v", i, err)
		}
		if decision.Outcome != ratelimit.OutcomeAllowed {
			t.Fatalf("Check %d outcome = %v, want allowed (burst is %d)", i, decision.Outcome, burst)
		}
	}

	decision, err := limiter.Check(ctx, "subject-a")
	if err != nil {
		t.Fatalf("Check %d: %v", burst+1, err)
	}
	if decision.Outcome != ratelimit.OutcomeRateLimited {
		t.Fatalf("Check %d outcome = %v, want rate limited", burst+1, decision.Outcome)
	}
	if decision.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %v, want positive (it goes into the Retry-After header)", decision.RetryAfter)
	}
}

// TestIntegrationBucketsAreIsolatedPerSubject 钉住不同主体各有自己的桶。
//
// 若键拼接漏了主体，所有请求会共享一个桶——限额瞬间变成全局的，
// 而配置里写的是每主体的值。
func TestIntegrationBucketsAreIsolatedPerSubject(t *testing.T) {
	ctx := context.Background()
	limiter, _ := newLimiter(t)
	burst := limiter.Policy().Burst

	// 把 A 的桶耗尽。
	for i := 0; i <= burst; i++ {
		if _, err := limiter.Check(ctx, "subject-a"); err != nil {
			t.Fatalf("draining subject-a: %v", err)
		}
	}

	// B 不该受影响。
	decision, err := limiter.Check(ctx, "subject-b")
	if err != nil {
		t.Fatalf("Check for subject-b: %v", err)
	}
	if decision.Outcome != ratelimit.OutcomeAllowed {
		t.Fatalf("subject-b outcome = %v, want allowed; the buckets are not isolated", decision.Outcome)
	}
}

// TestIntegrationRateLimitSurvivesScriptFlush 钉住脚本缓存被清空后的自愈。
//
// opt-in：SCRIPT FLUSH 是服务器级操作，会清掉同一实例上别人的脚本缓存。
func TestIntegrationRateLimitSurvivesScriptFlush(t *testing.T) {
	requireScriptFlushPermission(t)

	ctx := context.Background()
	limiter, client := newLimiter(t)

	if _, err := limiter.Check(ctx, "subject-a"); err != nil {
		t.Fatalf("Check before flush: %v", err)
	}
	if err := client.ScriptFlush(ctx).Err(); err != nil {
		t.Fatalf("SCRIPT FLUSH: %v", err)
	}

	decision, err := limiter.Check(ctx, "subject-a")
	if err != nil {
		t.Fatalf("Check after SCRIPT FLUSH: %v (the NOSCRIPT reload path did not recover)", err)
	}
	if decision.Outcome == ratelimit.OutcomeUnspecified {
		t.Fatalf("outcome after SCRIPT FLUSH = %v, want a real decision", decision.Outcome)
	}
}

// ── 本包自带的集成测试辅助（不复用 internal/testutil，理由见文件头）──────

const (
	redisAddrEnv     = "REDIS_ITEST_ADDR"
	redisPasswordEnv = "REDIS_ITEST_PASSWORD"
	redisDBEnv       = "REDIS_ITEST_DB"
	scriptFlushEnv   = "ALLOW_REDIS_SCRIPT_FLUSH"

	defaultRedisAddr = "127.0.0.1:6379"
	defaultRedisDB   = 15
)

// skipOrFail：默认 Skip；REQUIRE_DB_INTEGRATION=1 时变 Fatal，
// 不允许强制 job 的绿灯里藏 SKIP。
func skipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("REQUIRE_DB_INTEGRATION") == "1" {
		t.Fatalf("REQUIRE_DB_INTEGRATION=1 但集成前置不满足: "+format, args...)
	}
	t.Skipf(format, args...)
}

func realRedisOrSkip(t *testing.T) *redis.Client {
	t.Helper()

	addr := defaultRedisAddr
	if v := os.Getenv(redisAddrEnv); v != "" {
		addr = v
	}
	db := defaultRedisDB
	if raw := os.Getenv(redisDBEnv); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s=%q is not a number", redisDBEnv, raw)
		}
		db = parsed
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv(redisPasswordEnv),
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		skipOrFail(t, "real redis unreachable at %s db %d: %v", addr, db, err)
		return nil
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// uniqueKeyPrefix 返回本次测试独占的键前缀，并登记只删该前缀下键的清理动作。
// 不用 FLUSHDB：一次误配的 db 序号会让它清掉别人的数据，而那是不可逆的。
func uniqueKeyPrefix(t *testing.T, client *redis.Client) string {
	t.Helper()

	prefix := "itest-rl-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, prefix+"*", 256).Result()
			if err != nil {
				t.Logf("cleanup scan for %s*: %v", prefix, err)
				return
			}
			if len(keys) > 0 {
				if err := client.Del(ctx, keys...).Err(); err != nil {
					t.Logf("cleanup del for %s*: %v", prefix, err)
					return
				}
			}
			if next == 0 {
				return
			}
			cursor = next
		}
	})
	return prefix
}

func requireScriptFlushPermission(t *testing.T) {
	t.Helper()
	if os.Getenv(scriptFlushEnv) != "1" {
		t.Skipf("set %s=1 to allow SCRIPT FLUSH; it is server-wide and evicts other "+
			"applications' cached scripts on the same instance", scriptFlushEnv)
	}
}

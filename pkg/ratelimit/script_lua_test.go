package ratelimit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// 本文件用 miniredis **真实执行** Lua 脚本。
//
// 此前 pkg/ratelimit 的 Redis 测试用的是不解释 Lua 的假客户端，于是脚本内的
// 逻辑零覆盖——而恰恰是脚本里的一条守卫把"正常空闲"误判成了时钟故障，
// 这个缺陷因此长期存活。判定逻辑住在脚本里，测试就必须执行脚本。

// luaBase 是存储时钟的固定起点，使各用例的时间推进确定可复现。
var luaBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// newLuaBackend 造一个跑真实 Lua 的 backend，并把存储时钟钉在 luaBase。
func newLuaBackend(t *testing.T) (*RedisBackend, *miniredis.Miniredis) {
	t.Helper()
	// miniredis 起真实 TCP 监听：受限环境必须跳过，否则 check-restricted 变红。
	requireLoopback(t)

	mr := miniredis.RunT(t)
	mr.SetTime(luaBase)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	backend, err := NewRedisBackend(context.Background(), client, WithKeyPrefix("lua"))
	if err != nil {
		t.Fatalf("NewRedisBackend: %v", err)
	}
	return backend, mr
}

// advanceStore 把存储时钟往前推 d。
//
// 只需推这一个钟：判定的时间基准现在完全取自 Redis，调用方不再供时。
func advanceStore(mr *miniredis.Miniredis, d time.Duration) {
	mr.SetTime(luaBase.Add(d))
}

// TestLuaMaxKeysAdmission 钉住 redis 存储的键基数硬上限（rate-limiting spec）：
// 达上限拒绝新建、活跃桶不受影响、闲置过期释放名额——与 local 后端同契约。
// 缺这段准入，攻击者轮换主体可让 Redis 键无界增长。
func TestLuaMaxKeysAdmission(t *testing.T) {
	backend, mr := newLuaBackend(t)
	policy := luaTestPolicy()
	policy.MaxKeys = 2

	// 占满名额：两个主体各建一个桶。
	for _, key := range []string{"subject-a", "subject-b"} {
		d, err := backend.check(context.Background(), policy, key)
		if err != nil || d.Outcome != OutcomeAllowed {
			t.Fatalf("check(%s) = (%v, %v), want allowed", key, d.Outcome, err)
		}
	}

	// 第三个主体：达上限拒绝新建，且判定语义是键基数而不是限速。
	d, err := backend.check(context.Background(), policy, "subject-c")
	if err != nil {
		t.Fatalf("check(subject-c): %v", err)
	}
	if d.Outcome != OutcomeKeyBudgetExhausted {
		t.Fatalf("Outcome = %s, want key_budget_exhausted", d.Outcome)
	}

	// 活跃桶不受影响：已登记主体继续按令牌桶判定（Burst=2，第二次仍放行）。
	d, err = backend.check(context.Background(), policy, "subject-a")
	if err != nil || d.Outcome != OutcomeAllowed {
		t.Fatalf("existing subject = (%v, %v), want allowed (活跃桶不得被新键挤掉)", d.Outcome, err)
	}

	// 闲置过期释放名额：推过桶 TTL 后新主体可入场。
	advanceStore(mr, bucketTTL(policy)+time.Second)
	d, err = backend.check(context.Background(), policy, "subject-d")
	if err != nil || d.Outcome != OutcomeAllowed {
		t.Fatalf("after idle expiry = (%v, %v), want allowed (名额应随闲置成员释放)", d.Outcome, err)
	}
}

func luaTestPolicy() Policy {
	return Policy{
		ID:      "lua.test",
		Scope:   ScopeIP,
		Rate:    10,
		Burst:   2,
		MaxKeys: 100, // 生产上 Validate 强制 > 0；准入段对 0 会拒绝一切新键
	}
}

// TestLuaLongIdleIsNotAFault 钉住长时间空闲后按正常请求处理。
//
// 缺陷：守卫写的是 `now_ms > last_ms + max_skew_ms`，而 last_ms 是**上一次
// 请求**写入的时间戳、上限只有 5 秒、键 TTL 却是补满时间 +60 秒。于是任何
// 间隔超过 5 秒的正常请求都被判成时钟故障——fail_open 下限流被绕过，
// fail_closed 下持续 429 且不写回状态，客户端重试到键过期为止。
//
// 同一份脚本里第 64-69 行专门写了"一段很长的空闲之后"的截断逻辑，
// 却因为守卫先返回而永远不可达：两段代码互相矛盾。
func TestLuaLongIdleIsNotAFault(t *testing.T) {
	backend, mr := newLuaBackend(t)
	policy := luaTestPolicy()
	ctx := context.Background()

	// 先打一次，把状态写进去（此后 last_ms 就是"上一次请求时刻"）。
	if _, err := backend.check(ctx, policy, "idle-key"); err != nil {
		t.Fatalf("首次请求: %v", err)
	}

	// 让存储时钟前进到远超任何漂移上限之后再打一次。
	advanceStore(mr, 10*time.Minute)

	decision, err := backend.check(ctx, policy, "idle-key")
	if err != nil {
		t.Fatalf("长时间空闲后的请求被判为故障: %v（空闲不是时钟异常）", err)
	}
	if decision.Outcome != OutcomeAllowed {
		t.Fatalf("Outcome = %v, want %v：空闲后令牌应已补满", decision.Outcome, OutcomeAllowed)
	}
}

// TestLuaRejectsWhenBurstExhausted 回归保护：正常限流仍然生效。
//
// 时间基准换成存储时钟之后，"超出突发即拒绝"必须不变——否则上一条用例
// 可以靠"永远放行"通过。
func TestLuaRejectsWhenBurstExhausted(t *testing.T) {
	backend, _ := newLuaBackend(t)
	policy := luaTestPolicy() // Burst = 2
	ctx := context.Background()

	for i := range policy.Burst {
		decision, err := backend.check(ctx, policy, "burst-key")
		if err != nil {
			t.Fatalf("第 %d 次请求: %v", i+1, err)
		}
		if decision.Outcome != OutcomeAllowed {
			t.Fatalf("第 %d 次请求 Outcome = %v, want %v", i+1, decision.Outcome, OutcomeAllowed)
		}
	}

	decision, err := backend.check(ctx, policy, "burst-key")
	if err != nil {
		t.Fatalf("超额请求返回错误: %v", err)
	}
	if decision.Outcome != OutcomeRateLimited {
		t.Fatalf("Outcome = %v, want %v：突发耗尽后应当拒绝", decision.Outcome, OutcomeRateLimited)
	}
	if decision.RetryAfter <= 0 {
		t.Error("被拒绝时 RetryAfter 应为正值")
	}
}

// TestLuaRefillsOverTime 钉住令牌按存储时钟补充。
func TestLuaRefillsOverTime(t *testing.T) {
	backend, mr := newLuaBackend(t)
	policy := luaTestPolicy() // Rate = 10/s，Burst = 2
	ctx := context.Background()

	for range policy.Burst {
		if _, err := backend.check(ctx, policy, "refill-key"); err != nil {
			t.Fatalf("预热请求: %v", err)
		}
	}
	if d, _ := backend.check(ctx, policy, "refill-key"); d.Outcome != OutcomeRateLimited {
		t.Fatalf("预热后应处于限流状态，实际 %v", d.Outcome)
	}

	// 10/s 下 200ms 足够补出 1 个令牌。
	advanceStore(mr, 200*time.Millisecond)

	decision, err := backend.check(ctx, policy, "refill-key")
	if err != nil {
		t.Fatalf("补充后请求: %v", err)
	}
	if decision.Outcome != OutcomeAllowed {
		t.Fatalf("Outcome = %v, want %v：等待之后应当补出令牌", decision.Outcome, OutcomeAllowed)
	}
}

// TestProbeIsRepeatableAndLeavesNothing 是**回归测试**（rate-limiting spec）：
// 连续构造多个 backend（模拟进程反复重启）时，每次探测都真实放行，
// 且不留下任何探测产物。
//
// 上一批的探针用固定策略标识，其键基数注册表因此在重启间共享；探测只删桶键、
// 不删注册表成员，于是第二次探测就撞上 MaxKeys=1 被拒——而探针忽略判定结果，
// 把"被拒"报告成"通过"。本测试的两个断言各盯一半：探测可重复、产物已清理。
func TestProbeIsRepeatableAndLeavesNothing(t *testing.T) {
	requireLoopback(t)

	mr := miniredis.RunT(t)
	mr.SetTime(luaBase)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// 时钟不推进：所有探测产物都在存活期内，最能暴露"跨重启共享注册表"。
	for i := range 3 {
		if _, err := NewRedisBackend(context.Background(), client, WithKeyPrefix("lua")); err != nil {
			t.Fatalf("probe %d failed: %v (a restart within the artifact TTL must still verify)", i, err)
		}
	}

	for _, key := range mr.Keys() {
		if strings.Contains(key, "probe") || strings.Contains(key, "registry") {
			t.Fatalf("probe left %q behind: its artifacts must be cleaned so the next "+
				"process start verifies capability instead of colliding with them", key)
		}
	}
}

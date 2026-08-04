package ratelimit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// fakeRedis 在内存里模拟本包用到的三个命令。
//
// 它**不模拟 Lua**——那样只会把脚本再实现一遍，测的是模拟器而不是脚本。
// 这里模拟的是脚本**外围**的契约：SHA 缓存、NOSCRIPT 重载重试、
// 返回值解析、以及故障如何影响健康状态。脚本自身的算法由
// 定点换算与策略校验的用例覆盖。
type fakeRedis struct {
	mu sync.Mutex

	loadedSHA   string
	loadCalls   int
	evalCalls   int
	delCalls    int
	loadErr     error
	delErr      error
	evalErrs    []error // 按调用序返回；用尽后返回 nil
	evalResults []any   // 按调用序返回；用尽后重复最后一个
}

func (f *fakeRedis) ScriptLoad(ctx context.Context, _ string) *redis.StringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.loadCalls++
	cmd := redis.NewStringCmd(ctx)
	if f.loadErr != nil {
		cmd.SetErr(f.loadErr)
		return cmd
	}
	f.loadedSHA = "sha-" + string(rune('a'+f.loadCalls))
	cmd.SetVal(f.loadedSHA)
	return cmd
}

func (f *fakeRedis) EvalSha(ctx context.Context, _ string, _ []string, _ ...any) *redis.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()

	cmd := redis.NewCmd(ctx)
	idx := f.evalCalls
	f.evalCalls++

	if idx < len(f.evalErrs) && f.evalErrs[idx] != nil {
		cmd.SetErr(f.evalErrs[idx])
		return cmd
	}
	switch {
	case len(f.evalResults) == 0:
		cmd.SetVal([]any{int64(statusAllowed), int64(4_000_000), int64(0)})
	case idx < len(f.evalResults):
		cmd.SetVal(f.evalResults[idx])
	default:
		cmd.SetVal(f.evalResults[len(f.evalResults)-1])
	}
	return cmd
}

func (f *fakeRedis) Del(ctx context.Context, _ ...string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.delCalls++
	cmd := redis.NewIntCmd(ctx)
	if f.delErr != nil {
		cmd.SetErr(f.delErr)
		return cmd
	}
	cmd.SetVal(1)
	return cmd
}

func (f *fakeRedis) counts() (load, eval, del int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadCalls, f.evalCalls, f.delCalls
}

func redisTestPolicy() Policy {
	return Policy{
		ID: "redis.policy", Rate: 10, Burst: 5,
		Scope: ScopeIP, Store: StoreRedis,
		FailureMode: FailureClosed, MaxKeys: 100,
	}
}

// TestRedisBackendProbesCapabilityNotJustConnectivity 钉住"启动探测必须验能力"。
//
// 只 PING 通过只说明连接活着：不说明当前 ACL 允许 EVALSHA、不说明脚本能加载、
// 也不说明返回值契约与代码期望一致。资源层 Ping 成功但能力探测失败的实例
// 绝不能被判为 ready——它会在第一个真实请求上才暴露。
func TestRedisBackendProbesCapabilityNotJustConnectivity(t *testing.T) {
	t.Parallel()

	fake := &fakeRedis{}
	backend, err := NewRedisBackend(context.Background(), fake, WithKeyPrefix("svc"))
	if err != nil {
		t.Fatalf("NewRedisBackend: %v", err)
	}
	if backend == nil {
		t.Fatal("NewRedisBackend returned no backend")
	}

	load, eval, del := fake.counts()
	if load != 1 {
		t.Fatalf("script load calls = %d, want 1", load)
	}
	if eval != 1 {
		t.Fatalf("probe eval calls = %d, want 1: the probe must run a real limiter call", eval)
	}
	// 探测键必须清理，否则每次启动都在 Redis 里留一个无人认领的键。
	if del != 1 {
		t.Fatalf("probe key deletions = %d, want 1", del)
	}
}

// TestRedisBackendFailsWhenScriptCannotLoad 验证脚本加载失败即构造失败。
func TestRedisBackendFailsWhenScriptCannotLoad(t *testing.T) {
	t.Parallel()

	fake := &fakeRedis{loadErr: errors.New("NOPERM this user has no permissions to run 'script'")}
	if _, err := NewRedisBackend(context.Background(), fake, WithKeyPrefix("svc")); err == nil {
		t.Fatal("NewRedisBackend succeeded although the script could not be loaded")
	}
}

// TestRedisBackendReportsUncleanProbe 验证探测键删除失败被报告。
//
// 不报的话，"探测留了垃圾"永远不会被发现——键虽有短 TTL，但删除失败
// 通常意味着权限问题，那会在别处以更难查的方式再次出现。
func TestRedisBackendReportsUncleanProbe(t *testing.T) {
	t.Parallel()

	fake := &fakeRedis{delErr: errors.New("NOPERM del")}
	_, err := NewRedisBackend(context.Background(), fake, WithKeyPrefix("svc"))
	if err == nil {
		t.Fatal("NewRedisBackend succeeded although the probe key could not be removed")
	}
	if !strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("error %q does not describe the probe cleanup failure", err)
	}
}

// TestRedisBackendRetriesOnNoScript 钉住 NOSCRIPT 的重载重试。
//
// 主从切换或 SCRIPT FLUSH 之后脚本缓存会丢。缺这一条，一次故障切换
// 会让整片限流持续报错，而根因只是缓存没了——一个纯粹可自愈的状态。
func TestRedisBackendRetriesOnNoScript(t *testing.T) {
	t.Parallel()

	fake := &fakeRedis{}
	backend, err := NewRedisBackend(context.Background(), fake, WithKeyPrefix("svc"))
	if err != nil {
		t.Fatalf("NewRedisBackend: %v", err)
	}

	loadBefore, evalBefore, _ := fake.counts()

	// 下一次 eval 返回 NOSCRIPT，再下一次成功。
	fake.mu.Lock()
	fake.evalErrs = make([]error, evalBefore+1)
	fake.evalErrs[evalBefore] = errors.New("NOSCRIPT No matching script")
	fake.mu.Unlock()

	limiter, err := NewRedisLimiter(backend, redisTestPolicy())
	if err != nil {
		t.Fatalf("NewRedisLimiter: %v", err)
	}

	decision, err := limiter.Check(context.Background(), "svc:rl:redis.policy:GET:/x:ip:abc")
	if err != nil {
		t.Fatalf("Check after NOSCRIPT: %v", err)
	}
	if !decision.Allowed() {
		t.Fatalf("decision = %s, want allowed after a successful retry", decision.Outcome)
	}

	loadAfter, evalAfter, _ := fake.counts()
	if loadAfter != loadBefore+1 {
		t.Fatalf("script reloads = %d, want exactly one reload after NOSCRIPT", loadAfter-loadBefore)
	}
	if evalAfter != evalBefore+2 {
		t.Fatalf("eval calls = %d, want the failed call plus exactly one retry", evalAfter-evalBefore)
	}
}

// TestRedisBackendClockSkewIsAFaultNotAClamp 钉住时钟漂移的处置。
//
// v9 的做法是"钳制到上限再继续算"——那本身就是绕过：每个请求都把虚拟时间
// 往前推一个上限，于是每个请求都白得 `上限 × rate` 个 token，限流完全失效。
// 定稿是：漂移超限 = 故障态，不推进不写回，按 FailureMode 处理。
func TestRedisBackendClockSkewIsAFaultNotAClamp(t *testing.T) {
	t.Parallel()

	fake := &fakeRedis{}
	backend, err := NewRedisBackend(context.Background(), fake, WithKeyPrefix("svc"))
	if err != nil {
		t.Fatalf("NewRedisBackend: %v", err)
	}

	// 让后续调用一律返回"漂移超限"。
	fake.mu.Lock()
	fake.evalResults = []any{
		[]any{int64(statusAllowed), int64(4_000_000), int64(0)}, // probe
		[]any{int64(statusClockSkew), int64(0), int64(0)},
	}
	fake.mu.Unlock()

	policy := redisTestPolicy()
	limiter, err := NewRedisLimiter(backend, policy)
	if err != nil {
		t.Fatalf("NewRedisLimiter: %v", err)
	}

	decision, err := limiter.Check(context.Background(), "k")
	if !errors.Is(err, ErrClockSkew) {
		t.Fatalf("error = %v, want ErrClockSkew", err)
	}
	// FailureClosed → 拒绝。
	if decision.Allowed() {
		t.Fatal("a clock-skew fault was allowed under fail_closed")
	}
	if !decision.Degraded {
		t.Fatal("the decision is not marked as degraded")
	}

	// **漂移不得把 backend 拖进 Degraded**：那不是 Redis 的错，
	// 否则一次 NTP 抖动会让整条限流链路切到降级路径。
	if got := backend.Health().Phase(); got != PhaseHealthy {
		t.Fatalf("backend phase = %s after clock skew, want healthy", got)
	}
}

// TestRedisLimiterFailureModes 验证后端故障时三种模式的最终判定。
func TestRedisLimiterFailureModes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		mode        FailureMode
		wantAllowed bool
	}{
		{name: "fail_open allows", mode: FailureOpen, wantAllowed: true},
		{name: "fail_closed rejects", mode: FailureClosed, wantAllowed: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeRedis{}
			backend, err := NewRedisBackend(context.Background(), fake, WithKeyPrefix("svc"))
			if err != nil {
				t.Fatalf("NewRedisBackend: %v", err)
			}

			// probe 之后的调用一律失败。
			fake.mu.Lock()
			fake.evalErrs = []error{nil, errors.New("dial tcp: connection refused")}
			fake.mu.Unlock()

			policy := redisTestPolicy()
			policy.FailureMode = tc.mode
			limiter, err := NewRedisLimiter(backend, policy)
			if err != nil {
				t.Fatalf("NewRedisLimiter: %v", err)
			}

			decision, checkErr := limiter.Check(context.Background(), "k")
			if checkErr == nil {
				t.Fatal("Check returned no error although the backend failed")
			}
			if decision.Allowed() != tc.wantAllowed {
				t.Fatalf("allowed = %v, want %v under %s", decision.Allowed(), tc.wantAllowed, tc.mode)
			}
			if !decision.Degraded {
				t.Fatal("the decision is not marked as degraded")
			}
			if !tc.wantAllowed && decision.RetryAfterSeconds() < 1 {
				t.Fatal("a rejected request carries a Retry-After below the legal minimum of 1 second")
			}
		})
	}
}

// TestFallbackBackendSwitchesToShadowBucket 验证 local_fallback。
//
// 影子桶平时不消耗 token，因此切换瞬间是满桶——这正是"只能称算法模型一致、
// 不能称状态连续等价"的原因。测试同时确认降级标记被带出来，
// 否则上层无从知道这次判定走的是降级路径。
func TestFallbackBackendSwitchesToShadowBucket(t *testing.T) {
	t.Parallel()

	fake := &fakeRedis{}
	backend, err := NewRedisBackend(context.Background(), fake, WithKeyPrefix("svc"))
	if err != nil {
		t.Fatalf("NewRedisBackend: %v", err)
	}

	fake.mu.Lock()
	fake.evalErrs = []error{nil, errors.New("dial tcp: connection refused")}
	fake.mu.Unlock()

	policy := redisTestPolicy()
	policy.FailureMode = FailureLocalFallback
	fallback, err := NewFallbackBackend(backend, policy)
	if err != nil {
		t.Fatalf("NewFallbackBackend: %v", err)
	}
	defer fallback.Close()

	decision, err := fallback.Check(context.Background(), "k")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !decision.Allowed() {
		t.Fatal("the shadow bucket rejected the first request; it should start full")
	}
	if !decision.Degraded {
		t.Fatal("a fallback decision is not marked as degraded")
	}
	if got := fallback.Policy().ID; got != policy.ID {
		t.Fatalf("Policy().ID = %q, want %q", got, policy.ID)
	}
}

// TestParseScriptResultRejectsUnexpectedShapes 验证返回值契约被严格校验。
//
// 宽松解析会让脚本改动后的形状漂移悄悄通过，而表现是限流结果错误——
// 没有任何报错。
func TestParseScriptResultRejectsUnexpectedShapes(t *testing.T) {
	t.Parallel()

	policy := redisTestPolicy()

	cases := []struct {
		name string
		raw  any
	}{
		{name: "not a slice", raw: "ok"},
		{name: "wrong arity", raw: []any{int64(1), int64(2)}},
		{name: "non-integer status", raw: []any{"1", int64(0), int64(0)}},
		{name: "unknown status", raw: []any{int64(7), int64(0), int64(0)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseScriptResult(tc.raw, policy); err == nil {
				t.Fatalf("parseScriptResult(%v) = nil error, want a rejection", tc.raw)
			}
		})
	}
}

// TestBucketTTLCoversRefillPlusSlack 验证 TTL 覆盖补满时间且带余量。
//
// 没有余量的话，一个刚好在补满边界到达的请求会读到已过期的键、拿到满桶——
// 限额被悄悄放大。
func TestBucketTTLCoversRefillPlusSlack(t *testing.T) {
	t.Parallel()

	policy := redisTestPolicy() // Rate 10 / Burst 5 → 补满需 0.5s，向上取整 1s
	got := bucketTTL(policy)
	if got <= ttlSlack {
		t.Fatalf("bucketTTL = %s, want more than the slack alone (%s)", got, ttlSlack)
	}
	if got < time.Second+ttlSlack {
		t.Fatalf("bucketTTL = %s, want at least the refill time plus slack", got)
	}
}

// TestIsNoScript 验证 NOSCRIPT 识别只认前缀，不误伤其他错误。
func TestIsNoScript(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "noscript", err: errors.New("NOSCRIPT No matching script"), want: true},
		{name: "other redis error", err: errors.New("WRONGTYPE"), want: false},
		{name: "mentions noscript later", err: errors.New("failed: NOSCRIPT"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isNoScript(tc.err); got != tc.want {
				t.Fatalf("isNoScript(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

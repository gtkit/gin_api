package ratelimit

import (
	"context"
	"errors"
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

// TestRedisBackendToleratesUncleanProbe 验证探测键删除失败**不**让构造失败。
//
// 本测试此前叫 TestRedisBackendReportsUncleanProbe，断言的是相反的行为：
// "删除失败被报告"，而报告的形式是让构造返回错误。那个断言有两处站不住：
//
//   - 它的理由写着"键虽有短 TTL，但删除失败通常意味着权限问题"。而权限问题
//     具体就是"允许 EVALSHA、不允许 DEL"这种最小权限配置——限流的运行期路径
//     从不删除键（键一律靠 TTL 回收），所以那个权限缺失不影响任何功能。
//     让它阻断启动，等于以"更安全"的名义拒绝一个完好可用的系统。
//   - 它引用的那句错误信息说键"expire in 10s"，用的是一个从未被用于设置任何
//     TTL 的常量；真实 TTL 由 bucketTTL 算出（探针策略约 61s）。运维照它等
//     10 秒等不到键消失。
//
// 清理仍然会被尝试（见断言），只是失败被就地丢弃——依据是两个键都在创建
// 它们的同一段 Lua 里 PEXPIRE。
func TestRedisBackendToleratesUncleanProbe(t *testing.T) {
	t.Parallel()

	fake := &fakeRedis{delErr: errors.New("NOPERM del")}
	backend, err := NewRedisBackend(context.Background(), fake, WithKeyPrefix("svc"))
	if err != nil {
		t.Fatalf("仅 DEL 无权限时构造必须成功，得到: %v", err)
	}
	if backend == nil {
		t.Fatal("构造成功却返回 nil 后端")
	}
	if _, _, del := fake.counts(); del == 0 {
		t.Error("清理是尽力而为，但必须真的尝试过")
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

package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

// alwaysNoScript 恒返回 NOSCRIPT，逼每个请求都走脚本重载路径。
//
// 这正是主从切换、故障恢复与 SCRIPT FLUSH 之后的真实形态——
// 大量并发请求同时发现脚本缓存没了，同时去重载。
type alwaysNoScript struct{ cause error }

func (alwaysNoScript) ScriptLoad(ctx context.Context, _ string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	cmd.SetVal("reloaded-sha")
	return cmd
}

func (f alwaysNoScript) Process(_ context.Context, cmd redis.Cmder) error {
	cmd.SetErr(f.cause)
	return f.cause
}

func (alwaysNoScript) Del(ctx context.Context, _ ...string) *redis.IntCmd {
	return redis.NewIntCmd(ctx)
}

// TestConcurrentScriptReloadIsRaceFree 钉住脚本缓存标识的并发读写安全。
//
// 必须配 `-race` 才有意义（本仓 make check 的测试一律带 -race）。
//
// 此前 sha 是普通字符串字段：每个请求读它，NOSCRIPT 时并发写它。
// 实测 race detector 报 read/write 与 write/write 两类竞争，而 Go 内存模型下
// 那是未定义行为——最坏情况是读到撕裂的 string header。
//
// 触发条件一点也不罕见：Redis 主从切换、故障恢复、运维执行 SCRIPT FLUSH，
// 每一次都会让在飞的全部请求同时进入重载路径。
func TestConcurrentScriptReloadIsRaceFree(t *testing.T) {
	t.Parallel()

	failure := errors.New("NOSCRIPT No matching script")
	backend := &RedisBackend{
		client:   alwaysNoScript{cause: failure},
		health:   NewHealthState(DefaultHealthPolicy(), nil),
		observer: nopObserver{},
	}
	initial := "initial-sha"
	backend.sha.Store(&initial)

	policy := Policy{
		ID: "concurrent.reload", Rate: 10, Burst: 5,
		Scope: ScopeIP, Store: StoreRedis,
		FailureMode: FailureClosed, MaxKeys: 100,
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			// 首批执行返回NOSCRIPT原因，降级后的短路返回不可用判定。
			decision, err := backend.check(context.Background(), policy, "subject")
			if err != nil && !errors.Is(err, failure) {
				t.Errorf("unexpected reload failure: %v", err)
			} else if err == nil && decision.Outcome != OutcomeBackendUnavailable {
				t.Errorf("degraded request returned %v", decision.Outcome)
			}
		})
	}
	wg.Wait()

	// 重载后标识必须已更新，否则说明重载路径根本没跑到。
	if got := *backend.sha.Load(); got != "reloaded-sha" {
		t.Fatalf("sha = %q, want the reloaded value: the reload path did not run", got)
	}
}

// TestBucketTTLNeverOverflows 钉住键存活时长不会因溢出变成负数。
//
// 此前 `time.Duration(秒数) * time.Second` 直接溢出：Rate 极小而 Burst 较大时，
// 补满一桶所需秒数轻易超过 time.Duration（纳秒计的 int64）能表示的范围。
//
// 而策略校验只查了 Lua 的精确整数范围——**那是另一个边界**。两个边界各查各的，
// 于是一组通过了校验的合法策略产出了 -2346317h46m53s，
// 负 TTL 传给 Redis 会让键立即过期或被拒绝，限流因此静默失效。
func TestBucketTTLNeverOverflows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		rate  float64
		burst int
	}{
		{name: "ordinary policy", rate: 10, burst: 100},
		{name: "slow refill", rate: 0.5, burst: 10},
		// 此前实测溢出的那一组：校验通过，TTL 为负。
		{name: "the combination that used to overflow", rate: 0.000001, burst: 10000},
		{name: "extremely slow refill", rate: 1e-9, burst: 1},
		{name: "large burst with slow refill", rate: 1e-6, burst: 1_000_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policy := Policy{
				ID: "ttl.probe", Rate: tt.rate, Burst: tt.burst,
				Scope: ScopeIP, Store: StoreRedis,
				FailureMode: FailureClosed, MaxKeys: 100,
			}

			ttl := bucketTTL(policy)
			if ttl <= 0 {
				t.Fatalf("bucketTTL = %v: a non-positive TTL makes redis drop or reject the key, "+
					"and the limiter silently stops working", ttl)
			}
			if ttl > maxBucketTTL {
				t.Fatalf("bucketTTL = %v, exceeds the %v cap: a key nobody touches would keep "+
					"occupying memory long after its quota refilled", ttl, maxBucketTTL)
			}
			// 毫秒数同样必须为正——那才是真正传给 Lua 的值。
			if ttl.Milliseconds() <= 0 {
				t.Fatalf("ttl.Milliseconds() = %d, want positive", ttl.Milliseconds())
			}
		})
	}
}

// TestNewRedisBackendDegradedStart 钉住降级启动（rate-limiting spec）：
// WithDegradedStart 下脚本装载失败以 Degraded 相位构造成功、恢复探测可用；
// 缺省行为保持构造失败；取消不被降级吸收。
func TestNewRedisBackendDegradedStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      []Option
		ctx       func() context.Context
		wantErr   bool
		wantPhase HealthPhase
	}{
		{
			name:      "降级启动：装载失败构造成功且相位为 Degraded",
			opts:      []Option{WithDegradedStart()},
			wantPhase: PhaseDegraded,
		},
		{
			name:    "缺省：装载失败即构造失败",
			wantErr: true,
		},
		{
			name: "取消不被降级吸收",
			opts: []Option{WithDegradedStart()},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeRedis{loadErr: errors.New("connection refused")}
			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}

			backend, err := NewRedisBackend(ctx, client, tt.opts...)
			if tt.wantErr {
				if err == nil {
					t.Fatal("NewRedisBackend succeeded, want construction failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRedisBackend: %v", err)
			}
			if got := backend.Health().Phase(); got != tt.wantPhase {
				t.Fatalf("phase = %s, want %s", got, tt.wantPhase)
			}
		})
	}
}

// TestProbeRejectsNonAllowedDecision 是**回归测试**（rate-limiting spec）：
// 探针必须断言判定结果为放行，非放行即探测失败。
//
// 上一批给真实判定路径加了键基数准入，而探针共享那条路径——它用固定策略
// 标识、MaxKeys=1、且只看 err 不看 Decision，于是进程重启后探针恒定拿到
// "键基数拒绝"却报告成功：探针从此不验证任何东西，而它存在的全部理由
// 就是验证端到端能力。
func TestProbeRejectsNonAllowedDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int64
		wantErr bool
	}{
		{name: "放行即探测成功", status: statusAllowed},
		{name: "限速判定即探测失败", status: statusRejected, wantErr: true},
		{name: "键基数拒绝即探测失败", status: statusKeyBudgetDenied, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeRedis{evalResults: []any{
				[]any{tt.status, int64(1000000), int64(0)},
			}}
			_, err := NewRedisBackend(context.Background(), client)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("NewRedisBackend = %v, wantErr = %v", err, tt.wantErr)
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

type cancelingProbeCleanupClient struct {
	*fakeRedis
	cancel context.CancelFunc
}

func (c cancelingProbeCleanupClient) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	c.cancel()
	return c.fakeRedis.Del(ctx, keys...)
}

func TestProbeCleanupCancellationStopsConstructionWithBothCauses(t *testing.T) {
	t.Parallel()
	for _, degraded := range []bool{false, true} {
		t.Run(fmt.Sprintf("degraded=%v", degraded), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cleanup := errors.New("DEL cleanup denied")
			client := cancelingProbeCleanupClient{fakeRedis: &fakeRedis{delErr: cleanup}, cancel: cancel}
			var options []Option
			if degraded {
				options = append(options, WithDegradedStart())
			}
			backend, err := NewRedisBackend(ctx, client, options...)
			if backend != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, cleanup) {
				t.Fatalf("canceled construction backend=%v err=%v", backend, err)
			}
		})
	}
}

func TestProbeCleanupFailureIsRetainedAndNotifiedOnce(t *testing.T) {
	t.Parallel()
	cause := errors.New("NOPERM del")
	var calls atomic.Int32
	backend, err := NewRedisBackend(context.Background(), &fakeRedis{delErr: cause}, WithProbeCleanupErrorHandler(func(err error) {
		if !errors.Is(err, cause) {
			t.Errorf("cleanup notification lost cause: %v", err)
		}
		calls.Add(1)
	}))
	if err != nil {
		t.Fatal(err)
	}
	var work sync.WaitGroup
	for range 16 {
		work.Go(func() {
			if err := backend.probe(context.Background()); err != nil {
				t.Errorf("capability probe: %v", err)
			}
		})
	}
	work.Wait()
	if !errors.Is(backend.ProbeCleanupError(), cause) || calls.Load() != 1 || backend.Health().Phase() != PhaseHealthy {
		t.Fatalf("cleanup=%v calls=%d health=%v", backend.ProbeCleanupError(), calls.Load(), backend.Health().Phase())
	}
}

// TestProbeCapabilityFailureStillFailsConstruction 是上一条的反向锚。
//
// 缺了它，一个"探针所有错误都不致命"的实现也能让上面的用例通过——而那会让
// 一个 ACL 不允许 EVALSHA 的实例被判为可用，问题推迟到第一个真实请求。
func TestProbeCapabilityFailureStillFailsConstruction(t *testing.T) {
	t.Parallel()

	t.Run("脚本无法加载", func(t *testing.T) {
		t.Parallel()
		fake := &fakeRedis{loadErr: errors.New("NOPERM no permissions to run 'script'")}
		if _, err := NewRedisBackend(context.Background(), fake); err == nil {
			t.Fatal("脚本无法加载时构造必须失败")
		}
	})

	t.Run("探针调用报错", func(t *testing.T) {
		t.Parallel()
		fake := &fakeRedis{evalErrs: []error{errors.New("NOPERM no permissions to run 'evalsha'")}}
		if _, err := NewRedisBackend(context.Background(), fake); err == nil {
			t.Fatal("能力校验失败时构造必须失败")
		}
	})
}

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

func (f *fakeRedis) Process(_ context.Context, command redis.Cmder) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	cmd, ok := command.(interface{ SetVal(any) })
	if !ok {
		err := fmt.Errorf("unexpected Redis command %T", command)
		command.SetErr(err)
		return err
	}
	idx := f.evalCalls
	f.evalCalls++

	if idx < len(f.evalErrs) && f.evalErrs[idx] != nil {
		command.SetErr(f.evalErrs[idx])
		return f.evalErrs[idx]
	}
	switch {
	case len(f.evalResults) == 0:
		cmd.SetVal([]any{int64(statusAllowed), int64(4_000_000), int64(0)})
	case idx < len(f.evalResults):
		cmd.SetVal(f.evalResults[idx])
	default:
		cmd.SetVal(f.evalResults[len(f.evalResults)-1])
	}
	return nil
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

func TestDeductionDoesNotReplayAfterServerLosesResponse(t *testing.T) {
	testutil.RequireLoopback(t)
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

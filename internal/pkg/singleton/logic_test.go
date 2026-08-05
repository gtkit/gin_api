package singleton

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 本文件用**手写替身**覆盖不需要真实 Redis 的那部分逻辑。
//
// 与 lease_test.go 的 miniredis 版互补，不重复：
//
//   - miniredis 版真的执行 Lua，覆盖原子抢锁、compare-and-renew 的实际语义
//     ——那是替身证明不了的；
//   - 手写替身能精确制造故障（脚本加载失败、NOSCRIPT、返回值形状不对），
//     那是 miniredis 很难触发的，而它们决定了故障时的行为是否安全。
//
// 另一个实际收益：miniredis 要监听回环端口，受限环境会整包跳过——
// 那时这个包的覆盖率会归零。本文件不碰网络，任何环境都跑得起来。

// fakeLeaseClient 是可编排的 Redis 替身。
type fakeLeaseClient struct {
	mu sync.Mutex

	loadErr   error
	loadCalls int

	// evalResults 按调用序返回；用尽后重复最后一个。
	evalResults []leaseEvalResult
	evalCalls   int
	// evalKeys 记录每次调用收到的 keys，用于验证键的构造。
	evalKeys [][]string
	// evalArgs 记录每次调用收到的 args。
	evalArgs [][]any
}

type leaseEvalResult struct {
	val any
	err error
}

func (c *fakeLeaseClient) ScriptLoad(_ context.Context, _ string) *redis.StringCmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadCalls++
	if c.loadErr != nil {
		return redis.NewStringResult("", c.loadErr)
	}
	return redis.NewStringResult("sha", nil)
}

func (c *fakeLeaseClient) EvalSha(_ context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evalKeys = append(c.evalKeys, keys)
	c.evalArgs = append(c.evalArgs, args)

	idx := c.evalCalls
	c.evalCalls++
	if idx >= len(c.evalResults) {
		if len(c.evalResults) == 0 {
			return redis.NewCmdResult(nil, errors.New("no result configured"))
		}
		idx = len(c.evalResults) - 1
	}
	r := c.evalResults[idx]
	return redis.NewCmdResult(r.val, r.err)
}

func (c *fakeLeaseClient) loadCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadCalls
}

func newFakeLease(t *testing.T, client *fakeLeaseClient) *Lease {
	t.Helper()
	lease, err := NewLease(context.Background(), client, "test", "job", time.Minute)
	if err != nil {
		t.Fatalf("NewLease: %v", err)
	}
	return lease
}

// TestScriptsArePreloadedAtConstruction 钉住三个脚本在构造期加载。
//
// 留到第一次抢锁才加载的话，一个 ACL 不允许 EVAL 的 Redis 会表现为
// "服务起来了，但定时任务一次也没跑"——而那种故障没有任何显式信号。
func TestScriptsArePreloadedAtConstruction(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{}
	newFakeLease(t, client)

	// acquire / renew / release 三个脚本。
	if got := client.loadCount(); got != 3 {
		t.Fatalf("scripts loaded at construction = %d, want 3", got)
	}
}

// TestScriptLoadFailureFailsConstruction 钉住加载失败即构造失败。
func TestScriptLoadFailureFailsConstruction(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{loadErr: errors.New("NOPERM this user has no permissions")}
	_, err := NewLease(context.Background(), client, "test", "job", time.Minute)
	if err == nil {
		t.Fatal("NewLease succeeded although the scripts could not be loaded: the lock " +
			"would fail at the first acquisition instead of at startup")
	}
}

// TestAcquirePassesTheKeysAndArgsItPromises 钉住键与参数的形状。
func TestAcquirePassesTheKeysAndArgsItPromises(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{evalResults: []leaseEvalResult{{val: []any{int64(1), int64(7)}}}}
	lease := newFakeLease(t, client)

	held, err := lease.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if held.Fence != 7 {
		t.Errorf("fence = %d, want 7", held.Fence)
	}
	if held.Token == "" {
		t.Error("Acquire returned an empty token")
	}

	if len(client.evalKeys) != 1 {
		t.Fatalf("eval calls = %d, want 1", len(client.evalKeys))
	}
	keys := client.evalKeys[0]
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want the lock and the fence counter", keys)
	}
	if keys[0] != lease.lockKey() || keys[1] != lease.fenceKey() {
		t.Errorf("keys = %v, want [%q %q]", keys, lease.lockKey(), lease.fenceKey())
	}

	// 三个参数：token、租期毫秒、fence 计数器存活毫秒。
	if got := len(client.evalArgs[0]); got != 3 {
		t.Fatalf("args = %v, want 3 of them", client.evalArgs[0])
	}
	// 生产租约的 fence 计数器**不过期**：清掉会让序号从 1 重来，
	// 而某个仍在运行的旧实例可能持有更大的序号——fencing 反过来起反作用。
	if client.evalArgs[0][2] != "0" {
		t.Errorf("fence ttl arg = %v, want \"0\" (the counter must not expire)",
			client.evalArgs[0][2])
	}
}

// TestAcquireMapsResults 覆盖抢锁返回值到结果的映射。
func TestAcquireMapsResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		val     any
		wantErr error
		// wantMalformed 表示期望一个"形状不对"的错误（不是具名错误）。
		wantMalformed bool
	}{
		{name: "acquired", val: []any{int64(1), int64(3)}},
		{name: "not acquired", val: []any{int64(0), int64(0)}, wantErr: ErrNotAcquired},
		{name: "not a list", val: "ok", wantMalformed: true},
		{name: "wrong arity", val: []any{int64(1)}, wantMalformed: true},
		{name: "non-integer fence", val: []any{int64(1), "three"}, wantMalformed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lease := newFakeLease(t, &fakeLeaseClient{
				evalResults: []leaseEvalResult{{val: tt.val}},
			})
			_, err := lease.Acquire(context.Background())

			switch {
			case tt.wantMalformed:
				if err == nil {
					t.Fatalf("a malformed acquire result %#v was accepted", tt.val)
				}
				if errors.Is(err, ErrNotAcquired) {
					t.Fatal("a malformed result was reported as \"not acquired\": a script " +
						"whose return shape changed would look like normal contention")
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Acquire = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("Acquire: %v", err)
				}
			}
		})
	}
}

// TestRenewMapsResults 覆盖续租返回码到结果的映射。
func TestRenewMapsResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		val     any
		wantErr error
	}{
		{name: "renewed", val: int64(1)},
		{name: "lost", val: int64(0), wantErr: ErrLeaseLost},
		// 非预期的返回值按"已失去租约"处理：那是**安全的方向**。
		// 反过来（当作续租成功）会让一个已经失去租约的实例继续跑下去。
		{name: "unexpected value", val: "yes", wantErr: ErrLeaseLost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lease := newFakeLease(t, &fakeLeaseClient{
				evalResults: []leaseEvalResult{{val: tt.val}},
			})
			err := lease.Renew(context.Background(), Held{Token: "t"})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Renew = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestRedisFailureIsNotMistakenForContention 钉住故障与"锁被占"可区分。
//
// 把 Redis 故障当成 ErrNotAcquired，运维看到的是"另一个实例在跑"——
// 而实际上没有任何实例在跑，任务已经完全停摆。
func TestRedisFailureIsNotMistakenForContention(t *testing.T) {
	t.Parallel()

	lease := newFakeLease(t, &fakeLeaseClient{
		evalResults: []leaseEvalResult{{err: errors.New("connection refused")}},
	})

	_, err := lease.Acquire(context.Background())
	if err == nil {
		t.Fatal("Acquire succeeded against an unreachable Redis")
	}
	if errors.Is(err, ErrNotAcquired) {
		t.Fatal("an unreachable Redis was reported as contention: operators would see " +
			"\"another instance has it\" while in fact nothing is running at all")
	}
}

// TestNoScriptTriggersReloadAndRetry 钉住 Redis 重启后的自愈。
func TestNoScriptTriggersReloadAndRetry(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{
		evalResults: []leaseEvalResult{
			{err: errors.New("NOSCRIPT No matching script")},
			{val: []any{int64(1), int64(1)}},
		},
	}
	lease := newFakeLease(t, client)
	loadsBefore := client.loadCount()

	if _, err := lease.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire after NOSCRIPT: %v", err)
	}
	if client.loadCount() <= loadsBefore {
		t.Error("the script was not reloaded after NOSCRIPT: one Redis restart would stall " +
			"the task until the next deploy")
	}
}

// TestReloadFailureIsReported 钉住重载失败不被吞掉。
func TestReloadFailureIsReported(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{
		evalResults: []leaseEvalResult{{err: errors.New("NOSCRIPT No matching script")}},
	}
	lease := newFakeLease(t, client)
	client.mu.Lock()
	client.loadErr = errors.New("redis went away")
	client.mu.Unlock()

	if _, err := lease.Acquire(context.Background()); err == nil {
		t.Fatal("Acquire succeeded although the script could not be reloaded")
	}
}

// TestKeysShareAHashTagWithoutRedis 在不碰网络的情况下钉住集群语义。
//
// 与 lease_test.go 里的同名断言重复是有意的：那一条要绑回环端口，
// 受限环境会跳过——而集群 slot 是**只在生产才暴露**的问题，
// 恰恰不能依赖一条可能被跳过的测试来守。
func TestKeysShareAHashTagWithoutRedis(t *testing.T) {
	t.Parallel()

	lease := newFakeLease(t, &fakeLeaseClient{})

	lockTag := tagOf(t, lease.lockKey())
	fenceTag := tagOf(t, lease.fenceKey())
	if lockTag != fenceTag {
		t.Fatalf("lock key %q and fence key %q land in different cluster slots: "+
			"a Redis Cluster would reject the acquire script outright",
			lease.lockKey(), lease.fenceKey())
	}
}

func tagOf(t *testing.T, key string) string {
	t.Helper()
	open := strings.Index(key, "{")
	closeIdx := strings.Index(key, "}")
	if open < 0 || closeIdx < open {
		t.Fatalf("key %q has no cluster hash tag", key)
	}
	return key[open+1 : closeIdx]
}

// TestTokensAreUnpredictable 钉住 token 不可预测。
//
// token 是"这把锁是我的"的唯一证据。可预测意味着别的实例能构造出它，
// 从而删掉或续期不属于自己的锁。
func TestTokensAreUnpredictable(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{evalResults: []leaseEvalResult{{val: []any{int64(1), int64(1)}}}}
	lease := newFakeLease(t, client)

	seen := make(map[string]struct{}, 64)
	for range 64 {
		held, err := lease.Acquire(context.Background())
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		if _, dup := seen[held.Token]; dup {
			t.Fatalf("Acquire produced a duplicate token %q", held.Token)
		}
		seen[held.Token] = struct{}{}
	}
}

// TestRunnerConfigIsValidatedWithoutRedis 覆盖 Runner 的注册期校验。
//
// 同样不碰网络：Guarantee 的 fail-fast 是本包最重要的约束之一，
// 不该只由一条会被跳过的测试来守。
func TestRunnerConfigIsValidatedWithoutRedis(t *testing.T) {
	t.Parallel()

	lease := newFakeLease(t, &fakeLeaseClient{})
	task := func(context.Context, Held) error { return nil }

	tests := []struct {
		name      string
		guarantee Guarantee
		interval  time.Duration
		wantErr   bool
	}{
		{name: "idempotent passes", guarantee: GuaranteeIdempotent, interval: time.Minute},
		{name: "fenced passes", guarantee: GuaranteeFenced, interval: time.Minute},
		{name: "zero guarantee rejected", guarantee: GuaranteeUnspecified, interval: time.Minute, wantErr: true},
		{name: "unknown guarantee rejected", guarantee: Guarantee(42), interval: time.Minute, wantErr: true},
		{name: "zero interval rejected", guarantee: GuaranteeIdempotent, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner, err := NewRunner(lease, task, Config{
				Interval:  tt.interval,
				Guarantee: tt.guarantee,
			})
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("NewRunner error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if runner.Guarantee() != tt.guarantee {
				t.Errorf("Guarantee() = %v, want %v", runner.Guarantee(), tt.guarantee)
			}
			if runner.Name() != "job" {
				t.Errorf("Name() = %q, want \"job\"", runner.Name())
			}
		})
	}
}

// TestRenewIntervalIsAThirdOfTheLease 钉住续租周期留出重试余量。
//
// 取租期的 1/2 的话，一次续租失败就只剩一次机会——而网络抖动经常成对出现。
func TestRenewIntervalIsAThirdOfTheLease(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{}
	lease, err := NewLease(context.Background(), client, "test", "job", 90*time.Second)
	if err != nil {
		t.Fatalf("NewLease: %v", err)
	}

	runner, err := NewRunner(lease, func(context.Context, Held) error { return nil },
		Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	if runner.renewInterval != 30*time.Second {
		t.Fatalf("renew interval = %s for a 90s lease, want 30s (a third, so that two "+
			"consecutive renew failures still fit inside the lease)", runner.renewInterval)
	}
}

// TestIsNoScriptMatchesOnlyTheRealThing 钉住 NOSCRIPT 判定不误伤。
func TestIsNoScriptMatchesOnlyTheRealThing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil},
		{name: "real noscript", err: errors.New("NOSCRIPT No matching script"), want: true},
		{name: "unrelated", err: errors.New("connection refused")},
		{name: "mentions it later", err: errors.New("failed: NOSCRIPT")},
		{name: "too short", err: errors.New("NO")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isNoScript(tt.err); got != tt.want {
				t.Fatalf("isNoScript(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestRunnerExecutesTheTaskWithoutRedis 用替身驱动一次完整的执行循环。
//
// Runner 的逻辑（抢锁 → 跑任务 → 释放）不依赖真实 Redis，只依赖脚本的
// 返回值。用替身覆盖它，让这条路径在受限环境也能被验证——而它正是
// 整个包最核心的控制流。
func TestRunnerExecutesTheTaskWithoutRedis(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{
		evalResults: []leaseEvalResult{{val: []any{int64(1), int64(9)}}},
	}
	lease := newFakeLease(t, client)

	var gotFence int64
	observer := &recordingObserver{}
	runner, err := NewRunner(lease, func(_ context.Context, held Held) error {
		gotFence = held.Fence
		return nil
	}, Config{
		Interval:  time.Hour,
		Guarantee: GuaranteeIdempotent,
		Observer:  observer,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	runner.runOnce(context.Background())

	if gotFence != 9 {
		t.Fatalf("the task received fence %d, want 9", gotFence)
	}
	acquired, _, _, failed := observer.counts()
	if acquired != 1 {
		t.Errorf("lease acquisitions = %d, want 1", acquired)
	}
	if failed != 0 {
		t.Errorf("task failures = %d, want 0", failed)
	}
	if observer.successCount() != 1 {
		t.Errorf("task successes = %d, want 1: without a success event the metric has no "+
			"denominator", observer.successCount())
	}
}

// TestRunnerSkipsWhenAnotherInstanceHoldsTheLease 覆盖跳过路径。
func TestRunnerSkipsWhenAnotherInstanceHoldsTheLease(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{
		evalResults: []leaseEvalResult{{val: []any{int64(0), int64(0)}}},
	}
	lease := newFakeLease(t, client)

	var ran bool
	observer := &recordingObserver{}
	runner, err := NewRunner(lease, func(context.Context, Held) error {
		ran = true
		return nil
	}, Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent, Observer: observer})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	runner.runOnce(context.Background())

	if ran {
		t.Fatal("the task ran although another instance holds the lease")
	}
	if _, skipped, _, _ := observer.counts(); skipped != 1 {
		t.Errorf("skip events = %d, want 1: skipping is normal and must be observable", skipped)
	}
}

// TestRunnerDoesNotRunWhenRedisIsDown 钉住故障时不执行。
//
// 拿不到租约就执行，等于所有副本同时跑——恰好是这个机制存在的原因。
func TestRunnerDoesNotRunWhenRedisIsDown(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{
		evalResults: []leaseEvalResult{{err: errors.New("connection refused")}},
	}
	lease := newFakeLease(t, client)

	var ran bool
	observer := &recordingObserver{}
	runner, err := NewRunner(lease, func(context.Context, Held) error {
		ran = true
		return nil
	}, Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent, Observer: observer})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	runner.runOnce(context.Background())

	if ran {
		t.Fatal("the task ran without holding a lease: with Redis down every replica " +
			"would execute concurrently, which is the very thing the lease prevents")
	}
	if _, _, lost, _ := observer.counts(); lost != 1 {
		t.Errorf("lease-lost events = %d, want 1", lost)
	}
}

// TestCanceledTaskIsNotCountedAsAFailure 钉住取消不计失败。
//
// 取消的原因要么是关停、要么是失去租约（后者已由 LeaseLost 报过）。
// 在这里再报一次会让同一个事件在指标上计两次，失败率虚高一倍。
func TestCanceledTaskIsNotCountedAsAFailure(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{
		evalResults: []leaseEvalResult{{val: []any{int64(1), int64(1)}}},
	}
	lease := newFakeLease(t, client)

	observer := &recordingObserver{}
	runner, err := NewRunner(lease, func(context.Context, Held) error {
		return context.Canceled
	}, Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent, Observer: observer})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	runner.runOnce(context.Background())

	if _, _, _, failed := observer.counts(); failed != 0 {
		t.Fatalf("task failures = %d, want 0: a cancellation is not a task failure, and "+
			"counting it doubles the observed failure rate", failed)
	}
	if observer.successCount() != 0 {
		t.Errorf("task successes = %d, want 0: a canceled run did not succeed either",
			observer.successCount())
	}
}

// TestReleaseUsesTheLockKeyAndToken 覆盖释放的参数形状。
func TestReleaseUsesTheLockKeyAndToken(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{evalResults: []leaseEvalResult{{val: int64(1)}}}
	lease := newFakeLease(t, client)

	if err := lease.Release(context.Background(), Held{Token: "my-token"}); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if len(client.evalKeys) != 1 || client.evalKeys[0][0] != lease.lockKey() {
		t.Fatalf("release keys = %v, want the lock key", client.evalKeys)
	}
	// token 必须传下去：不传的话脚本无从比对，就退化成无条件 DEL。
	if len(client.evalArgs[0]) != 1 || client.evalArgs[0][0] != "my-token" {
		t.Fatalf("release args = %v, want just the token", client.evalArgs[0])
	}
}

// TestReleaseReportsRedisFailure 钉住释放失败不被吞掉。
func TestReleaseReportsRedisFailure(t *testing.T) {
	t.Parallel()

	lease := newFakeLease(t, &fakeLeaseClient{
		evalResults: []leaseEvalResult{{err: errors.New("connection refused")}},
	})
	if err := lease.Release(context.Background(), Held{Token: "t"}); err == nil {
		t.Fatal("Release reported success against an unreachable Redis")
	}
}

// TestRenewReportsRedisFailureRatherThanLeaseLost 钉住故障与失锁可区分。
//
// 两者都会导致中止（安全方向一致），但日志与指标上必须分得开：
// "锁被别人抢走"说明有实例竞争，"Redis 连不上"说明基础设施出了问题，
// 而排查这两件事要去看完全不同的地方。
func TestRenewReportsRedisFailureRatherThanLeaseLost(t *testing.T) {
	t.Parallel()

	lease := newFakeLease(t, &fakeLeaseClient{
		evalResults: []leaseEvalResult{{err: errors.New("connection refused")}},
	})
	err := lease.Renew(context.Background(), Held{Token: "t"})
	if err == nil {
		t.Fatal("Renew succeeded against an unreachable Redis")
	}
	if errors.Is(err, ErrLeaseLost) {
		t.Fatal("an unreachable Redis was reported as a lost lease: the two need different " +
			"investigations, and conflating them sends the operator to the wrong place")
	}
}

// TestRunnerRunReturnsNilOnShutdown 覆盖关停路径。
//
// 返回错误的话，WorkerManager 会把一次正常关停当成 worker 异常退出。
func TestRunnerRunReturnsNilOnShutdown(t *testing.T) {
	t.Parallel()

	client := &fakeLeaseClient{evalResults: []leaseEvalResult{{val: []any{int64(0), int64(0)}}}}
	lease := newFakeLease(t, client)

	runner, err := NewRunner(lease, func(context.Context, Held) error { return nil },
		Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned %v on shutdown, want nil: the manager would treat a normal "+
			"stop as an unexpected exit", err)
	}
}

// TestMaxRuntimeDefaultsToTenLeases 覆盖默认执行上限。
func TestMaxRuntimeDefaultsToTenLeases(t *testing.T) {
	t.Parallel()

	lease := newFakeLease(t, &fakeLeaseClient{})
	runner, err := NewRunner(lease, func(context.Context, Held) error { return nil },
		Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	if runner.maxRuntime != 10*time.Minute {
		t.Fatalf("default max runtime = %s for a 1m lease, want 10m", runner.maxRuntime)
	}
}

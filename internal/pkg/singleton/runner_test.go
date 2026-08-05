package singleton

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/testutil"
)

// recordingObserver 记录 Runner 的事件。
type recordingObserver struct {
	mu        sync.Mutex
	acquired  int
	skipped   int
	lost      []error
	failed    []error
	succeeded int
}

func (o *recordingObserver) LeaseAcquired(string, int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.acquired++
}

func (o *recordingObserver) LeaseSkipped(string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.skipped++
}

func (o *recordingObserver) LeaseLost(_ string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lost = append(o.lost, err)
}

func (o *recordingObserver) TaskSucceeded(string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.succeeded++
}

func (o *recordingObserver) TaskFailed(_ string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failed = append(o.failed, err)
}

func (o *recordingObserver) successCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.succeeded
}

func (o *recordingObserver) counts() (acquired, skipped, lost, failed int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.acquired, o.skipped, len(o.lost), len(o.failed)
}

// TestGuaranteeMustBeDeclared 是注册期 fail-fast 的负向证明。
//
// 锁不是正确性保证。这个约束强迫作者在注册时就回答"重复执行会怎样"，
// 而不是把问题留到事故现场。零值被接受的话，一个非幂等任务会悄悄
// 失去保护，而代码看上去毫无异常。
func TestGuaranteeMustBeDeclared(t *testing.T) {
	t.Parallel()

	lease, _ := newTestLease(t, time.Minute)
	task := func(context.Context, Held) error { return nil }

	tests := []struct {
		name      string
		guarantee Guarantee
		wantErr   bool
		// wantHints 是错误里必须出现的字样：只说"非法"不能让人知道该怎么办。
		wantHints []string
	}{
		{name: "idempotent passes", guarantee: GuaranteeIdempotent},
		{name: "fenced passes", guarantee: GuaranteeFenced},
		{
			name: "zero value is rejected", guarantee: GuaranteeUnspecified, wantErr: true,
			wantHints: []string{"GuaranteeIdempotent", "GuaranteeFenced", "not a correctness guarantee"},
		},
		{name: "unknown value is rejected", guarantee: Guarantee(99), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewRunner(lease, task, Config{
				Interval:  time.Minute,
				Guarantee: tt.guarantee,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("guarantee %v was accepted, want it rejected", tt.guarantee)
				}
				for _, hint := range tt.wantHints {
					if !strings.Contains(err.Error(), hint) {
						t.Errorf("error %q does not mention %q", err, hint)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("guarantee %v rejected: %v", tt.guarantee, err)
			}
		})
	}
}

func TestNewRunnerValidatesItsInputs(t *testing.T) {
	t.Parallel()

	lease, _ := newTestLease(t, time.Minute)
	task := func(context.Context, Held) error { return nil }

	tests := []struct {
		name     string
		lease    *Lease
		task     TaskFunc
		interval time.Duration
	}{
		{name: "no lease", lease: nil, task: task, interval: time.Minute},
		{name: "no task", lease: lease, task: nil, interval: time.Minute},
		{name: "zero interval", lease: lease, task: task, interval: 0},
		{name: "negative interval", lease: lease, task: task, interval: -time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewRunner(tt.lease, tt.task, Config{
				Interval:  tt.interval,
				Guarantee: GuaranteeIdempotent,
			})
			if err == nil {
				t.Fatal("NewRunner accepted an invalid input")
			}
		})
	}
}

// TestTaskRunsImmediatelyOnStart 钉住启动后先跑一轮。
//
// 等第一个 tick 的话，一个每天跑一次的任务在重启后要等一整天——
// 而重启往往正是因为出了问题需要它尽快跑。
func TestTaskRunsImmediatelyOnStart(t *testing.T) {
	t.Parallel()

	lease, _ := newTestLease(t, time.Minute)

	ran := make(chan Held, 1)
	runner, err := NewRunner(lease, func(_ context.Context, held Held) error {
		select {
		case ran <- held:
		default:
		}
		return nil
	}, Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()

	select {
	case held := <-ran:
		if held.Fence <= 0 {
			t.Errorf("the task received fence %d, want a positive sequence", held.Fence)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the task did not run on start: after a restart it would idle until the " +
			"first tick, which for a daily job means a full day")
	}
}

// TestLosingTheLeaseCancelsTheTask 是本包最重要的一条。
//
// 失去租约后继续执行没有任何独占保证——另一个实例此刻可能正在跑同一个任务。
// 任务 ctx 必须立刻取消，而不是"再观察一次"。
func TestLosingTheLeaseCancelsTheTask(t *testing.T) {
	t.Parallel()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// 短租期让续租周期落在几十毫秒量级，测试因此不必等太久。
	lease, err := NewLease(context.Background(), client, "test", "job", 150*time.Millisecond)
	if err != nil {
		t.Fatalf("NewLease: %v", err)
	}

	started := make(chan struct{})
	canceled := make(chan struct{})
	observer := &recordingObserver{}

	runner, err := NewRunner(lease, func(taskCtx context.Context, _ Held) error {
		close(started)
		// 一直跑到 ctx 被取消——正是租约丢失应当触发的事。
		<-taskCtx.Done()
		close(canceled)
		return taskCtx.Err()
	}, Config{
		Interval:  time.Hour,
		Guarantee: GuaranteeIdempotent,
		Observer:  observer,
		// **MaxRuntime 必须远大于本测试的等待时间**，否则超时会替租约丢失
		// 把任务取消掉，测试就无法区分两者——它会在实现根本不检查租约时
		// 照样通过。默认值是租期的 10 倍（这里只有 1.5 秒），刚好落在
		// 等待窗口内，验证了个寂寞。
		MaxRuntime: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the task never started")
	}

	// 把锁从 Redis 里删掉：模拟租约过期后被另一个实例接手。
	// 下一次 compare-and-renew 会发现锁已不属于自己。
	srv.Del(lease.lockKey())

	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("the task kept running after the lease was lost: without that cancellation " +
			"two instances would be executing the same job at once")
	}

	// 事件必须留痕：失去租约是需要有人关注的事，不能静默。
	deadline := time.After(2 * time.Second)
	for {
		if _, _, lost, _ := observer.counts(); lost > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("losing the lease produced no observer event")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestSecondInstanceSkipsInsteadOfRunning 钉住多副本下只有一个在跑。
func TestSecondInstanceSkipsInsteadOfRunning(t *testing.T) {
	t.Parallel()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// 两个 Runner 共用一个 Redis，模拟两个副本。
	makeRunner := func(t *testing.T, obs Observer, task TaskFunc) *Runner {
		t.Helper()
		lease, err := NewLease(context.Background(), client, "test", "job", time.Minute)
		if err != nil {
			t.Fatalf("NewLease: %v", err)
		}
		runner, err := NewRunner(lease, task, Config{
			Interval:  time.Hour,
			Guarantee: GuaranteeIdempotent,
			Observer:  obs,
		})
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		return runner
	}

	// 第一个实例拿到租约后阻塞住，保证第二个来抢时它还持有。
	holding := make(chan struct{})
	release := make(chan struct{})
	firstObs := &recordingObserver{}
	first := makeRunner(t, firstObs, func(context.Context, Held) error {
		close(holding)
		<-release
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = first.Run(ctx) }()

	select {
	case <-holding:
	case <-time.After(3 * time.Second):
		t.Fatal("the first instance never acquired the lease")
	}

	// 第二个实例：必须跳过，而不是并发执行。
	var secondRan bool
	secondObs := &recordingObserver{}
	second := makeRunner(t, secondObs, func(context.Context, Held) error {
		secondRan = true
		return nil
	})
	second.runOnce(context.Background())

	if secondRan {
		t.Fatal("both instances ran the task at the same time: the lease did not exclude " +
			"the second one")
	}
	if _, skipped, _, _ := secondObs.counts(); skipped != 1 {
		t.Errorf("skip events = %d, want 1: a skipped run is normal and must be observable", skipped)
	}

	close(release)
}

// TestTaskFailureIsObservedButDoesNotStopTheRunner 钉住一次失败不终结循环。
//
// 后台任务的一次失败正确的处置是下个周期重试。让 Runner 退出会把一次
// 瞬时故障变成永久停摆，而且没有任何信号。
func TestTaskFailureIsObservedButDoesNotStopTheRunner(t *testing.T) {
	t.Parallel()

	lease, _ := newTestLease(t, time.Minute)
	observer := &recordingObserver{}

	runner, err := NewRunner(lease, func(context.Context, Held) error {
		return errors.New("task blew up")
	}, Config{
		Interval:  50 * time.Millisecond,
		Guarantee: GuaranteeIdempotent,
		Observer:  observer,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned %v, want nil on context cancellation", err)
	}

	acquired, _, _, failed := observer.counts()
	if failed < 2 {
		t.Fatalf("task failures observed = %d, want at least 2: one failure must not stop "+
			"the runner", failed)
	}
	if acquired < 2 {
		t.Errorf("lease acquisitions = %d, want at least 2", acquired)
	}
}

// TestReleaseHappensEvenAfterCancellation 钉住关停后锁被主动释放。
//
// 用已取消的 ctx 去释放会立刻失败，锁只能等租期自然过期——那让下一个
// 实例白等一个租期。释放必须用独立的 ctx。
func TestReleaseHappensEvenAfterCancellation(t *testing.T) {
	t.Parallel()

	lease, _ := newTestLease(t, time.Minute)

	runner, err := NewRunner(lease, func(taskCtx context.Context, _ Held) error {
		<-taskCtx.Done()
		return taskCtx.Err()
	}, Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = runner.Run(ctx)

	// Run 返回后锁必须已经释放：下一个实例应当能立刻抢到。
	if _, err := lease.Acquire(context.Background()); err != nil {
		t.Fatalf("the lock was not released after shutdown (%v): the next instance would "+
			"have to wait out the whole lease term", err)
	}
}

// TestGuaranteeStringIsStable 钉住可作日志字段的稳定名称。
func TestGuaranteeStringIsStable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		guarantee Guarantee
		want      string
	}{
		{GuaranteeIdempotent, "idempotent"},
		{GuaranteeFenced, "fenced"},
		{GuaranteeUnspecified, "unspecified"},
		{Guarantee(99), "invalid"},
	}

	for _, tt := range tests {
		if got := tt.guarantee.String(); got != tt.want {
			t.Errorf("Guarantee(%d).String() = %q, want %q", tt.guarantee, got, tt.want)
		}
	}
}

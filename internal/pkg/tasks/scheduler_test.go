package tasks

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/pkg/singleton"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
)

// schedulerConfig 返回一个启用了调度器的配置。
//
// 租约刻意取得很短（90ms → 续租周期 30ms），让续租相关的测试在毫秒级完成。
// 配置层的下限校验（minSchedulerLeaseTTL）作用于用户配置，不作用于此处直接构造的值。
func schedulerConfig() config.TasksConfig {
	cfg := enabledConfig()
	cfg.Scheduler = config.SchedulerConfig{
		Enabled:       true,
		LeaseName:     "test-scheduler",
		LeaseTTL:      90 * time.Millisecond,
		RetryInterval: 10 * time.Millisecond,
		UniqueTTL:     time.Second,
	}
	return cfg
}

// newTestRedis 起一个进程内 Redis 并返回连上它的客户端。
//
// 声明端口依赖放在这里而不是 10 个调用方那里：miniredis.RunT 起的是**真实的
// TCP 监听**，本函数就是那个直接绑定者。绑不上端口时 RunT 走 t.Fatalf，
// 于是在受限环境里这些测试会**失败**而不是跳过——而受限实跑只在 release-check
// 里执行，那种失败会一路潜伏到发版当天。分散到调用方则是下一个新增调用方再漏一次。
func newTestRedis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()
	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return srv, client
}

func idempotentTask() PeriodicTask {
	return PeriodicTask{
		Spec:      "@every 1h",
		TaskType:  "cleanup:run",
		Payload:   map[string]string{"scope": "all"},
		Guarantee: singleton.GuaranteeIdempotent,
	}
}

// TestPeriodicTaskMustDeclareItsGuarantee 钉住"零值即拒绝"。
//
// 租约不是正确性保证：租约过期而旧 leader 尚未察觉的窗口内，两个实例会同时投递。
// 默认成安全会让一个非幂等任务悄悄失去保护，因此忘记声明必须失败。
func TestPeriodicTaskMustDeclareItsGuarantee(t *testing.T) {
	t.Parallel()

	_, client := newTestRedis(t)

	task := idempotentTask()
	task.Guarantee = singleton.GuaranteeUnspecified

	_, err := NewScheduler(schedulerConfig(), client, "scheduler", SchedulerObserver{}, task)
	if err == nil {
		t.Fatal("未声明 guarantee 的周期任务竟被接受")
	}
	if !strings.Contains(err.Error(), "guarantee must be declared") {
		t.Fatalf("错误 %q 没有指出缺少 guarantee 声明", err)
	}
}

// TestInvalidPeriodicTasksAreRejectedAtConstruction 覆盖其余注册期校验。
func TestInvalidPeriodicTasksAreRejectedAtConstruction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*PeriodicTask)
		want   string
	}{
		{
			name:   "empty spec",
			mutate: func(p *PeriodicTask) { p.Spec = "  " },
			want:   "spec must not be empty",
		},
		{
			name:   "empty task type",
			mutate: func(p *PeriodicTask) { p.TaskType = "" },
			want:   "task type must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, client := newTestRedis(t)
			task := idempotentTask()
			tt.mutate(&task)

			_, err := NewScheduler(schedulerConfig(), client, "scheduler", SchedulerObserver{}, task)
			if err == nil {
				t.Fatal("非法的周期任务竟被接受")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("错误 %q 不含 %q", err, tt.want)
			}
		})
	}
}

// TestDisabledSchedulerBuildsNothing 钉住默认关闭时零构造。
//
// 三种关闭形态都必须返回 nil：队列关、调度器关、没有任何条目。
// 最后一种尤其重要——没有条目的调度器是一个空转的 worker，
// 注册它只会让关停多等一轮、让监控里多一个永远空闲的实例。
func TestDisabledSchedulerBuildsNothing(t *testing.T) {
	t.Parallel()

	queueOff := schedulerConfig()
	queueOff.Enabled = false

	schedulerOff := schedulerConfig()
	schedulerOff.Scheduler.Enabled = false

	tests := []struct {
		name    string
		cfg     config.TasksConfig
		entries []PeriodicTask
	}{
		{name: "queue disabled", cfg: queueOff, entries: []PeriodicTask{idempotentTask()}},
		{name: "scheduler disabled", cfg: schedulerOff, entries: []PeriodicTask{idempotentTask()}},
		{name: "no entries", cfg: schedulerConfig(), entries: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// client 传 nil：关闭态若真去建连接，这里会 panic 或报错。
			scheduler, err := NewScheduler(tt.cfg, nil, "scheduler", SchedulerObserver{}, tt.entries...)
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			if scheduler != nil {
				t.Fatal("关闭态竟构造出了调度器")
			}
		})
	}
}

// TestEnabledSchedulerRequiresAClient 钉住启用态缺客户端即失败。
func TestEnabledSchedulerRequiresAClient(t *testing.T) {
	t.Parallel()

	_, err := NewScheduler(schedulerConfig(), nil, "scheduler", SchedulerObserver{}, idempotentTask())
	if err == nil {
		t.Fatal("启用态缺少 redis 客户端竟未报错")
	}
}

// TestSecondInstanceDoesNotSchedule 钉住"未持有租约的实例不投递"。
//
// **反证**：移除 leadOnce 里的 ErrNotAcquired 判定，本用例必须失败——
// 那正是多副本下重复投递的根因。
func TestSecondInstanceDoesNotSchedule(t *testing.T) {
	t.Parallel()

	_, client := newTestRedis(t)
	cfg := schedulerConfig()

	first, err := NewScheduler(cfg, client, "first", SchedulerObserver{}, idempotentTask())
	if err != nil {
		t.Fatalf("NewScheduler(first): %v", err)
	}

	var skipped []string
	var mu sync.Mutex
	second, err := NewScheduler(cfg, client, "second", SchedulerObserver{
		LeaseSkipped: func(name string) {
			mu.Lock()
			defer mu.Unlock()
			skipped = append(skipped, name)
		},
	}, idempotentTask())
	if err != nil {
		t.Fatalf("NewScheduler(second): %v", err)
	}

	ctx := context.Background()

	// 第一个实例占住租约。
	held, err := first.lease.Acquire(ctx)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	t.Cleanup(func() { _ = first.lease.Release(ctx, held) })

	// 第二个实例这一轮必须直接跳过，不构造调度器、不投递任何任务。
	acquired, err := second.leadOnce(ctx)
	if err != nil {
		t.Fatalf("second leadOnce: %v", err)
	}
	if acquired {
		t.Fatal("租约已被占用，第二个实例竟然成为了 leader")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(skipped) != 1 || skipped[0] != "second" {
		t.Fatalf("LeaseSkipped 事件 = %v，want [second]", skipped)
	}
}

// TestLosingTheLeaseStopsScheduling 钉住"续租失败即停止调度"。
//
// **反证**：移除 keepAlive 里的 cancel()，本用例必须超时失败——
// 那意味着失去租约的实例仍在调度，与新 leader 同时投递。
func TestLosingTheLeaseStopsScheduling(t *testing.T) {
	t.Parallel()

	srv, client := newTestRedis(t)
	cfg := schedulerConfig()

	var lostErr error
	var mu sync.Mutex
	done := make(chan struct{})
	scheduler, err := NewScheduler(cfg, client, "scheduler", SchedulerObserver{
		LeaseLost: func(_ string, err error) {
			mu.Lock()
			lostErr = err
			mu.Unlock()
			close(done)
		},
	}, idempotentTask())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	ctx := context.Background()
	held, err := scheduler.lease.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	leadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go scheduler.keepAlive(leadCtx, cancel, held)

	// 让 redis 不可用：续租必然失败。
	srv.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("续租失败后未上报 LeaseLost —— 失去租约的实例仍会继续调度")
	}

	select {
	case <-leadCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("续租失败后未取消 leadCtx —— 调度器不会停止")
	}

	mu.Lock()
	defer mu.Unlock()
	if lostErr == nil {
		t.Fatal("LeaseLost 事件没有携带错误原因")
	}
}

// TestRunStopsOnContextCancel 钉住选主循环遵守 ctx 取消。
//
// LifecycleLongRunning 的 worker 在 ctx 取消之前返回一律被视为异常退出，
// 因此这里同时验证"取消后确实返回 nil"。
func TestRunStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	_, client := newTestRedis(t)

	// 用另一个实例占住租约，让被测实例停在"抢不到锁"的重试路径上——
	// 这条路径不构造底层调度器，测试因此不依赖队列后端的命令支持。
	holder, err := NewScheduler(schedulerConfig(), client, "holder", SchedulerObserver{}, idempotentTask())
	if err != nil {
		t.Fatalf("NewScheduler(holder): %v", err)
	}
	held, err := holder.lease.Acquire(context.Background())
	if err != nil {
		t.Fatalf("holder Acquire: %v", err)
	}
	t.Cleanup(func() { _ = holder.lease.Release(context.Background(), held) })

	scheduler, err := NewScheduler(schedulerConfig(), client, "scheduler", SchedulerObserver{}, idempotentTask())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- scheduler.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("Run 在 ctx 取消后返回 %v，want nil", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未在 ctx 取消后返回")
	}
}

// TestSchedulerCleanupIsIdempotent 钉住 Cleanup 的幂等契约。
//
// worker 管理器保证 Prepare 成功后 Cleanup 恰好被调用一次，
// 但它经三条路径调用（启动回滚、正常关停、OneShot 完成），实现必须容忍重复。
func TestSchedulerCleanupIsIdempotent(t *testing.T) {
	t.Parallel()

	_, client := newTestRedis(t)
	scheduler, err := NewScheduler(schedulerConfig(), client, "scheduler", SchedulerObserver{}, idempotentTask())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	ctx := context.Background()
	for i := range 3 {
		if err := scheduler.Cleanup(ctx); err != nil {
			t.Fatalf("第 %d 次 Cleanup: %v", i+1, err)
		}
	}
}

// TestUnspecifiedGuaranteeErrorNamesTheTask 保证错误信息能定位到具体任务。
//
// 一次注册多条周期任务时，"某个任务没声明 guarantee"若不指出是哪一个，
// 排查要靠逐条比对。
func TestUnspecifiedGuaranteeErrorNamesTheTask(t *testing.T) {
	t.Parallel()

	bad := PeriodicTask{Spec: "@every 1h", TaskType: "report:daily"}
	err := bad.validate()
	if err == nil {
		t.Fatal("未声明 guarantee 竟通过校验")
	}
	if !strings.Contains(err.Error(), "report:daily") {
		t.Fatalf("错误 %q 没有指出是哪个任务", err)
	}
}

// TestLeaseIsReleasedOnShutdown 钉住"关停时主动释放租约"。
//
// 主动释放让接管的实例立即可用，而不是等一个完整的租期——
// 那段时间里没有任何实例在调度，而租期越长这个空窗越久。
func TestLeaseIsReleasedOnShutdown(t *testing.T) {
	t.Parallel()

	srv, client := newTestRedis(t)
	cfg := schedulerConfig()

	acquired := make(chan struct{})
	var once sync.Once
	scheduler, err := NewScheduler(cfg, client, "scheduler", SchedulerObserver{
		LeaseAcquired: func(string, int64) { once.Do(func() { close(acquired) }) },
	}, idempotentTask())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- scheduler.Run(ctx) }()

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("调度器未能获取租约")
	}

	lockKey := "tasks:singleton:{" + cfg.Scheduler.EffectiveLeaseName() + "}:lock"
	if !srv.Exists(lockKey) {
		cancel()
		t.Fatalf("成为 leader 后锁键 %q 不存在", lockKey)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 ctx 取消后返回")
	}

	if srv.Exists(lockKey) {
		t.Fatal("关停后租约未被释放 —— 接管的实例要白等一个完整租期")
	}
}

// TestPrepareRejectsAnInvalidSpec 钉住"非法 cron 表达式在启动期就失败"。
//
// 这是 Prepare 存在的理由：spec 写错（"0 3 * * *" 少写一段、拼错描述符）
// 若留到运行期才发现，表现是这个任务**永远不触发**——
// 而没有任何人在等一个不存在的信号，故障可以静默存在几个月。
func TestPrepareRejectsAnInvalidSpec(t *testing.T) {
	t.Parallel()

	_, client := newTestRedis(t)

	bad := idempotentTask()
	bad.Spec = "not-a-cron-expression"

	scheduler, err := NewScheduler(schedulerConfig(), client, "scheduler", SchedulerObserver{}, bad)
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	err = scheduler.Prepare(context.Background())
	if err == nil {
		t.Fatal("非法 cron 表达式竟通过了 Prepare —— 该任务会永远不触发且无人知晓")
	}
	if !strings.Contains(err.Error(), bad.TaskType) {
		t.Fatalf("错误 %q 没有指出是哪个任务", err)
	}
}

// TestPrepareAcceptsAValidSpec 保证上面的用例不是"Prepare 永远失败"。
//
// 只断言失败不足以证明判据有效——一条永远失败的校验也能通过那个断言。
func TestPrepareAcceptsAValidSpec(t *testing.T) {
	t.Parallel()

	_, client := newTestRedis(t)

	scheduler, err := NewScheduler(schedulerConfig(), client, "scheduler", SchedulerObserver{}, idempotentTask())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	if err := scheduler.Prepare(context.Background()); err != nil {
		t.Fatalf("合法配置的 Prepare 失败: %v", err)
	}
}

// TestPrepareFailsWhenRedisIsUnreachable 钉住"租约能力在启动期实测"。
//
// 只 PING 不能证明当前 ACL 能执行脚本，也不能证明锁键与 fence 键在同一个集群 slot，
// 因此 Prepare 用随机探测键真跑一遍抢锁/续租/释放。
func TestPrepareFailsWhenRedisIsUnreachable(t *testing.T) {
	t.Parallel()

	srv, client := newTestRedis(t)
	scheduler, err := NewScheduler(schedulerConfig(), client, "scheduler", SchedulerObserver{}, idempotentTask())
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}

	srv.Close()

	if err := scheduler.Prepare(context.Background()); err == nil {
		t.Fatal("Redis 不可用时 Prepare 竟然成功 —— 启动失败被推迟到了运行期")
	}
}

package bootstrap

// TASKQUEUE: 可选接线的回归测试。启用 taskqueue.go 和配置接线后，
// 删除下面包围测试代码的 /* 和 */，执行本包测试。
/*
import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/taskqueue"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

// TestSchedulerWorkerIsAProducer 钉住调度器的关停分类。
//
// 分类决定关停顺序，而顺序是实质的：调度器是任务生产者，必须**最先**停。
// 若它被当成 consumer，关停时它会在 HTTP 之后才停——那段时间里它仍在投递，
// 而消费者正在 drain，新投递的任务这一轮没人处理。
//
// Lifecycle 同样是声明而非推断：调度器的 Run 持续调度，在 ctx 取消之前返回
// （包括返回 nil）意味着所有周期任务都停了，而进程看起来完全正常——
// 声明为 LongRunning 才能让管理器把那种退出上报为异常。
func TestSchedulerWorkerIsAProducer(t *testing.T) {
	t.Parallel()

	w := &asynqSchedulerWorker{}

	if got := w.Kind(); got != KindProducer {
		t.Fatalf("Kind() = %v, want KindProducer（生产者必须最先停）", got)
	}
	if got := w.Lifecycle(); got != LifecycleLongRunning {
		t.Fatalf("Lifecycle() = %v, want LifecycleLongRunning", got)
	}
	if w.Required() {
		t.Fatal("Required() = true：调度器不可用不应让整个实例被摘流，周期任务本就允许迟到")
	}
}

// TestNilSchedulerIsNotRegistered 钉住"未启用时不注册 worker"。
//
// 构造函数在未启用时返回 nil 调度器，adapter 必须据此返回 nil——
// 否则 worker 集合里会多出一个包着 nil 的 worker，它会在 Prepare 时 panic。
func TestNilSchedulerIsNotRegistered(t *testing.T) {
	t.Parallel()

	if w := newAsynqSchedulerWorker(nil); w != nil {
		t.Fatal("nil 调度器竟被包装成了 worker")
	}
}

func TestSchedulerDrainTimeoutPreservesSharedResources(t *testing.T) {
	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr(), ReadTimeout: 5 * time.Second})
	var resourceCloses, extensionCloses atomic.Int32
	t.Cleanup(func() {
		if resourceCloses.Load() == 0 {
			if err := client.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	// Hold one real queue script response while the cron job is in flight.
	srv.Server().SetPreHook(func(_ *server.Peer, command string, args ...string) bool {
		if strings.EqualFold(command, "evalsha") || strings.EqualFold(command, "eval") {
			for _, arg := range args[2:] {
				if strings.HasPrefix(arg, "asynq:") && blocked.CompareAndSwap(false, true) {
					close(entered)
					<-release
					break
				}
			}
		}
		return false
	})
	scheduler, err := taskqueue.NewScheduler(taskqueue.Config{
		Enabled: true, ShutdownTimeout: 20 * time.Millisecond,
		Scheduler: taskqueue.SchedulerConfig{Enabled: true},
	}, client, "blocked-scheduler", taskqueue.PeriodicTask{Spec: "@every 1s", TaskType: "test:drain"})
	if err != nil {
		t.Fatal(err)
	}
	budget := testWorkerBudget()
	// A premature Run return must not be hidden by a longer fallback Cleanup wait.
	budget.Cleanup = 10 * time.Millisecond
	manager, err := NewWorkerManager(budget, newAsynqSchedulerWorker(scheduler))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager.runMu.Lock()
	done := manager.running[KindProducer][0].done
	manager.runMu.Unlock()
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := manager.StopAll(ctx); err != nil {
			t.Error(err)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("scheduler goroutine did not exit after releasing the test server")
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("periodic enqueue did not reach the controlled TCP server")
	}
	stopCtx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	stopErr := manager.StopKind(stopCtx, KindProducer)
	cancel()
	if !errors.Is(stopErr, ErrWorkerNotExited) {
		t.Fatalf("StopKind prematurely confirmed scheduler exit: %v", stopErr)
	}
	owned := &lifecycleComponent{stop: func(context.Context) error { resourceCloses.Add(1); return client.Close() }}
	cleanup := &providerCleanup{}
	cleanup.push("shared extension", cleanupExtensions, func(context.Context) error { extensionCloses.Add(1); return nil })
	app := &App{workers: manager, cleanup: cleanup, runtime: &Runtime{owners: &runtimeResources{redis: owned}}}
	if err := app.closeExtensions(t.Context()); !errors.Is(err, ErrWorkerNotExited) || extensionCloses.Load() != 0 {
		t.Fatalf("extension released during scheduler drain: err=%v closes=%d", err, extensionCloses.Load())
	}
	if err := app.closeResources(t.Context()); !errors.Is(err, ErrWorkerNotExited) || resourceCloses.Load() != 0 {
		t.Fatalf("shared Redis released during scheduler drain: err=%v closes=%d", err, resourceCloses.Load())
	}
	if err := manager.CleanupRemaining(t.Context()); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("late scheduler exit was not collected")
	}
	if len(manager.LiveWorkers()) != 0 {
		t.Fatalf("late exit tracking: live=%v", manager.LiveWorkers())
	}
	if err := app.closeExtensions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.closeResources(t.Context()); err != nil {
		t.Fatal(err)
	}
	if resourceCloses.Load() != 1 || extensionCloses.Load() != 1 {
		t.Fatalf("late cleanup counts: resources=%d extensions=%d", resourceCloses.Load(), extensionCloses.Load())
	}
}

// TestRedisRequiredForTaskQueue 钉住任务队列对 Redis 必需性的贡献。
//
// 漏掉这一行的后果不是"Redis 挂了任务不跑"，而是**进程起不来且错误指错方向**：
// 推导为 optional → Redis 已配置但启动期不可达时 res.redis 保持 nil →
// Rdb() 返回 nil → taskqueue.NewProducer 报 "producer requires a redis client"
// → 启动失败。运维看到的那句话读起来像配置缺失，而真实原因（Redis 不可达）
// 不出现在错误里任何位置。
//
// 这条判据同时覆盖调度器：它向 Redis 队列投递，且以 tasks.enabled 为前提。
func TestRedisRequiredForTaskQueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "队列启用即 required",
			cfg:  &config.Config{Tasks: taskqueue.Config{Enabled: true}},
			want: true,
		},
		{
			name: "队列启用且开了调度器仍为 required",
			cfg: &config.Config{Tasks: taskqueue.Config{
				Enabled:   true,
				Scheduler: taskqueue.SchedulerConfig{Enabled: true},
			}},
			want: true,
		},
		{
			// 反向锚：缺了它，一个无条件返回 true 的实现也能让上面两条通过，
			// 而那会让不用队列的部署因为一个用不到的依赖而起不来。
			name: "队列关闭不抬高必需性",
			cfg:  &config.Config{Tasks: taskqueue.Config{Enabled: false}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RedisRequired(tt.cfg); got != tt.want {
				t.Fatalf("redisRequired = %v, want %v", got, tt.want)
			}
		})
	}
}

*/

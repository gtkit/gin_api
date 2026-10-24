package bootstrap

import "testing"

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

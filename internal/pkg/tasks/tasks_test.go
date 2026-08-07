package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gtkit/asynqx"
)

func enabledConfig() config.TasksConfig {
	return config.TasksConfig{Enabled: true, Concurrency: 2}
}

// TestDisabledProducerRefusesInsteadOfDropping 钉住关闭态不静默丢弃。
//
// 静默丢弃是这类能力最难查的失效形态：调用方以为任务排上了、实际什么也没发生，
// 而且没有任何日志、指标或错误能指向它。
func TestDisabledProducerRefusesInsteadOfDropping(t *testing.T) {
	t.Parallel()

	producer, err := NewProducer(config.TasksConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	if producer.Enabled() {
		t.Fatal("关闭态报告为启用")
	}

	err = producer.Enqueue(context.Background(), "email:send", map[string]string{"to": "x"})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Enqueue error = %v, want ErrDisabled", err)
	}
	if !strings.Contains(err.Error(), "email:send") {
		t.Fatalf("错误 %q 没有指出是哪个任务类型", err)
	}

	// 关闭态与 nil 的收尾都必须无害，且可重复。
	if err := producer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := producer.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	var nilProducer *Producer
	if nilProducer.Enabled() {
		t.Fatal("nil Producer 报告为启用")
	}
	if err := nilProducer.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
	if err := nilProducer.Enqueue(context.Background(), "t", nil); !errors.Is(err, ErrDisabled) {
		t.Fatalf("nil Enqueue error = %v, want ErrDisabled", err)
	}
}

// TestEnabledProducerRequiresClient 钉住启用时缺客户端即构造失败。
//
// 让它构造成功、运行期再失败，等于把一个装配错误推迟到线上第一次入队。
func TestEnabledProducerRequiresClient(t *testing.T) {
	t.Parallel()

	if _, err := NewProducer(enabledConfig(), nil); err == nil {
		t.Fatal("启用态缺 redis 客户端时构造仍然成功了")
	}
}

// TestConsumerNotRegisteredWhenPointless 钉住两种"不该注册消费者"的情况。
//
// 一个没有处理器的消费者只会让关停多等一轮、让监控里多一个永远空闲的实例。
func TestConsumerNotRegisteredWhenPointless(t *testing.T) {
	t.Parallel()

	noop := func(*asynqx.Worker) error { return nil }

	tests := []struct {
		name     string
		cfg      config.TasksConfig
		handlers []Registrar
	}{
		{name: "队列未启用", cfg: config.TasksConfig{Enabled: false}, handlers: []Registrar{noop}},
		{name: "没有处理器", cfg: enabledConfig(), handlers: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NewConsumer(tt.cfg, nil, "c", tt.handlers...); got != nil {
				t.Fatal("不该注册消费者的情况下仍返回了实例")
			}
		})
	}
}

// TestConsumerPrepareRequiresClient 钉住 Prepare 是启动期的失败点。
func TestConsumerPrepareRequiresClient(t *testing.T) {
	t.Parallel()

	consumer := NewConsumer(enabledConfig(), nil, "c", func(*asynqx.Worker) error { return nil })
	if consumer == nil {
		t.Fatal("NewConsumer 返回 nil")
	}
	if err := consumer.Prepare(context.Background()); err == nil {
		t.Fatal("缺 redis 客户端时 Prepare 仍然成功了")
	}
	// Run 在 Prepare 未成功时必须报错，而不是空转。
	if err := consumer.Run(context.Background()); err == nil {
		t.Fatal("Prepare 未成功时 Run 仍然成功了")
	}
	// 未 Prepare 成功时 Cleanup 无害。
	if err := consumer.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
}

// TestNilConsumerCleanupIsSafe 钉住 nil 消费端的收尾无害。
func TestNilConsumerCleanupIsSafe(t *testing.T) {
	t.Parallel()

	var consumer *Consumer
	if err := consumer.Cleanup(context.Background()); err != nil {
		t.Fatalf("nil Cleanup: %v", err)
	}
}

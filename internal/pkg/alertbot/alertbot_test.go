package alertbot

import (
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func enabled(mutate func(*config.AlertBotConfig)) config.AlertBotConfig {
	cfg := config.AlertBotConfig{
		Enabled:    true,
		Platform:   config.AlertBotFeishu,
		WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/test",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

// TestDisabledReturnsNilChannel 钉住未启用时不产出通道。
//
// 返回一个"什么都不做的通道"会让人以为告警已接通——告警链路上的空壳
// 比明确的"没接"更危险。
func TestDisabledReturnsNilChannel(t *testing.T) {
	t.Parallel()

	got, err := New(config.AlertBotConfig{Enabled: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got != nil {
		t.Fatal("未启用时仍产出了通道")
	}
}

// TestNilNotifierIsSafe 钉住 nil 通道的方法不 panic。
//
// 装配层按 nil 判断是否接线，而告警路径上的 panic 会把进程带下去——
// 那正是告警最不该造成的后果。
func TestNilNotifierIsSafe(t *testing.T) {
	t.Parallel()

	var n *Notifier
	n.Send("boom")
	n.SendTo("https://other", "boom")
	if got := n.Platform(); got != "" {
		t.Fatalf("nil Platform() = %q, want empty", got)
	}
}

// TestEachPlatformBuilds 钉住三家平台都能构造，且平台标识可辨识。
func TestEachPlatformBuilds(t *testing.T) {
	t.Parallel()

	for _, platform := range []string{config.AlertBotFeishu, config.AlertBotDingTalk, config.AlertBotWeCom} {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()

			n, err := New(enabled(func(c *config.AlertBotConfig) { c.Platform = platform }))
			if err != nil {
				t.Fatalf("New(%s): %v", platform, err)
			}
			if n == nil {
				t.Fatalf("New(%s) 返回 nil", platform)
			}
			if got := n.Platform(); got != platform {
				t.Fatalf("Platform() = %q, want %q", got, platform)
			}
		})
	}
}

// TestUnknownPlatformIsRejected 钉住未知平台构造失败而不是静默降级。
func TestUnknownPlatformIsRejected(t *testing.T) {
	t.Parallel()

	_, err := New(enabled(func(c *config.AlertBotConfig) { c.Platform = "slack" }))
	if err == nil {
		t.Fatal("未知平台被接受了")
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Fatalf("错误 %q 没有指出是哪个平台", err)
	}
}

// TestRetryMappingAccountsForFirstAttempt 钉住次数换算。
//
// msgbot 的 MaxRetries 是**首次之后**的额外次数，而配置里 max_attempts 是
// 含首次的总次数——差 1。不换算的话配 2 会变成实际尝试 3 次，故障时把告警
// 队列堵得更久。
func TestRetryMappingAccountsForFirstAttempt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		maxAttempts int
		wantRetries int
	}{
		{name: "默认（2 次尝试）", maxAttempts: 0, wantRetries: 1},
		{name: "只试一次即不重试", maxAttempts: 1, wantRetries: 0},
		{name: "三次尝试 = 两次重试", maxAttempts: 3, wantRetries: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := enabled(func(c *config.AlertBotConfig) { c.MaxAttempts = tt.maxAttempts })
			if got := cfg.EffectiveMaxAttempts() - 1; got != tt.wantRetries {
				t.Fatalf("映射出的 MaxRetries = %d, want %d", got, tt.wantRetries)
			}
		})
	}
}

// TestEffectiveDefaults 钉住兜底值，尤其是超时不能太长。
//
// 告警发送发生在日志的告警队列里，一条卡住的发送会拖住它后面的所有告警。
func TestEffectiveDefaults(t *testing.T) {
	t.Parallel()

	empty := config.AlertBotConfig{}
	if got := empty.EffectiveTimeout(); got <= 0 || got > 30*time.Second {
		t.Fatalf("默认超时 %s 不在 (0, 30s] 内", got)
	}
	if got := empty.EffectiveMaxAttempts(); got < 1 {
		t.Fatalf("默认尝试次数 %d < 1，等于永不发送", got)
	}

	set := config.AlertBotConfig{Timeout: 3 * time.Second, MaxAttempts: 5}
	if got := set.EffectiveTimeout(); got != 3*time.Second {
		t.Fatalf("EffectiveTimeout() = %s, want 3s", got)
	}
	if got := set.EffectiveMaxAttempts(); got != 5 {
		t.Fatalf("EffectiveMaxAttempts() = %d, want 5", got)
	}
}

// TestSendToIgnoresURL 钉住 SendTo 不按运行期 url 新建 provider。
//
// 每条告警新建一套 HTTP 客户端与连接池是本仓明令禁止的形态，而告警路径上的
// 连接泄漏最难被发现——它只在故障时才被走到。
func TestSendToIgnoresURL(t *testing.T) {
	t.Parallel()

	n, err := New(enabled(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before := n.provider

	// 不会真的发出去（地址不可达），这里验的是 provider 没有被替换。
	n.SendTo("https://completely.other/hook", "x")

	if n.provider != before {
		t.Fatal("SendTo 按运行期 url 替换了 provider")
	}
}

// TestEmptyMessageIsNotSent 钉住空消息不占用发送配额。
func TestEmptyMessageIsNotSent(t *testing.T) {
	t.Parallel()

	n, err := New(enabled(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 仅验证不 panic 且立即返回：空消息在 send 的第一行就被挡下。
	n.Send("")
	n.Send("   ")
}

// Package mail 是 SMTP 出站通道的装配层封装：按 config.EmailConfig 构造
// gtkit/tools/mailx 客户端，向调用方暴露与供应商无关的窄接口。
// 通道未启用（host 为空）时不构造客户端；配置语义校验在 config.Validate 完成。
package mail

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/tools/mailx"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// Message 是一封待发送的纯文本邮件。
type Message struct {
	To      []string
	Subject string
	Text    string
}

// sender 是发送后端的最小面。
type sender interface {
	Send(ctx context.Context, msg mailx.Message) error
}

// newSender 是构造缝：测试注入 fake，生产走 mailx。
var newSender = func(cfg config.EmailConfig) (sender, error) {
	opts := make([]mailx.Option, 0, 4)
	if cfg.Port > 0 {
		opts = append(opts, mailx.WithPort(cfg.Port))
	}
	if cfg.User != "" {
		opts = append(opts, mailx.WithCredentials(cfg.User, cfg.Pass))
	}
	if cfg.FromName != "" {
		opts = append(opts, mailx.WithFromName(cfg.FromName))
	}
	if cfg.ImplicitSSL {
		opts = append(opts, mailx.WithImplicitSSL())
	}
	return mailx.New(cfg.Host, cfg.From, opts...)
}

// Client 是可并发使用的 SMTP 发送客户端。
type Client struct {
	s sender
}

// NewWithConfig 按配置构造客户端。
//
// 通道未启用时返回 (nil, nil)：调用方以 nil 判定"没有这个通道"，
// 而构造错误必须让启动失败——配置了却静默没有通道，比启动失败更难排查。
func NewWithConfig(cfg config.EmailConfig) (*Client, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	s, err := newSender(cfg)
	if err != nil {
		return nil, fmt.Errorf("mail: build smtp client: %w", err)
	}
	return &Client{s: s}, nil
}

// Send 发送一封纯文本邮件；超时与取消经 ctx 控制（库内另有连接超时兜底）。
func (c *Client) Send(ctx context.Context, msg Message) error {
	if c == nil {
		return errors.New("mail: client is not configured")
	}
	if len(msg.To) == 0 {
		return errors.New("mail: message has no recipients")
	}
	if err := c.s.Send(ctx, mailx.Message{To: msg.To, Subject: msg.Subject, Text: msg.Text}); err != nil {
		return fmt.Errorf("mail: send: %w", err)
	}
	return nil
}

package news

import (
	"context"
	"errors"
	"slices"
)

// SMSSender 按逻辑模板名发送短信，供应商模板编号与签名由实现负责翻译。
// 发送须遵守 ctx 的取消与期限；使用方不取得底层客户端的关闭权限。
type SMSSender interface {
	Send(ctx context.Context, phone, templateKey string, params map[string]string) error
}

// WithSMS borrows an SMS sender and snapshots its alert destinations.
// A nil sender, empty logical template or no phones disables SMS alerts.
func WithSMS(sender SMSSender, template string, phones ...string) Option {
	phones = slices.Clone(phones)
	return func(o *options) { o.sms, o.smsTemplate, o.smsPhones = sender, template, phones }
}

func (n *Notifier) sendSMS(ctx context.Context, result error) error {
	if n.sms != nil && n.smsTemplate != "" {
		for _, phone := range n.smsPhones {
			result = errors.Join(result, n.deliver(ctx, "sms", func(callCtx context.Context) error {
				return n.sms.Send(callCtx, phone, n.smsTemplate, map[string]string{"env": n.envName})
			}))
		}
	} else {
		n.emit(Delivery{Channel: "sms", Result: "disabled"})
	}
	return result
}

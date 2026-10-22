package news

import "context"

// MailMessage 是项目内的纯文本邮件契约，不包含供应商 SDK 类型。
type MailMessage struct {
	To      []string
	Subject string
	Text    string
}

// WebhookSender 向本次指定的机器人目标发送文本，发送须遵守 ctx 的取消与期限。
// 使用方借用该能力，不取得底层客户端的关闭权限。
type WebhookSender interface {
	Send(ctx context.Context, target, text string) error
}

// MailSender 发送项目邮件，发送须遵守 ctx 的取消与期限。
// 使用方借用该能力，不取得底层客户端的关闭权限。
type MailSender interface {
	Send(ctx context.Context, message MailMessage) error
}

// SMSSender 按逻辑模板名发送短信，供应商模板编号与签名由实现负责翻译。
// 发送须遵守 ctx 的取消与期限；使用方不取得底层客户端的关闭权限。
type SMSSender interface {
	Send(ctx context.Context, phone, templateKey string, params map[string]string) error
}

// AlertSink 是日志借用的尽力告警入口，不承诺消息送达。
// target 仅覆盖本次机器人目标；空值使用默认目标，邮件和短信仍按配置扇出。
type AlertSink interface {
	Warn(target string, args ...string)
}

// AlertDrainBinder 将日志 owner 本次清理 ctx 的取消关联到通知生命周期。
// 绑定不授予 Stop/Close 权限，也不立即阻止预算内的通知发送。
type AlertDrainBinder interface {
	// BindDrain 返回解除本次绑定的函数；release 须幂等并等待自身取消回调结束。
	BindDrain(ctx context.Context) (release func())
}

// NotificationSink 组合日志所需的告警与排空协作能力，不转移通知资源所有权。
type NotificationSink interface {
	AlertSink
	AlertDrainBinder
}

package news

import "context"

// AlertSink 直接满足 logger.Messager，是日志借用的尽力告警入口。
// SendTo 的 target 仅覆盖本次机器人目标；空值使用默认目标，其他通道仍按配置扇出。
type AlertSink interface {
	Send(text string)
	SendTo(target, text string)
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

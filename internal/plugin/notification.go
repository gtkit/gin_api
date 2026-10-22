package plugin

import "github.com/gtkit/gin-api/internal/pkg/news"

// NotificationSink 沿用 news 的项目通知契约，供装配端向日志提供借用能力。
// 它不暴露 SDK，也不授予消费者 Stop、Close 或通道资源所有权。
type NotificationSink = news.NotificationSink

// RobotProvider publishes a borrowed, lifecycle-protected robot sender.
type RobotProvider interface{ Robot() news.WebhookSender }

// MailProvider publishes a borrowed, lifecycle-protected mail sender.
type MailProvider interface{ Mail() news.MailSender }

// SMSProvider publishes a borrowed, lifecycle-protected SMS sender.
type SMSProvider interface{ SMS() news.SMSSender }

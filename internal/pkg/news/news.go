package news

import (
	"context"
	"fmt"
	"strings"
	"time"

	logger "github.com/gtkit/logger/v2"
	"github.com/gtkit/msgbot"
	"github.com/gtkit/msgbot/dingtalk"
	"github.com/gtkit/msgbot/feishu"
	"github.com/gtkit/msgbot/wecom"
	newslib "github.com/gtkit/news"
	"github.com/gtkit/stringx"

	"github.com/gtkit/gin-api/internal/pkg/mail"
	"github.com/gtkit/gin-api/internal/pkg/sms"
	"github.com/gtkit/gin-api/internal/platform/config"
)

// alertSendTimeout 是单次告警发送的独立超时：告警发生在日志调用路径上，
// 通道故障不能把日志调用方拖住。
const alertSendTimeout = 10 * time.Second

// mailSender / smsSender 是告警通道的最小面（使用方定义接口），测试注入 fake。
type mailSender interface {
	Send(ctx context.Context, msg mail.Message) error
}

type smsSender interface {
	Send(ctx context.Context, phone, templateKey string, params map[string]string) error
}

type Notifier struct {
	defaultURL    string
	envName       string
	now           func() string
	webhookSender func(ctx context.Context, url string, args ...string) error
	errorLogger   func(msg string)

	// 告警多通道：字段为 nil 即该通道未启用，Warn 扇出时跳过。
	mail        mailSender
	mailTo      []string
	sms         smsSender
	smsPhones   []string
	smsTemplate string
}

// NewWithConfig 构造多通道告警通知器。
//
// 邮件与短信客户端在此构造（当前唯一构造点——能力有唯一所有者，业务日后
// 需要注入时应把构造上提到装配层共享，而不是再建第二份实例）。
// 构造失败返回 error 让启动失败：配置了却静默没有通道是禁止形态。
func NewWithConfig(cfg *config.Config) (*Notifier, error) {
	n := &Notifier{
		now:         func() string { return time.Now().Format(time.DateTime) },
		errorLogger: func(msg string) { logger.Error(msg) },
	}
	if cfg == nil {
		// 无配置时按飞书兜底，与本字段引入之前的行为一致。
		n.webhookSender = webhookSenderFor("", "")
		return n, nil
	}
	n.webhookSender = webhookSenderFor(cfg.News.Platform, cfg.News.Secret)
	n.defaultURL = cfg.News.FSURL
	n.envName = string(cfg.Env)

	// nil 具体指针不得直接塞进接口字段：那会得到一个非 nil 的接口值，
	// Warn 的 nil 判定随之失效——所以只有客户端与告警配置同时存在才接线。
	mailClient, err := mail.NewWithConfig(cfg.Email)
	if err != nil {
		return nil, fmt.Errorf("news: init mail channel: %w", err)
	}
	if mailClient != nil && len(cfg.Email.AlertTo) > 0 {
		n.mail = mailClient
		n.mailTo = cfg.Email.AlertTo
	}

	smsClient, err := sms.NewWithConfig(cfg.SMS)
	if err != nil {
		return nil, fmt.Errorf("news: init sms channel: %w", err)
	}
	if smsClient != nil && cfg.SMS.AlertTemplate != "" && len(cfg.SMS.AlertPhones) > 0 {
		n.sms = smsClient
		n.smsPhones = cfg.SMS.AlertPhones
		n.smsTemplate = cfg.SMS.AlertTemplate
	}

	return n, nil
}

// 支持的机器人平台，别名到 config 的单一来源常量：启动校验（validateNewsConfig）
// 认哪些值，这里就分发哪些值——两处各写一份的话，白名单与分发迟早漂移。
// 留空按飞书处理：该字段是后加的，历史配置里没有它。
const (
	platformFeishu   = config.NewsPlatformFeishu
	platformDingTalk = config.NewsPlatformDingTalk
	platformWeCom    = config.NewsPlatformWeCom
)

// webhookSenderFor 返回一个按 url 发送告警的函数。
//
// 底层是 gtkit/msgbot：相比原先的 gtkit/news/fs，它多出加签、失败重试与
// 钉钉/企业微信支持，而三家平台的报文差异全部由它承担。
//
// **每次调用现建 provider 是廉价的**：msgbot 在未注入 HTTPClient 时用包级
// 共享的 client（internal.DefaultClient），因此这里不会产生新的连接池。
// 保留"运行期传 url"的能力是为了不改 Warn 的既有语义——调用方仍可把告警
// 发到默认之外的机器人。
func webhookSenderFor(platform, secret string) func(context.Context, string, ...string) error {
	return func(ctx context.Context, url string, args ...string) error {
		if strings.TrimSpace(url) == "" {
			return fmt.Errorf("news: webhook url is empty")
		}

		cfg := msgbot.Config{
			WebhookURL: url,
			Secret:     secret,
			// 与其他通道同值同语义的独立超时：告警发生在日志调用路径上，
			// 通道故障不能把日志调用方拖住。
			Timeout: alertSendTimeout,
		}

		var (
			provider msgbot.Provider
			err      error
		)
		// 归一化与启动校验同口径（后者大小写不敏感）：两处不一致时，
		// 一个通过了校验的 "Feishu" 会在运行期落到 default 分支报未知平台。
		switch strings.ToLower(strings.TrimSpace(platform)) {
		case platformDingTalk:
			provider, err = dingtalk.New(cfg)
		case platformWeCom:
			provider, err = wecom.New(cfg)
		case platformFeishu, "":
			provider, err = feishu.New(cfg)
		default:
			return fmt.Errorf("news: unknown alert platform %q (want %q, %q or %q)",
				platform, platformFeishu, platformDingTalk, platformWeCom)
		}
		if err != nil {
			return fmt.Errorf("news: build %s webhook: %w", platform, err)
		}

		return provider.SendText(ctx, strings.Join(args, " "))
	}
}

// Warn 扇出一条告警到全部已启用通道。
func (n *Notifier) Warn(url string, args ...string) {
	// nil 接收者直接返回而不是回退到某个包级默认实例：
	// 包级默认实例正是本次要消灭的东西——它与 log 包内自行构造的那个并存，
	// 形成两套配置可能不同的通知器，而调用方无从知道自己用的是哪一个。
	if n == nil {
		return
	}

	if url == "" {
		url = n.defaultURL
	}
	title := stringx.BuilderJoin([]string{n.envName, " time: ", n.now()})
	callArgs := make([]string, 0, len(args)+1)
	callArgs = append(callArgs, title)
	callArgs = append(callArgs, args...)
	// 与邮件/短信通道同值同语义的独立超时（outbound-notification spec：
	// 每次通道发送必须有独立超时）；失败与其他通道一样记录，不静默丢弃。
	ctx, cancel := context.WithTimeout(context.Background(), alertSendTimeout)
	if err := n.webhookSender(ctx, url, callArgs...); err != nil {
		// errorLogger 走 Error 级别，不触发 Messager，无递归。
		n.errorLogger("alert webhook send failed: " + err.Error())
	}
	cancel()

	// 扇出到其余已启用通道：单通道失败只记录，不阻断其余、不影响进程。
	msg := strings.Join(args, " ")
	n.sendMailAlert(msg)
	n.sendSMSAlert()
}

// sendMailAlert 经邮件通道发送告警；通道未启用时零调用。
func (n *Notifier) sendMailAlert(msg string) {
	if n.mail == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), alertSendTimeout)
	defer cancel()
	err := n.mail.Send(ctx, mail.Message{
		To:      n.mailTo,
		Subject: "[" + n.envName + "] service alert",
		Text:    msg,
	})
	if err != nil {
		// errorLogger 走 Error 级别，不触发 Messager，无递归。
		n.errorLogger("alert mail send failed: " + err.Error())
	}
}

// sendSMSAlert 经短信通道发送告警；通道未启用时零调用。
//
// 短信只报"哪个环境出事了"（params 契约固定 {"env": 环境名}），
// 细节走 webhook/邮件——模板变量自由化会撞上供应商的参数长度与审核限制。
func (n *Notifier) sendSMSAlert() {
	if n.sms == nil {
		return
	}
	for _, phone := range n.smsPhones {
		ctx, cancel := context.WithTimeout(context.Background(), alertSendTimeout)
		err := n.sms.Send(ctx, phone, n.smsTemplate, map[string]string{"env": n.envName})
		cancel()
		if err != nil {
			n.errorLogger("alert sms send failed: " + err.Error())
		}
	}
}

func (n *Notifier) ErrRecord(msg string) {
	if n == nil {
		return
	}

	n.errorLogger(msg)
	n.Warn("", msg)
}

func SendPicMsg(ctx context.Context, newsApp newslib.AppNewser, openids, shotname string) error {
	openidSlice := strings.SplitSeq(openids, ",")
	for openid := range openidSlice {
		openid = strings.TrimSpace(openid)
		if openid == "" {
			continue
		}
		if err := newsApp.SendImageMsg(ctx, openid, shotname); err != nil {
			logger.Errorf("send image message error:%v, openid:%s", err, openid)
		}
	}
	return nil
}

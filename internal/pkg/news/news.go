package news

import (
	"context"
	"fmt"
	"strings"
	"time"

	logger "github.com/gtkit/logger/v2"
	newslib "github.com/gtkit/news"
	"github.com/gtkit/news/fs"
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
	webhookSender func(url string, args ...string) error
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
		now:           func() string { return time.Now().Format(time.DateTime) },
		webhookSender: func(url string, args ...string) error { return fs.WebHookSend(url, args...) },
		errorLogger:   func(msg string) { logger.Error(msg) },
	}
	if cfg == nil {
		return n, nil
	}
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
	_ = n.webhookSender(url, callArgs...)

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
	openidSlice := strings.Split(openids, ",")
	for _, openid := range openidSlice {
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

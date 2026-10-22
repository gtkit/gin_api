package news

import (
	"context"
	"strings"
	"time"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	newslib "github.com/gtkit/news"
)

// alertSendTimeout 是单次告警发送的独立超时：告警发生在日志调用路径上，
// 通道故障不能把日志调用方拖住。
const alertSendTimeout = 10 * time.Second

type Notifier struct {
	defaultURL    string
	envName       string
	now           func() string
	webhookSender func(ctx context.Context, url string, args ...string) error
	report        func(Delivery)

	// 告警多通道：字段为 nil 即该通道未启用，Warn 扇出时跳过。
	mail        MailSender
	mailTo      []string
	sms         SMSSender
	smsPhones   []string
	smsTemplate string
}

// Warn 扇出一条告警到全部已启用通道。
func (n *Notifier) Warn(url string, args ...string) {
	// nil 接收者直接返回而不是回退到某个包级默认实例：
	// 包级默认实例正是本次要消灭的东西——它与 log 包内自行构造的那个并存，
	// 形成两套配置可能不同的通知器，而调用方无从知道自己用的是哪一个。
	if n == nil {
		return
	}

	// Per-channel failures have already been reported by WarnContext.
	_ = n.WarnContext(context.Background(), url, args...)
}

func (n *Notifier) ErrRecord(msg string) {
	if n == nil {
		return
	}

	applog.Error(msg)
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
			applog.Errorf("send image message error:%v, openid:%s", err, openid)
		}
	}
	return nil
}

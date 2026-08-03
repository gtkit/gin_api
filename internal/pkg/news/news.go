package news

import (
	"context"
	"strings"
	"time"

	logger "github.com/gtkit/logger/v2"
	newslib "github.com/gtkit/news"
	"github.com/gtkit/news/fs"
	"github.com/gtkit/stringx"

	"github.com/gtkit/gin-api/internal/platform/config"
)

type Notifier struct {
	defaultURL    string
	envName       string
	now           func() string
	webhookSender func(url string, args ...string) error
	errorLogger   func(msg string)
}

func NewWithConfig(cfg *config.Config) *Notifier {
	n := &Notifier{
		now:           func() string { return time.Now().Format(time.DateTime) },
		webhookSender: func(url string, args ...string) error { return fs.WebHookSend(url, args...) },
		errorLogger:   func(msg string) { logger.Error(msg) },
	}
	if cfg != nil {
		n.defaultURL = cfg.News.FSURL
		n.envName = string(cfg.Env)
	}

	return n
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

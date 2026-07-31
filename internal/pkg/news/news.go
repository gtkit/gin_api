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

var (
	defaultNotifier = NewWithConfig(nil)
)

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

func InitWithConfig(cfg *config.Config) {
	defaultNotifier = NewWithConfig(cfg)
}

func (n *Notifier) Warn(url string, args ...string) {
	if n == nil {
		n = defaultNotifier
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
		n = defaultNotifier
	}

	n.errorLogger(msg)
	n.Warn("", msg)
}

func Warn(url string, args ...string) {
	defaultNotifier.Warn(url, args...)
}

func ErrRecord(msg string) { defaultNotifier.ErrRecord(msg) }

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

func resetNewsHooksForTest() {
	defaultNotifier = NewWithConfig(nil)
}

func setNewsURLGetterForTest(fn func() string) { defaultNotifier.defaultURL = fn() }
func setEnvNameForTest(fn func() string)       { defaultNotifier.envName = fn() }
func setNewsNowForTest(fn func() string)       { defaultNotifier.now = fn }
func setWebhookSenderForTest(fn func(url string, args ...string) error) {
	defaultNotifier.webhookSender = fn
}
func setNewsErrorLoggerForTest(fn func(msg string)) { defaultNotifier.errorLogger = fn }

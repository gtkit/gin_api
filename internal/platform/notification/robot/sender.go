package robot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
	"github.com/gtkit/msgbot"
	"github.com/gtkit/msgbot/dingtalk"
	"github.com/gtkit/msgbot/feishu"
	"github.com/gtkit/msgbot/wecom"
)

const sendTimeout = 10 * time.Second

type sender struct {
	defaultURL string
	platform   string
	secret     string
	client     *http.Client
}

var _ news.WebhookSender = (*sender)(nil)
var _ logger.Messager = (*sender)(nil)

func openSender(_ context.Context, cfg config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
	platform := strings.ToLower(strings.TrimSpace(cfg.Platform))
	switch platform {
	case "", config.NewsPlatformFeishu:
		platform = config.NewsPlatformFeishu
	case config.NewsPlatformDingTalk, config.NewsPlatformWeCom:
	default:
		return nil, nil, fmt.Errorf("robot: unsupported platform")
	}
	return &sender{defaultURL: cfg.FSURL, platform: platform, secret: cfg.Secret}, nil, nil
}

func (s *sender) Send(text string) { s.SendTo("", text) }

func (s *sender) SendTo(target, text string) {
	if err := s.SendContext(context.Background(), target, text); err != nil && !errors.Is(err, news.ErrSenderDisabled) {
		logger.Error("webhook notification send failed")
	}
}

func (s *sender) SendContext(ctx context.Context, target, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if target == "" {
		target = s.defaultURL
	}
	if strings.TrimSpace(target) == "" {
		return news.ErrSenderDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	cfg := msgbot.Config{WebhookURL: target, Secret: s.secret, HTTPClient: s.client, Timeout: sendTimeout}
	var (
		provider msgbot.Provider
		err      error
	)
	switch s.platform {
	case config.NewsPlatformFeishu:
		provider, err = feishu.New(cfg)
	case config.NewsPlatformDingTalk:
		provider, err = dingtalk.New(cfg)
	case config.NewsPlatformWeCom:
		provider, err = wecom.New(cfg)
	default:
		return fmt.Errorf("robot: unsupported platform")
	}
	if err != nil {
		return err
	}
	return provider.SendText(ctx, text)
}

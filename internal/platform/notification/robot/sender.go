package robot

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/msgbot"
	"github.com/gtkit/msgbot/dingtalk"
	"github.com/gtkit/msgbot/feishu"
	"github.com/gtkit/msgbot/wecom"
)

type sender struct {
	platform string
	secret   string
	client   *http.Client
}

func openSender(_ context.Context, cfg config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
	platform := strings.ToLower(strings.TrimSpace(cfg.Platform))
	switch platform {
	case "", config.NewsPlatformFeishu:
		platform = config.NewsPlatformFeishu
	case config.NewsPlatformDingTalk, config.NewsPlatformWeCom:
	default:
		return nil, nil, fmt.Errorf("robot: unsupported platform")
	}
	return &sender{platform: platform, secret: cfg.Secret}, nil, nil
}

func (s *sender) Send(ctx context.Context, target, text string) error {
	if strings.TrimSpace(target) == "" {
		return news.ErrSenderMissing
	}
	cfg := msgbot.Config{WebhookURL: target, Secret: s.secret, HTTPClient: s.client, Timeout: 10 * time.Second}
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

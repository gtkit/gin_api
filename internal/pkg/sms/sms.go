// Package sms 是出站短信通道的装配层封装：按 config.SMSConfig 构造
// gtkit/smsx 的 aliyun 驱动客户端，调用方以逻辑模板名发送，
// 供应商模板编号与签名的解析在驱动内完成，供应商类型不出包。
// 通道未启用（provider 为空）时不构造客户端；配置语义校验在 config.Validate 完成。
package sms

import (
	"context"
	"errors"
	"fmt"

	smsprovider "github.com/gtkit/smsx"
	"github.com/gtkit/smsx/providers/aliyun"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// buildAliyunClient 是驱动构造缝：aliyun.Builder 的 NewClient 注入点是
// 包内未导出接口，外部无法构造 fake，测试改在这一层替换整个构造函数。
var buildAliyunClient = func(cfg aliyun.Config) (smsprovider.SMSClient, error) {
	return aliyun.Builder{}.Build(cfg)
}

// Client 是可并发使用的短信发送客户端。
type Client struct {
	inner smsprovider.SMSClient
}

// NewWithConfig 按配置构造客户端。
//
// 通道未启用时返回 (nil, nil)：调用方以 nil 判定"没有这个通道"，
// 构造错误让启动失败（与 mail 包同一契约）。
func NewWithConfig(cfg config.SMSConfig) (*Client, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if cfg.Provider != "aliyun" {
		return nil, fmt.Errorf("sms: unsupported provider %q: only \"aliyun\" is wired", cfg.Provider)
	}

	templates := make(map[string]aliyun.Template, len(cfg.Templates))
	for name, tpl := range cfg.Templates {
		templates[name] = aliyun.Template{Code: tpl.Code, SignName: tpl.SignName}
	}
	inner, err := buildAliyunClient(aliyun.Config{
		AccessKeyID:     cfg.AccessID,
		AccessKeySecret: cfg.AccessSecret,
		RegionID:        cfg.RegionID,
		DefaultSignName: cfg.SignName,
		Templates:       templates,
	})
	if err != nil {
		return nil, fmt.Errorf("sms: build %s client: %w", cfg.Provider, err)
	}
	return &Client{inner: inner}, nil
}

// Send 以逻辑模板名向单个手机号发送短信。
//
// 模板不存在、签名缺失等配置性错误由驱动以稳定哨兵返回
// （smsprovider.ErrTemplateNotFound / ErrInvalidConfig），调用方用 errors.Is 判别。
func (c *Client) Send(ctx context.Context, phone, templateKey string, params map[string]string) error {
	if c == nil {
		return errors.New("sms: client is not configured")
	}
	if err := c.inner.Send(ctx, phone, templateKey, params); err != nil {
		return fmt.Errorf("sms: send: %w", err)
	}
	return nil
}

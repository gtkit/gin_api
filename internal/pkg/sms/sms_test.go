package sms

import (
	"context"
	"errors"
	"strings"
	"testing"

	smsprovider "github.com/gtkit/smsx"
	"github.com/gtkit/smsx/providers/aliyun"

	"github.com/gtkit/gin-api/internal/platform/config"
)

type fakeSMS struct {
	phone    string
	template string
	params   map[string]string
	fail     error
}

func (f *fakeSMS) Send(_ context.Context, phone, templateID string, params map[string]string) error {
	f.phone, f.template, f.params = phone, templateID, params
	return f.fail
}

// TestNewWithConfigGate 钉住配置门与驱动配置翻译：未启用零构造、
// 未知 provider 拒绝、模板映射（含逐模板签名与默认签名）完整传给驱动。
//
// 不并行：使用包级构造缝。
func TestNewWithConfigGate(t *testing.T) {
	c, err := NewWithConfig(config.SMSConfig{})
	if c != nil || err != nil {
		t.Fatalf("disabled channel = (%v, %v), want (nil, nil)", c, err)
	}

	if _, err := NewWithConfig(config.SMSConfig{Provider: "tencent"}); err == nil {
		t.Fatal("NewWithConfig accepted an unwired provider")
	}

	original := buildAliyunClient
	defer func() { buildAliyunClient = original }()

	var gotCfg aliyun.Config
	fake := &fakeSMS{}
	buildAliyunClient = func(cfg aliyun.Config) (smsprovider.SMSClient, error) {
		gotCfg = cfg
		return fake, nil
	}

	c, err = NewWithConfig(config.SMSConfig{
		Provider: "aliyun", AccessID: "id", AccessSecret: "secret",
		RegionID: "cn-beijing", SignName: "default-sign",
		Templates: map[string]config.SMSTemplateConfig{
			"alert": {Code: "SMS_1"},
			"vcode": {Code: "SMS_2", SignName: "other-sign"},
		},
	})
	if err != nil || c == nil {
		t.Fatalf("valid config = (%v, %v), want client", c, err)
	}
	if gotCfg.AccessKeyID != "id" || gotCfg.DefaultSignName != "default-sign" || gotCfg.RegionID != "cn-beijing" {
		t.Fatalf("driver config = %+v", gotCfg)
	}
	if tpl := gotCfg.Templates["alert"]; tpl.Code != "SMS_1" || tpl.SignName != "" {
		t.Fatalf("alert template = %+v (sign 留空由驱动回退默认签名)", tpl)
	}
	if tpl := gotCfg.Templates["vcode"]; tpl.Code != "SMS_2" || tpl.SignName != "other-sign" {
		t.Fatalf("vcode template = %+v", tpl)
	}

	// 构造失败传播。
	buildAliyunClient = func(aliyun.Config) (smsprovider.SMSClient, error) {
		return nil, errors.New("bad credentials shape")
	}
	if _, err := NewWithConfig(config.SMSConfig{Provider: "aliyun"}); err == nil {
		t.Fatal("NewWithConfig swallowed a driver construction error")
	}
}

// TestSendDelegatesWithLogicalTemplate 钉住发送路径：逻辑模板名与参数原样
// 交给驱动（模板解析在驱动内），错误带 sms: 前缀包装，nil 客户端明确报错。
func TestSendDelegatesWithLogicalTemplate(t *testing.T) {
	t.Parallel()

	fake := &fakeSMS{}
	c := &Client{inner: fake}

	params := map[string]string{"env": "prod"}
	if err := c.Send(context.Background(), "13800000000", "alert", params); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fake.phone != "13800000000" || fake.template != "alert" || fake.params["env"] != "prod" {
		t.Fatalf("delegated call = %+v", fake)
	}

	fake.fail = smsprovider.ErrTemplateNotFound
	err := c.Send(context.Background(), "13800000000", "missing", nil)
	if err == nil || !errors.Is(err, smsprovider.ErrTemplateNotFound) {
		t.Fatalf("template error = %v, want ErrTemplateNotFound passthrough", err)
	}
	if !strings.Contains(err.Error(), "sms: send") {
		t.Fatalf("error %q lacks the sms: send prefix", err)
	}

	var nilClient *Client
	if err := nilClient.Send(context.Background(), "1", "t", nil); err == nil {
		t.Fatal("nil client Send returned nil error")
	}
}

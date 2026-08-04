package config

import (
	"fmt"
	"strings"
)

// UnwiredExtensionWarnings 返回"填了配置却没有运行时实现"的扩展位。
//
// 这些段落是预留的：Email / SMS / Pay / Aliyun / Baidu 是绝大多数业务迟早要接的
// 能力，先把配置形状定下来，使用者接线时不必再改一次配置契约——而那次改动会
// 波及所有已派生项目。
//
// 但预留不等于可用。**填了却静默无效的配置，比没有这个配置更危险**：
// 运维会以为邮件已经能发，直到某天发现一封都没出去。因此填了就提示一次。
//
// 只提示不阻断：接线本就是使用者的工作，母版不该因为它没做完而拒绝启动。
func (c *Config) UnwiredExtensionWarnings() []string {
	if c == nil {
		return nil
	}

	// 判据取每个段落的**关键字段**：只要它被填了，就说明使用者是想启用这个能力的。
	slots := []struct {
		name     string
		filled   bool
		wireHint string
	}{
		{
			name:     "email",
			filled:   strings.TrimSpace(c.Email.Host) != "",
			wireHint: "inject an SMTP client in providers.go and consume cfg.Email",
		},
		{
			name:     "sms",
			filled:   c.SMS.Enabled,
			wireHint: "inject an SMS client and consume cfg.SMS / cfg.Aliyun.SMS",
		},
		{
			name:     "pay",
			filled:   strings.TrimSpace(c.Pay.Wechat.AppID) != "" || strings.TrimSpace(c.Pay.Alipay.AppID) != "",
			wireHint: "inject a payment client and consume cfg.Pay",
		},
		{
			name:     "baidu",
			filled:   strings.TrimSpace(c.Baidu.Voice.APIKey) != "",
			wireHint: "inject a Baidu voice client and consume cfg.Baidu",
		},
	}

	var warnings []string
	for _, s := range slots {
		if !s.filled {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			"config section %q is filled but has no runtime consumer: it will have no effect "+
				"until you wire it up (%s). See the registration slots in SCAFFOLD.md.",
			s.name, s.wireHint))
	}
	return warnings
}

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
			name:     "llm",
			filled:   len(c.LLM.Providers) > 0,
			wireHint: "inject an LLM provider and consume cfg.LLM",
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

// RetentionWarnings 返回未设置保留期的个人信息表。
//
// 与扩展位告警分开：那些是"填了但没接线"，这里是"**没填**且后果是数据无限期
// 留存"。一份无人清理、越积越多的个人信息档案，出问题时才被发现的代价最高，
// 而它恰恰是最容易被忽略的——什么都不配就是这个状态。
//
// 只提示不阻断：保留期是使用方按司法辖区决定的事实，母版无从判断，
// 也不该因为它没决定而拒绝启动。
func (c *Config) RetentionWarnings() []string {
	if c == nil {
		return nil
	}

	var warnings []string
	if !c.Modules.User.RetentionEnabled() {
		warnings = append(warnings,
			"modules.user.deactivation_retention_days is 0: deactivation records keep the "+
				"user's phone, email, nickname and source IP indefinitely, and no cleanup task "+
				"is registered. Set a retention window that matches your jurisdiction, or make "+
				"sure an external process purges the table.")
	}
	return warnings
}

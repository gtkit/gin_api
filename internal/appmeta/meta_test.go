package appmeta

import (
	"strings"
	"testing"
)

// TestNoBusinessPollution 守卫：作为脚手架母版，appmeta 不得内置任何具体业务的
// 外部资源 URL 或缓存键（如某业务域名的默认头像）。身份名（CommandName 等）是
// 模板占位标识，由 project init 按项目重写，不在本守卫范围。
func TestNoBusinessPollution(t *testing.T) {
	t.Parallel()

	if strings.HasPrefix(DefaultAvatarURL, "http://") || strings.HasPrefix(DefaultAvatarURL, "https://") {
		t.Errorf("DefaultAvatarURL 不应内置外部 URL，母版应保持中性，got %q", DefaultAvatarURL)
	}
}

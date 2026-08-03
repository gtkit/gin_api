package appmeta

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// externalURLPattern 匹配任何 http/https 字面量。
var externalURLPattern = regexp.MustCompile(`https?://`)

// TestNoBusinessPollution 守卫：作为脚手架母版，appmeta 不得内置任何具体业务的
// 外部资源 URL 或缓存键。
//
// 判据改为**扫描整个生成物**，而不是绑死在某个具体常量上：
// 绑定常量的守卫会随该常量被删除而失效，而它想防的问题（业务污染混进身份文件）
// 并不会随之消失——下一个人加一个新常量时就没人拦着了。
func TestNoBusinessPollution(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("meta.go")
	if err != nil {
		t.Fatalf("read meta.go: %v", err)
	}

	// 注释里的说明文字不算污染，只看代码行。
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if externalURLPattern.MatchString(trimmed) {
			t.Errorf("appmeta 不应内置外部 URL，母版应保持中性，got %q", trimmed)
		}
	}
}

// TestIdentityConstantsAreNonEmpty 守住身份常量确实被生成。
//
// 生成器若因某种原因产出空值，check-appmeta 只比较"生成物与提交物一致"——
// 两边同时为空时它照样通过。这条从值本身兜住那种情形。
func TestIdentityConstantsAreNonEmpty(t *testing.T) {
	t.Parallel()

	if strings.TrimSpace(CommandName) == "" {
		t.Error("CommandName must not be empty")
	}
	if strings.TrimSpace(RuntimeName) == "" {
		t.Error("RuntimeName must not be empty")
	}
	// 前导/尾随空白曾是真实缺陷：RuntimeName 一度是 " Client"，
	// 拼出来的运行时名带一个多余空格。
	if strings.TrimSpace(RuntimeName) != RuntimeName {
		t.Errorf("RuntimeName has surrounding whitespace: %q", RuntimeName)
	}
	if strings.TrimSpace(CommandName) != CommandName {
		t.Errorf("CommandName has surrounding whitespace: %q", CommandName)
	}
}

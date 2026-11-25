// 数据清理工具.
package sanitizer

import (
	"html"
	"regexp"
	"strings"
)

type Sanitizer struct {
	htmlPattern   *regexp.Regexp
	scriptPattern *regexp.Regexp
}

func NewSanitizer() *Sanitizer {
	return &Sanitizer{
		htmlPattern:   regexp.MustCompile(`<[^>]*>`),
		scriptPattern: regexp.MustCompile(`(?i)<script.*?>.*?</script>`),
	}
}

// 清理HTML标签.
func (s *Sanitizer) StripHTML(input string) string {
	// 先移除script标签
	input = s.scriptPattern.ReplaceAllString(input, "")
	// 再移除其他HTML标签
	return s.htmlPattern.ReplaceAllString(input, "")
}

// 清理用于数据库查询的字符串.
func (s *Sanitizer) CleanForDB(input string) string {
	// 转义特殊字符
	input = strings.ReplaceAll(input, "'", "\\'")
	input = strings.ReplaceAll(input, "\"", "\\\"")
	input = strings.ReplaceAll(input, "\\", "\\\\")
	input = strings.ReplaceAll(input, "\x00", "\\0")
	input = strings.ReplaceAll(input, "\n", "\\n")
	input = strings.ReplaceAll(input, "\r", "\\r")
	input = strings.ReplaceAll(input, "\t", "\\t")

	return input
}

// 清理用于输出到HTML的内容.
func (s *Sanitizer) CleanForHTML(input string) string {
	return html.EscapeString(input)
}

// 限制字符串长度.
func (s *Sanitizer) Truncate(input string, maxLength int) string {
	if len(input) <= maxLength {
		return input
	}

	return input[:maxLength]
}

// 过滤敏感词.
func (s *Sanitizer) FilterSensitiveWords(input string) string {
	sensitiveWords := []string{
		"密码", "身份证", "银行卡", "支付密码",
		// 添加更多敏感词
	}

	result := input
	for _, word := range sensitiveWords {
		result = strings.ReplaceAll(result, word, "***")
	}

	return result
}

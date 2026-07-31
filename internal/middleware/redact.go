package middleware

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// redactedPlaceholder 是脱敏后替换敏感值的占位符。
const redactedPlaceholder = "[REDACTED]"

// sensitiveExactKeys 是按小写精确匹配的敏感字段名集合。
// 精确匹配用于像 code 这类容易与 zipcode/areacode 等无害字段发生子串误伤的键。
var sensitiveExactKeys = map[string]struct{}{
	"password":      {},
	"passwd":        {},
	"pwd":           {},
	"old_password":  {},
	"new_password":  {},
	"token":         {},
	"access_token":  {},
	"refresh_token": {},
	"id_token":      {},
	"code":          {},
	"sms_code":      {},
	"smscode":       {},
	"verify_code":   {},
	"verifycode":    {},
	"captcha":       {},
	"secret":        {},
	"api_key":       {},
	"apikey":        {},
	"app_secret":    {},
	"sign":          {},
	"signature":     {},
	"authorization": {},
	"mobile":        {},
	"phone":         {},
	"id_card":       {},
	"idcard":        {},
	"bank_card":     {},
	"card_no":       {},
}

// sensitiveStems 是按小写子串匹配的敏感词根，用于覆盖 user_password、client_secret、
// access_token 等复合命名。仅收录歧义极低的词根，避免过度脱敏。
var sensitiveStems = []string{"password", "passwd", "token", "secret", "apikey"}

// isSensitiveKey 判断字段名是否敏感（大小写不敏感）。
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	if _, ok := sensitiveExactKeys[lower]; ok {
		return true
	}
	for _, stem := range sensitiveStems {
		if strings.Contains(lower, stem) {
			return true
		}
	}
	return false
}

// redactQuery 对 query string 做脱敏：解析后将敏感键的值替换为占位符再重新编码。
// 解析失败（非法 query）时返回占位说明，绝不回传原文。
func redactQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "[unparseable query]"
	}
	for key, vs := range values {
		if !isSensitiveKey(key) {
			continue
		}
		for i := range vs {
			vs[i] = redactedPlaceholder
		}
		values[key] = vs
	}
	return values.Encode()
}

// redactBody 按 content-type 对 body 做脱敏：
//   - JSON：递归脱敏后重新序列化；
//   - form-urlencoded：按 query 规则脱敏；
//   - 其它/解析失败：只返回 content-type 与大小占位，绝不回传原始字节。
func redactBody(contentType string, raw []byte) string {
	if len(raw) == 0 {
		return ""
	}

	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch {
	case strings.Contains(mediaType, "json"):
		if s, ok := redactJSON(raw); ok {
			return s
		}
		return placeholder(mediaType, len(raw))
	case mediaType == "application/x-www-form-urlencoded":
		return redactQuery(string(raw))
	default:
		return placeholder(mediaType, len(raw))
	}
}

// redactJSON 反序列化 JSON 后递归脱敏敏感键，再序列化回字符串。
// 反序列化失败返回 ok=false，由调用方回退到占位符。
func redactJSON(raw []byte) (string, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	redacted := redactValue(v)
	out, err := json.Marshal(redacted)
	if err != nil {
		return "", false
	}
	return string(out), true
}

// redactValue 递归遍历任意 JSON 值：map 中命中敏感键的值替换为占位符，
// 其余键与数组元素继续递归。
func redactValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		for key, child := range val {
			if isSensitiveKey(key) {
				val[key] = redactedPlaceholder
				continue
			}
			val[key] = redactValue(child)
		}
		return val
	case []any:
		for i := range val {
			val[i] = redactValue(val[i])
		}
		return val
	default:
		return val
	}
}

// placeholder 生成不含敏感内容的 body 占位说明。
func placeholder(mediaType string, size int) string {
	if mediaType == "" {
		mediaType = "unknown"
	}
	return fmt.Sprintf("[%s; %d bytes]", mediaType, size)
}

package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	resp "github.com/gtkit/gin-api/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
)

// InputValidation 输入验证中间件.
func InputValidation() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Step 1: 先验证查询参数。
		// 这一步主要用于提前拦截明显异常或带攻击特征的输入，避免把非法参数传入业务层。
		if err := validateQueryParams(c.Request.URL.Query()); err != nil {
			abortInputValidationError(c, "无效的查询参数: "+err.Error())
			return
		}

		// Step 2: 再验证请求头。
		// 对请求头保留具体错误细节是安全的，而且能帮助客户端快速修正请求。
		if err := validateHeaders(c.Request.Header); err != nil {
			abortInputValidationError(c, "无效的请求头: "+err.Error())
			return
		}

		// Step 3: 校验通过后继续执行后续链路。
		c.Next()
	}
}

// abortInputValidationError 统一返回输入校验失败的响应。
// 这类错误属于客户端请求问题，因此保留清晰的修正提示是合理的。
func abortInputValidationError(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, resp.NewResponse(int32(goerr.ErrParams), message))
}

func validateQueryParams(params url.Values) error {
	// SQL注入检测
	sqlPatterns := []string{
		`(?i)(union|select|insert|update|delete|drop|create|alter|exec|execute)`,
		`(?i)(\bOR\b|\bAND\b).*(\=|LIKE)`,
		`[\'"]*(\s)*(;|--)`,
	}

	for key, values := range params {
		for _, value := range values {
			for _, pattern := range sqlPatterns {
				if matched, _ := regexp.MatchString(pattern, value); matched {
					return fmt.Errorf("参数 %s 包含潜在的SQL注入攻击", key)
				}
			}
		}
	}

	return nil
}

func validateHeaders(headers http.Header) error {
	// 检查User-Agent
	userAgent := headers.Get("User-Agent")
	if userAgent == "" {
		return errors.New("缺少User-Agent头")
	}

	// 检查Referer（如果需要）
	referer := headers.Get("Referer")
	if referer != "" && !isValidReferer(referer) {
		return errors.New("无效的Referer")
	}

	return nil
}

func isValidReferer(referer string) bool {
	// 检查Referer是否在允许的域名列表中
	allowedDomains := []string{"example.com", "localhost"}

	parsedURL, err := url.Parse(referer)
	if err != nil {
		return false
	}

	for _, domain := range allowedDomains {
		if strings.HasSuffix(parsedURL.Host, domain) {
			return true
		}
	}

	return false
}

// Package upload 提供可选上传器、文件展示策略和回源响应头。
//
// 用户上传的文件被静态服务或下载接口回源时，浏览器把 HTML/SVG 当文档渲染
// 就是存储型 XSS。Policy 用于判断某个路径的内容能否内联展示——
// 零值策略一律不能（fail-closed），白名单由构造方显式给出。
//
// 响应头的写出由 FileHeaders 承载；上传本身
// （对象存储、分片、STS 直传）经 gtkit/gouploader 接入，见本目录 README.md。
package upload

import (
	"mime"
	"path"
	"strings"
)

// Policy 是内联展示白名单。零值策略拒绝一切内联（全部强制附件下载）。
type Policy struct {
	inline map[string]struct{}
}

// NewPolicy 用一组 MIME 类型构造白名单；类型经规范化（去参数、小写）后存储。
func NewPolicy(mimeTypes ...string) Policy {
	inline := make(map[string]struct{}, len(mimeTypes))
	for _, mt := range mimeTypes {
		if normalized := normalizeMIME(mt); normalized != "" {
			inline[normalized] = struct{}{}
		}
	}
	return Policy{inline: inline}
}

// DefaultPolicy 返回保守默认白名单：只收浏览器渲染时没有脚本执行面的类型。
// 刻意排除 text/html 与 image/svg+xml——两者都能携带脚本，是存储型 XSS
// 的主要载体；需要内联它们是一次显式安全决策，由派生项目自建 Policy 承担。
func DefaultPolicy() Policy {
	return NewPolicy(
		"image/jpeg",
		"image/png",
		"image/gif",
		"image/webp",
		"application/pdf",
		"text/plain",
	)
}

// AllowsInline 判定一个 MIME 类型（可携带参数，如 "text/plain; charset=utf-8"）
// 能否内联展示。
func (p Policy) AllowsInline(mimeType string) bool {
	if len(p.inline) == 0 {
		return false
	}
	_, ok := p.inline[normalizeMIME(mimeType)]
	return ok
}

// AllowsInlinePath 按请求路径的扩展名推断 MIME 后判定。
// 无扩展名或未知扩展名一律不可内联：推断不出类型时按最危险情况处理。
func (p Policy) AllowsInlinePath(requestPath string) bool {
	ext := path.Ext(requestPath)
	if ext == "" {
		return false
	}
	mt := mime.TypeByExtension(ext)
	if mt == "" {
		return false
	}
	return p.AllowsInline(mt)
}

func normalizeMIME(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	return strings.ToLower(strings.TrimSpace(base))
}

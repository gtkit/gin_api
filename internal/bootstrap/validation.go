package bootstrap

import (
	"fmt"

	"github.com/gtkit/verify"

	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// verifyInit 生产恒为 verify.New；测试经它注入失败，证明初始化失败会阻断装配。
var verifyInit = verify.New

// validationOptions 返回派生项目的自定义校验规则与中文消息注册项。
//
// var 而非 func：测试经替换证明注册位里的选项确实会进入初始化装配；
// 派生项目直接改写函数体，在切片里追加
// verify.WithTranslation / verify.WithValidationTranslation / verify.WithStructValidation。
//
// 注册只能发生在这里（启动阶段、任何绑定之前）：validator/v10 的注册
// 与校验不能并发执行，handler 里注册规则是数据竞争。
var validationOptions = func() []verify.Option {
	return []verify.Option{
		// http_url 的中文消息。
		//
		// **必须补**：validator 的中文翻译包收录了 `url` 与 `max`，没有收录 `http_url`。
		// 而母版的 UserUpdateRequest.Avatar 用的是后者——`url` 不限 scheme，实测放行
		// javascript:／data:／file:／vbscript:，那类值入库后由消费端渲染成链接即是
		// 存储型 XSS 载体。
		//
		// 缺了这条注册，校验照样生效，只是该字段的错误消息退回英文（"link http_url"）。
		// 那是一次静默的、按字段的降级：不报错、不影响判定，只有真正触发该字段
		// 校验失败时才看得见。
		//
		// 用 WithValidationTranslation 而不是 WithTranslation：前者给**既有** tag 配消息，
		// 后者用于新 tag 且要求同时提供校验函数——而 http_url 的校验函数由 validator
		// 内置，自己再写一份只会多一处可能与内置行为漂移的实现。
		verify.WithValidationTranslation("http_url", "{0}必须是 http 或 https 链接"),
	}
}

// initValidation 初始化 gin 共享 validator 的中文翻译与自定义规则。
//
// 必须在 HTTP engine 接受任何请求之前执行：validator 在首次校验某类型时
// 缓存该类型的字段名元数据，晚于首次绑定的初始化会让已缓存类型的
// 校验消息永远输出 Go 字段名而不是对外 json 字段名。
func initValidation() error {
	if err := verifyInit(validationOptions()...); err != nil {
		return fmt.Errorf("init validation translator: %w", err)
	}
	// 就绪标志只在初始化成功后置位：渲染路径据此决定走翻译还是既有渲染，
	// 它自己绝不触发初始化（那会在请求并发期间改写全局 validator）。
	resp.EnableValidationTranslation()
	return nil
}

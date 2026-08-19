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
	return nil
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

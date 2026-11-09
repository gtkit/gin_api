package resp

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/gtkit/goerr"
	"github.com/gtkit/verify"
)

// 本文件把 gtkit/verify 的三类校验错误（单字段 / 结构体 / map）各自收口成
// 统一的 422 响应，覆盖 verify 的 FieldErr / StructErr / AllStructErr /
// StructFieldErrors / MapErr / AllMapErr 全套错误 API。
//
// 与 response.go 里既有的 BindErr / BindStructErr 的关系：
//   - BindErr / BindStructErr：多字段失败只回排序后的**第一条**（msg 单句，
//     data 为空数组）。既有契约，保持不变。
//   - BindAllErr：一次回**全部**字段错误（msg 聚合摘要 + data.fields 结构化明细）。
//   - FieldErr：单值校验（application 层 verify.Field）的对客收口。
//   - ValidateMapErr / ValidateMapAllErr：动态 payload 的 map 校验收口。
//
// 全部方法都遵守同一条时序铁律：翻译器未就绪（启动阶段 verify.New 未成功）
// 时绝不触发翻译——validator 首次校验即缓存字段名，请求并发期间的懒初始化
// 会改写全局 validator，是真实数据竞争（见 response.go 的 validationTranslationReady）。

// FieldErrorPayload 是 verify.FieldError 的对客投影。
//
// 只暴露字段名（对外 json tag）与翻译后的中文消息。刻意不含 verify.FieldError
// 的 Tag 与 Value：Value 是用户提交的原始值（可能是口令、手机号等敏感输入），
// 回显等于把敏感数据再抛回客户端；Tag 是内部校验规则名，属于实现细节。
// 二者只进日志（ErrorLogFields），不进响应体。
type FieldErrorPayload struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// fieldErrorPayloads 把 verify.FieldErrors 投影为对客安全的切片。
// 每条消息走与 msg 相同的脱敏管线（红act 手机号/邮箱、截断超长），
// 保证 data.fields 与顶层 msg 的对外口径一致。
func fieldErrorPayloads(fields verify.FieldErrors) []FieldErrorPayload {
	out := make([]FieldErrorPayload, 0, len(fields))
	for _, fe := range fields {
		out = append(out, FieldErrorPayload{
			Field:   fe.Field,
			Message: goerr.SanitizeForClient(fe.Message, clientMessageOptions),
		})
	}
	return out
}

// validationReady 报告 err 是否为「当前进程可翻译」的校验错误：翻译器已就绪
// 且 err 链上确有 validator.ValidationErrors。两个条件缺一不可——未就绪时
// 返回 false，调用方据此走固定文案兜底，绝不触碰 verify 的翻译路径。
func validationReady(err error) (validator.ValidationErrors, bool) {
	if !validationTranslationReady.Load() {
		return nil, false
	}
	ve, ok := errors.AsType[validator.ValidationErrors](err)
	return ve, ok
}

// BindAllErr 处理结构体绑定/校验错误，返回 HTTP 422：msg 是所有出错字段的
// 聚合摘要，data.fields 给出逐字段的结构化明细（字段名 + 中文消息）。
//
// 与 BindStructErr 的分工：后者多字段失败只回排序后的第一条，适合一次只提示
// 一个错误的接口；BindAllErr 一次回全部，适合表单类接口让客户端逐字段高亮。
// 两者都是 422、都不泄漏 Go 结构体名与导出字段名（用对外 json tag）。
// 请求体超限仍优先映射为 413（与 BindErr 一致）。
//
// 翻译器未就绪或 err 非校验类时，回退固定文案「请求参数格式错误」，
// 原始错误保留为诊断 cause。
func BindAllErr(c *gin.Context, err error) {
	if payloadTooLargeFromBind(c, err) {
		return
	}
	if ve, ok := validationReady(err); ok {
		if translated := verify.AllStructErr(ve); translated != nil {
			ErrWithData(c, translated, gin.H{"fields": fieldErrorPayloads(verify.StructFieldErrors(ve))})
			return
		}
	}
	Err(c, goerr.New(err, goerr.StatusValidateParams(), "请求参数格式错误"))
}

// FieldErr 处理单字段校验错误——典型来自 application 层的
// verify.Field(value, tag) / verify.FieldCtx。返回 HTTP 422，msg 形如
// 「phone 必须是有效的大陆手机号」。字段名由调用方给出：单值校验没有结构体
// 上下文可提取 json tag，只有调用方知道这个值对应哪个对外字段。
//
// 与 verify.FieldErr 的透传约定一致：err 非 validator.ValidationErrors 时
// 原样交给统一错误处理（Err），不强行套 422——否则会把 DB/网络等错误误报成
// 参数错误。翻译器未就绪时回退固定文案并保留 cause。
func FieldErr(c *gin.Context, field string, err error) {
	if err == nil {
		return
	}
	if _, ok := errors.AsType[validator.ValidationErrors](err); !ok {
		Err(c, err)
		return
	}
	if validationTranslationReady.Load() {
		if translated := verify.FieldErr(field, err); translated != nil {
			Err(c, translated)
			return
		}
	}
	Err(c, goerr.New(err, goerr.StatusValidateParams(), "请求参数格式错误"))
}

// ValidateMapErr 处理 verify.Map / verify.MapCtx 的校验结果（map[string]any），
// 返回 HTTP 422，msg 为键名排序后的第一条字段错误。空结果视为无错误，不写响应
// （调用方应在 len(errs) == 0 时继续正常流程）。
//
// 用于校验没有固定结构体的动态 payload（如开放 API 的任意 JSON 对象）。
// 翻译器未就绪、或结果里没有可翻译的 ValidationErrors 时回退固定文案。
func ValidateMapErr(c *gin.Context, errs map[string]any) {
	if len(errs) == 0 {
		return
	}
	if validationTranslationReady.Load() {
		if translated := verify.MapErr(errs); translated != nil {
			Err(c, translated)
			return
		}
	}
	Err(c, goerr.New(nil, goerr.StatusValidateParams(), "请求参数格式错误"))
}

// ValidateMapAllErr 处理 verify.Map 的校验结果，返回 HTTP 422，msg 为所有
// 字段错误的聚合摘要；嵌套 map 的对外字段名按点号拼接（如「user.email」）。
//
// 与 BindAllErr 的差异：verify 只为结构体导出了结构化字段提取器
// （StructFieldErrors），map 没有对应的公开 API，因此本方法只在 msg 里给
// 聚合摘要，data 保持空数组。需要逐字段结构化明细时，对结构体绑定改用
// BindAllErr。空结果视为无错误，不写响应。
func ValidateMapAllErr(c *gin.Context, errs map[string]any) {
	if len(errs) == 0 {
		return
	}
	if validationTranslationReady.Load() {
		if translated := verify.AllMapErr(errs); translated != nil {
			Err(c, translated)
			return
		}
	}
	Err(c, goerr.New(nil, goerr.StatusValidateParams(), "请求参数格式错误"))
}

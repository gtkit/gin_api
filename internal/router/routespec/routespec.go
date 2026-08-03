// Package routespec 定义 HTTP 路由契约的描述符。
//
// 它是路由契约的**单一来源**：访问策略、超时策略、限流策略都挂在描述符上，
// 由中央 registrar 统一翻译成 handler chain。此前这些策略散落在各模块的
// 路由注册代码里，同一件事有"注册函数"和"文档/规范"两套来源，必须靠人工同步。
//
// 依赖边界（由架构传感器强制）：本包只允许依赖标准库与 Gin 的 HTTP 类型。
// 禁止 import bootstrap、runtime/module、application、repository、GORM、配置实现
// 或任何具体中间件——它是**协议描述包**，不是 service locator 或 registry。
// 反过来，internal/router 只消费本包，禁止 import 任何 internal/module/*。
// 依赖方向因此固定为：bootstrap → router ← routespec ← transport/http。
package routespec

import (
	"time"

	"github.com/gin-gonic/gin"
)

// AccessMode 是路由的访问控制模式。
//
// 零值刻意是 AccessUnspecified 且注册期拒绝：作者漏写访问策略是最可能的失误路径，
// 而"漏写 = 公开"会让一个本该鉴权的接口静默暴露。零值必须指向拒绝，
// 公开接口必须显式写 AccessPublic 表明这是有意的。
type AccessMode uint8

const (
	// AccessUnspecified 是非法零值，注册期 fail-closed。
	AccessUnspecified AccessMode = iota
	// AccessPublic 显式声明公开，无需任何身份。
	AccessPublic
	// AccessOptionalAuthenticated 允许匿名访问；一旦提供 token 则必须合法。
	AccessOptionalAuthenticated
	// AccessAuthenticated 要求已认证身份。
	AccessAuthenticated
	// AccessAuthorized 在认证之外还要求通过具名 authorizer 的授权判定。
	AccessAuthorized
)

func (m AccessMode) String() string {
	switch m {
	case AccessPublic:
		return "public"
	case AccessOptionalAuthenticated:
		return "optional_authenticated"
	case AccessAuthenticated:
		return "authenticated"
	case AccessAuthorized:
		return "authorized"
	default:
		return "unspecified"
	}
}

// AccessPolicy 描述一条路由的访问控制要求。
type AccessPolicy struct {
	Mode AccessMode
	// AuthorizerID 仅在 AccessAuthorized 时非空，引用装配期 registry 中的具名 authorizer。
	//
	// 这里只持**稳定 ID**而不是闭包：descriptor 由模块 transport 拥有，
	// 而 authorizer 的具体实现在 runtime 装配层。持闭包会让 transport 反向依赖装配层。
	AuthorizerID string
}

// TimeoutMode 是路由的超时语义。
//
// 零值同样非法：没有超时策略的路由会一直占用连接与 goroutine，
// 而"忘了写"和"有意不限"在代码里长得一模一样。
type TimeoutMode uint8

const (
	// TimeoutUnspecified 是非法零值，注册期 fail-closed。
	TimeoutUnspecified TimeoutMode = iota
	// TimeoutCooperative 依赖调用链观察 ctx 并返回 DeadlineExceeded，由终态中间件映射 504。
	TimeoutCooperative
	// TimeoutClientVisible 到点先向客户端返回，但仍等待 handler 结束。
	TimeoutClientVisible
	// TimeoutStreaming 无总时长上限，使用可续期的写空闲超时。
	TimeoutStreaming
)

func (m TimeoutMode) String() string {
	switch m {
	case TimeoutCooperative:
		return "cooperative"
	case TimeoutClientVisible:
		return "client_visible"
	case TimeoutStreaming:
		return "streaming"
	default:
		return "unspecified"
	}
}

// TimeoutPolicy 描述一条路由的超时预算。
//
// 字段的具体取值约束与执行语义由超时批次落地；本包只负责让策略成为
// 描述符的一部分，并在注册期拒绝零值——这样后续批次接线时不存在"没有策略的路由"。
type TimeoutPolicy struct {
	Mode TimeoutMode
	// Deadline 是业务预算，Cooperative / ClientVisible 必须为正。
	Deadline time.Duration
	// WriteGrace 是 socket 写 deadline 相对业务预算的宽限，非流式必须为正。
	WriteGrace time.Duration
	// IdleWrite 是流式的写空闲超时，Streaming 必须为正。
	IdleWrite time.Duration
	// HeartbeatInterval 是流式心跳间隔，Streaming 必须为正且严格小于 IdleWrite。
	//
	// 严格小于是硬约束：标准库明确 write deadline 过期后再设置不能延长它，
	// 心跳晚于空闲超时就再也救不回来，只能断连。
	HeartbeatInterval time.Duration
}

// RouteSpec 是一条路由的完整契约。
//
// 后续批次只做**纵向追加**（请求体策略、OperationID、schema、tags 等），
// 不允许重写成第二套描述符——那正是本包要消灭的"两套来源"问题。
type RouteSpec struct {
	// Method 是大写的 HTTP 方法。
	Method string
	// Path 是模块内的相对路径，以 / 开头。API 版本前缀由中央 registrar 添加，
	// 模块不自行拼接——否则版本策略会散落到每个模块。
	Path string
	// Handler 是业务处理函数。注册期拒绝 nil。
	Handler gin.HandlerFunc
	// AccessPolicy 由访问策略批次消费。
	AccessPolicy AccessPolicy
	// TimeoutPolicy 由超时批次消费。
	TimeoutPolicy TimeoutPolicy
	// OperationID 是该操作的**全局唯一**稳定标识。
	//
	// 它是 OpenAPI 里客户端生成器用来命名方法的那个值，因此重命名就是一次
	// 破坏性变更。全局唯一由 compile 强制：重复会让生成的客户端出现同名方法，
	// 而多数生成器对此的处理是**静默覆盖其中一个**——于是一条真实存在的接口
	// 在客户端里消失了。
	OperationID string
	// Tags 是 OpenAPI 分组标签，输出前稳定排序。
	Tags []string
	// Summary 是一句话描述。
	Summary string
	// Responses 是该操作**额外**声明的响应状态码。
	//
	// 200 与鉴权相关的 401/403/503 由访问策略派生，不必在此重复；
	// 这里只写业务特有的（如写方法的 422、探针的 503）。
	//
	// 响应契约归描述符所有而不是由生成器猜：生成器只能从方法名猜，
	// 而"这个接口会不会返回 422"是业务事实，猜错的方向是**漏声明**——
	// 客户端因此不会处理那个分支。
	Responses map[string]string
	// Deprecated 标记该操作已弃用。
	//
	// 只是标记，不改变行为：真正的下线是删除 RouteSpec，那要走破坏性变更流程。
	Deprecated bool
	// RateLimitPolicyID 由限流批次消费；空表示不限流。
	RateLimitPolicyID string
}

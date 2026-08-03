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
	// RequestBody 是请求体的**样本值**（传零值实例，如 `dto.UserUpdateRequest{}`）。
	//
	// 传值而不是 reflect.Type：描述符由业务代码书写，`dto.X{}` 比
	// `reflect.TypeOf(dto.X{})` 少一层噪音，而生成器无论如何都要反射。
	//
	// 写方法必须在它与 NoRequestBody 之间二选一，见 NoRequestBody 的说明。
	RequestBody any
	// NoRequestBody 显式声明该写方法不接受请求体。
	//
	// **不能用 RequestBody == nil 表达**：那样"忘了声明请求体"和"确实没有请求体"
	// 在代码里长得一模一样，而前者的后果是契约里少掉这个接口最重要的部分——
	// 客户端生成器会产出一个不接受任何参数的方法。
	NoRequestBody bool
	// ResponseShape 声明 200 响应的形状。零值非法，注册期 fail-closed。
	ResponseShape ResponseShape
	// ResponseBody 是 200 响应体的样本值，由 ResponseShape 决定如何解读。
	//
	// Enveloped 时它是信封里 data 的类型（只声明 data，不声明信封：
	// 信封形状由 response 包拥有，写进每条描述符就是把同一件事抄 N 遍，
	// 改信封时必然漏掉几条）；Raw 时它就是整个响应体；Empty 时必须为 nil。
	ResponseBody any
}

// ResponseShape 是 200 响应体的形状。
//
// 需要三态而不是"有/无"两态：本仓同时存在两类响应——业务接口走
// `response.ResResult` 统一信封，而 K8s 探针返回裸 JSON（它的形状是编排器
// 的契约，不能改成信封）。把探针也当成信封会让契约**说谎**，
// 而照着它生成的探针客户端会去读一个不存在的 code 字段。
type ResponseShape uint8

const (
	// ResponseShapeUnspecified 是非法零值，注册期 fail-closed。
	//
	// 沉默不能当作"没有响应体"：漏声明与确实没有在代码里长得一模一样，
	// 而漏声明的后果是客户端生成器产出一个不返回数据的方法。
	ResponseShapeUnspecified ResponseShape = iota
	// ResponseShapeEnveloped 表示响应走统一信封，ResponseBody 是其中的 data。
	ResponseShapeEnveloped
	// ResponseShapeRaw 表示 ResponseBody 就是整个响应体，不加信封。
	ResponseShapeRaw
	// ResponseShapeEmpty 表示走信封但没有业务数据，线上是 `{code,msg,data:null}`。
	ResponseShapeEmpty
)

func (s ResponseShape) String() string {
	switch s {
	case ResponseShapeEnveloped:
		return "enveloped"
	case ResponseShapeRaw:
		return "raw"
	case ResponseShapeEmpty:
		return "empty"
	default:
		return "unspecified"
	}
}

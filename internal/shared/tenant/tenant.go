// Package tenant 承载请求上下文里的租户身份，供数据访问层的隔离判定消费。
//
// 它只做「带着」与「取出」，不做认证、不碰数据库、不依赖 gin 或 GORM——
// 因此任何层都可以 import 它（见 docs/architecture.md 的「纯基础包」）。
// 真正的条件注入在 internal/pkg/dataisolate，那是基础设施型包。
//
// 为什么不复用 internal/shared/auth 的 Principal：那个类型承载的是**认证结论**
// （你是谁、令牌声明了哪些角色），由 ginc 存进 gin context 的 Keys。而数据访问层
// 拿到的是 context.Context——handler 一律往下传 c.Request.Context()，不是 *gin.Context，
// 于是 gin Keys 里的东西在 GORM 回调里根本取不到。两者是不同的载体，
// 不是同一个东西的两种写法。
//
// 为什么不给 auth.Principal 加 TenantID：那会把租户来源绑到令牌声明上，
// 牵动 JWT 的签发与校验链路。租户从什么派生是装配方的决定（令牌声明、
// 用户归属表、组织同步结果都可能），本包只规定「必须来自已认证身份」这一条。
package tenant

import "context"

// ID 是租户标识。
//
// 取字符串而不是整型：租户标识是不参与运算的**不透明身份**，slug、UUID 与
// 十进制数字都能承载。判别列相应地应为字符串列（如 VARCHAR）。
type ID string

type idKey struct{}

// bypassKey 与 idKey 分开：两者语义不同，且必须能被分别判定——
// 「没有身份」与「要求绕过但绕过被禁用」是两种不同的失败，
// 错误信息里也必须是两句不同的话（否则写了绕过的人只会收到「没有身份」，
// 无从知道是安全闸拦下的）。
type bypassKey struct{}

// With 返回携带租户身份的 ctx。
//
// **这是租户进入数据访问层的唯一入口，也因此是一个越权面**：任何能调用它的代码
// 都能把后续查询切到任意租户。它的正当调用点只有两处——解析已认证身份的中间件，
// 以及有意的、单次调用粒度的越权操作（超管代查这类）。业务逻辑与仓储不该出现它。
//
// 空标识按「没有身份」处理，见 From。
func With(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, idKey{}, id)
}

// From 取出 ctx 携带的租户身份；没有则 ok=false。
//
// 空标识返回 ok=false，而不是一个空的 ID：空标识不是身份。若把它当身份用，
// 注入出来的条件是判别列等于空串——那既不会报错，也不会命中任何正常数据，
// 表现为「隔离在工作」，实际是每次查询都查了个不存在的租户。
func From(ctx context.Context) (ID, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(idKey{}).(ID)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// WithBypass 返回一个要求跳过租户隔离的 ctx。
//
// 只应作用于**单次**数据访问调用，形如：
//
//	repo.List(tenant.WithBypass(ctx), q)
//
// 不要把它放进请求级中间件或长生命周期的 ctx——那会让整条链路脱离隔离，
// 而在代码里它只表现为多了一行。
//
// 是否真的生效由 dataisolate 的安全闸决定（默认关闭）。闸门关闭时本标记不生效，
// 且失败信息会点名该配置项。
func WithBypass(ctx context.Context) context.Context {
	return context.WithValue(ctx, bypassKey{}, true)
}

// IsBypass 报告 ctx 是否要求跳过租户隔离。
func IsBypass(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	bypass, ok := ctx.Value(bypassKey{}).(bool)
	return ok && bypass
}

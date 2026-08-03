package router

import "github.com/gtkit/gin-api/internal/router/routespec"

// Module 定义业务模块向路由层贡献路由契约的能力。
//
// 返回描述符而不是接收 *gin.RouterGroup：模块自己注册时，访问策略、超时预算与限流
// 只能表达为中间件的调用顺序，散落在各模块里且无法被统一校验；版本前缀、分组、
// 中间件顺序这些本应跨模块一致的东西也会被复制到每个模块。
//
// 改为贡献描述符后，模块只声明"这条路由是什么、要什么策略"，
// 由中央 registrar 统一校验并翻译成 handler chain。
type Module interface {
	// Routes 返回该模块的路由契约。
	//
	// 路径是模块内相对路径（以 / 开头），API 版本前缀由中央 registrar 添加。
	Routes() []routespec.RouteSpec

	// Name 用于编译错误定位——出问题时必须能一眼看出是哪个模块写错了。
	Name() string
}

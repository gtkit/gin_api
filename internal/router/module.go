package router

import "github.com/gin-gonic/gin"

// Module 定义业务模块向路由层注册自身路由的能力。
//
// 每个 module 的 transport/http 包实现此接口，
// 由 bootstrap 层组装后传入路由初始化函数，
// 避免路由层直接依赖所有 module 的具体类型。
type Module interface {
	RegisterRoutes(group *gin.RouterGroup)
}

package http

import (
	userapp "github.com/gtkit/gin-api/internal/module/user/application"

	"github.com/gin-gonic/gin"
)

// UserModule 实现 router.Module 接口。
type UserModule struct {
	service        *userapp.Service
	authMiddleware gin.HandlerFunc
}

func NewModule(service *userapp.Service, authMiddleware gin.HandlerFunc) *UserModule {
	return &UserModule{service: service, authMiddleware: authMiddleware}
}

func (m *UserModule) RegisterRoutes(router *gin.RouterGroup) {
	handler := NewHandler(m.service)
	group := router.Group("/user").Use(m.authMiddleware)
	group.GET("", handler.Get)
	group.PUT("", handler.Update)
	group.POST("/deactivate", handler.Deactivate)
}

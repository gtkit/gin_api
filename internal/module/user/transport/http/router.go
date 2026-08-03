package http

import (
	"net/http"
	"time"

	userapp "github.com/gtkit/gin-api/internal/module/user/application"
	"github.com/gtkit/gin-api/internal/module/user/dto"
	"github.com/gtkit/gin-api/internal/router/routespec"
)

// 本模块路由的业务超时预算。
//
// 显式声明而不是继承某个全局默认：不同模块的合理预算本就不同，
// 而"全局默认"会让所有路由跟着最慢的那条走。
const (
	userDeadline   = 5 * time.Second
	userWriteGrace = 2 * time.Second
)

// UserModule 贡献 user 模块的路由契约。
//
// 它不再接收 *gin.RouterGroup、也不自己注册路由与中间件：访问控制、超时、限流
// 全部声明在描述符上，由中央 registrar 统一校验并翻译成 handler chain。
// 模块因此不需要知道版本前缀与中间件顺序，也无法绕过契约注册路由。
type UserModule struct {
	service *userapp.Service
}

func NewModule(service *userapp.Service) *UserModule {
	return &UserModule{service: service}
}

// Name 用于编译错误定位。
func (m *UserModule) Name() string { return "module.user" }

// Routes 返回 user 模块的路由契约。
//
// 路径是模块内相对路径；`/api/v1` 之类的版本前缀由中央 registrar 添加。
func (m *UserModule) Routes() []routespec.RouteSpec {
	handler := NewHandler(m.service)
	timeout := routespec.TimeoutPolicy{
		Mode:       routespec.TimeoutCooperative,
		Deadline:   userDeadline,
		WriteGrace: userWriteGrace,
	}
	// 三条路由都要求已认证身份。
	//
	// 此前这是靠 group.Use(authMiddleware) 表达的——一旦有人新增路由时忘了挂进
	// 同一个 group，它就会静默变成公开接口，而代码看上去毫无异常。
	// 写进描述符后，漏写访问策略会被注册期直接拒绝（零值非法）。
	authenticated := routespec.AccessPolicy{Mode: routespec.AccessAuthenticated}

	return []routespec.RouteSpec{
		{
			Method:        http.MethodGet,
			Path:          "/user",
			Handler:       handler.Get,
			AccessPolicy:  authenticated,
			TimeoutPolicy: timeout,
			OperationID:   "getCurrentUserProfile",
			Tags:          []string{"User"},
			Summary:       "Get the current user profile",
			Responses:     map[string]string{"500": "Internal error"},
			ResponseShape: routespec.ResponseShapeEnveloped,
			ResponseBody:  dto.UserProfileResponse{},
		},
		{
			Method:        http.MethodPut,
			Path:          "/user",
			Handler:       handler.Update,
			AccessPolicy:  authenticated,
			TimeoutPolicy: timeout,
			OperationID:   "updateCurrentUserProfile",
			Tags:          []string{"User"},
			Summary:       "Update the current user profile",
			Responses: map[string]string{
				"422": "Request payload failed validation",
				"500": "Internal error",
			},
			RequestBody: dto.UserUpdateRequest{},
			// 更新成功后不回传资料，handler 是 `resp.Ok(c, nil)`。
			ResponseShape: routespec.ResponseShapeEmpty,
		},
		{
			Method:        http.MethodPost,
			Path:          "/user/deactivate",
			Handler:       handler.Deactivate,
			AccessPolicy:  authenticated,
			TimeoutPolicy: timeout,
			OperationID:   "deactivateCurrentUser",
			Tags:          []string{"User"},
			Summary:       "Deactivate the current user account",
			Responses: map[string]string{
				"422": "Request payload failed validation",
				"500": "Internal error",
			},
			RequestBody: dto.UserDeactivateRequest{},
			// 停用成功后没有业务数据可返回，线上是 `{code,msg,data:null}`。
			ResponseShape: routespec.ResponseShapeEmpty,
		},
	}
}

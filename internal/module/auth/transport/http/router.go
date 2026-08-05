package http

import (
	"net/http"
	"time"

	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/module/auth/dto"
	"github.com/gtkit/gin-api/internal/router/routespec"
)

// 本模块路由的业务超时预算。
//
// 比 user 模块宽：密码哈希本身就要几百毫秒（那是它的设计目的），
// 再加上一次数据库往返与一次 Redis 往返。按读接口的预算裁，
// 正常的登录会在高负载下被自己的超时掐断。
const (
	authDeadline   = 10 * time.Second
	authWriteGrace = 2 * time.Second
)

// AuthModule 贡献本地认证的路由契约。
//
// 它只在 auth.mode 为 local 时被装配。外部身份模式下整个模块不注册——
// 那种部署里令牌由外部签发，这里的端点没有任何意义，留着只是多一组
// 无人看管的凭据入口。
type AuthModule struct {
	service *authapp.Service
}

// NewModule 构造模块。
func NewModule(service *authapp.Service) *AuthModule {
	return &AuthModule{service: service}
}

// Name 用于编译错误定位。
func (m *AuthModule) Name() string { return "module.auth" }

// Routes 返回认证模块的路由契约。
func (m *AuthModule) Routes() []routespec.RouteSpec {
	handler := NewHandler(m.service)
	timeout := routespec.TimeoutPolicy{
		Mode:       routespec.TimeoutCooperative,
		Deadline:   authDeadline,
		WriteGrace: authWriteGrace,
	}

	// 认证端点用**专用的严格策略**，不复用业务默认值。
	//
	// 这几条是全站唯一接受口令猜测的入口：与读接口共用一个额度，等于给
	// 爆破留出与正常浏览同样宽的通道。策略在 config 的 ratelimit.policies
	// 里定义，这里只引用 ID。
	//
	// 只按来源限、不按身份限：请求发生在拿到身份**之前**，此时没有可用的
	// 用户维度——按身份限流对未登录的爆破者不起任何作用。
	credentialLimits := []string{"auth.credential"}

	// 令牌端点的额度可以宽一些：它们要求持有一个已经签发的有效令牌，
	// 不构成口令猜测面。
	tokenLimits := []string{"auth.token"}

	// 注册/登录/刷新是公开端点：调用它们的正是尚未持有令牌的客户端。
	//
	// 显式声明 AccessPublic 而不是留空——零值非法，漏写会被注册期拒绝，
	// 因此"公开"必须是一个有人写下的决定，不能是忘记的结果。
	public := routespec.AccessPolicy{Mode: routespec.AccessPublic}

	return []routespec.RouteSpec{
		{
			Method:             http.MethodPost,
			Path:               "/auth/register",
			Handler:            handler.Register,
			AccessPolicy:       public,
			TimeoutPolicy:      timeout,
			RateLimitPolicyIDs: credentialLimits,
			OperationID:        "register",
			Tags:               []string{"Auth"},
			Summary:            "Register an account and receive tokens (local auth mode only)",
			Responses: map[string]string{
				"409": "Phone is already registered",
				"422": "Request payload failed validation",
				"500": "Internal error",
			},
			RequestBodyPolicy: routespec.RequestBodyPolicy{Mode: routespec.BodyModeJSON},
			RequestBody:       dto.RegisterRequest{},
			ResponseShape:     routespec.ResponseShapeEnveloped,
			ResponseBody:      dto.TokenResponse{},
		},
		{
			Method:             http.MethodPost,
			Path:               "/auth/login",
			Handler:            handler.Login,
			AccessPolicy:       public,
			TimeoutPolicy:      timeout,
			RateLimitPolicyIDs: credentialLimits,
			OperationID:        "login",
			Tags:               []string{"Auth"},
			Summary:            "Exchange credentials for tokens (local auth mode only)",
			Responses: map[string]string{
				// 401 同时覆盖"账号不存在"与"密码错误"：区分两者等于提供一个
				// 不需要口令就能枚举已注册账号的接口。
				"401": "Invalid credentials",
				"403": "Account is not active",
				"422": "Request payload failed validation",
				"500": "Internal error",
			},
			RequestBodyPolicy: routespec.RequestBodyPolicy{Mode: routespec.BodyModeJSON},
			RequestBody:       dto.LoginRequest{},
			ResponseShape:     routespec.ResponseShapeEnveloped,
			ResponseBody:      dto.TokenResponse{},
		},
		{
			Method:             http.MethodPost,
			Path:               "/auth/refresh",
			Handler:            handler.Refresh,
			AccessPolicy:       public,
			TimeoutPolicy:      timeout,
			RateLimitPolicyIDs: tokenLimits,
			OperationID:        "refreshToken",
			Tags:               []string{"Auth"},
			Summary:            "Rotate a refresh token into a new token pair (local auth mode only)",
			Responses: map[string]string{
				"401": "Refresh token rejected; log in again",
				"422": "Request payload failed validation",
				"500": "Internal error",
			},
			RequestBodyPolicy: routespec.RequestBodyPolicy{Mode: routespec.BodyModeJSON},
			RequestBody:       dto.RefreshRequest{},
			ResponseShape:     routespec.ResponseShapeEnveloped,
			ResponseBody:      dto.TokenResponse{},
		},
		{
			Method: http.MethodPost,
			Path:   "/auth/logout",
			// 登出要求认证：要撤销的正是本次请求所用的令牌，
			// 而它的标识只有认证中间件能提供。
			Handler:            handler.Logout,
			AccessPolicy:       routespec.AccessPolicy{Mode: routespec.AccessAuthenticated},
			TimeoutPolicy:      timeout,
			RateLimitPolicyIDs: tokenLimits,
			OperationID:        "logout",
			Tags:               []string{"Auth"},
			Summary:            "Revoke the current access token and refresh family (local auth mode only)",
			Responses: map[string]string{
				"422": "Request payload failed validation",
				"500": "Internal error",
			},
			RequestBodyPolicy: routespec.RequestBodyPolicy{Mode: routespec.BodyModeJSON},
			RequestBody:       dto.LogoutRequest{},
			// 登出成功不回传任何数据。
			ResponseShape: routespec.ResponseShapeEmpty,
		},
	}
}

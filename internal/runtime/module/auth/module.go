package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/pkg/credential"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/pkg/tokenstore"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/repository/authcredential"
)

// Deps 是装配本地认证所需的进程级依赖。
type Deps struct {
	DB    *gorm.DB
	Redis *redis.Client
	// Issuer 签发访问令牌，由进程级 JWT 装配提供。
	Issuer authapp.AccessIssuer
	Config config.AuthConfig
	// AccessTTL 是访问令牌寿命，来自 jwt.token_ttl_hours。
	//
	// 由调用方传入而不是从 Config 读：签发令牌的是 JWT 装配，寿命是它的事实。
	// 本模块若自己再存一份，客户端拿到的 expires_in 与令牌真实的 exp 迟早
	// 会对不上。
	AccessTTL time.Duration
	// ClockSkew 是验签允许的时钟偏移，来自 jwt.clock_skew_seconds。
	//
	// 由调用方传入而不是本模块自己读配置：验签用的 leeway 是 JWT 装配的
	// 事实，撤销记录必须活过 exp + 同一个偏移，两处取值分叉会让撤销记录
	// 比验签窗口先消失（local-authentication spec）。
	ClockSkew time.Duration
	// KeyPrefix 隔离同一 Redis 上不同服务的键空间。
	KeyPrefix string
	// Metrics 记录认证事件；为 nil 时不记录。
	Metrics *metrics.Recorder
	// Accounts 是账号维度限流（rate-limiting spec）；nil 表示该维度未配置
	//（策略 AccountRateLimitPolicyID 缺席即关闭），由装配层按注册表决定注入。
	Accounts authapp.AccountLimiter
}

// Assembly 是装配产物。
//
// 把模块与撤销表一起返回：撤销表既被认证服务使用（登出写入），也被认证
// **中间件**使用（每个请求读取）。分两次构造会产生两份指向同一 Redis 的
// 对象，看似无害，但任何一处将来加了本地缓存，两份状态就会分叉。
type Assembly struct {
	// Service 是认证业务服务；auth.mode 为 external 时为 nil。
	Service *authapp.Service
	// Revocation 供认证中间件检查令牌是否已被撤销。
	Revocation *tokenstore.RevocationList
}

// New 装配本地认证模块。
//
// 只在 auth.mode 为 local 时调用。外部身份模式下整个模块不存在——
// 那种部署里令牌由外部签发，这些端点没有意义，留着只是多一组
// 无人看管的凭据入口。
//
// 返回 error 而不是 panic：装配缺失是启动期错误，应当以错误表达。
func New(ctx context.Context, deps Deps) (Assembly, error) {
	if !deps.Config.LocalEnabled() {
		return Assembly{}, fmt.Errorf("auth: New called with mode %q, expected %q",
			deps.Config.Mode, config.AuthModeLocal)
	}
	if deps.Config.EffectiveBcryptMaxWaiters() < 0 || deps.Config.BcryptQueueTimeout < 0 {
		return Assembly{}, fmt.Errorf("auth: invalid password queue capacity or timeout")
	}
	if deps.DB == nil {
		return Assembly{}, fmt.Errorf("auth: database is required")
	}
	if deps.Redis == nil {
		return Assembly{}, errMissingRedis
	}
	if deps.Issuer == nil {
		return Assembly{}, fmt.Errorf("auth: access token issuer is required")
	}
	if deps.AccessTTL <= 0 {
		return Assembly{}, fmt.Errorf("auth: access ttl must be positive, got %s", deps.AccessTTL)
	}

	refresh, err := tokenstore.NewRefreshStore(ctx, deps.Redis, deps.KeyPrefix,
		deps.Config.EffectiveRefreshTTL(), deps.Config.RefreshReplaySecret)
	if err != nil {
		return Assembly{}, fmt.Errorf("auth: build refresh store: %w", err)
	}

	revocation, err := tokenstore.NewRevocationList(deps.Redis, deps.KeyPrefix, deps.ClockSkew)
	if err != nil {
		return Assembly{}, fmt.Errorf("auth: build revocation list: %w", err)
	}

	// 构造期即生成时间掩护用的哈希，失败即装配失败：一个无法提供等价耗时掩护的
	// Hasher 会让账号存在性经时间信道泄漏，而那不会有任何运行期信号。
	hasher, err := credential.NewHasher(deps.Config.EffectiveBcryptCost())
	if err != nil {
		return Assembly{}, fmt.Errorf("auth: build password hasher: %w", err)
	}

	service, err := authapp.NewService(authapp.Options{
		Credentials:    credentialRepo{repo: authcredential.New(deps.DB)},
		Hasher:         newPasswordHasher(hasher, deps.Config, deps.Metrics),
		AccessIssuer:   deps.Issuer,
		RefreshStore:   refreshStore{store: refresh},
		RevocationList: revocation,
		Auditor:        authAuditor{},
		Events:         eventRecorder{recorder: deps.Metrics},
		AccessTTL:      deps.AccessTTL,
		Accounts:       deps.Accounts,
	})
	if err != nil {
		return Assembly{}, fmt.Errorf("auth: build service: %w", err)
	}

	return Assembly{
		Service:    service,
		Revocation: revocation,
	}, nil
}

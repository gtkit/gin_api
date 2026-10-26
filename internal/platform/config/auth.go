package config

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// minSecretLen 是配置里各专用密钥的最小字节数。
//
// 与 pkg/ratelimit 的键密钥下限同值：两者都是"泄漏即失效"的服务端密钥，
// 对暴力枚举的抵抗力要求一致。
const minSecretLen = 32

// maxBcryptConcurrency 是口令校验并发上限的上限。
//
// 取 CPU 核数的 8 倍：bcrypt 是刻意的 CPU 密集计算，远超核数的并发只是让
// 每个请求都变慢，同时把"有上限"这件事变成形式。
var maxBcryptConcurrency = runtime.GOMAXPROCS(0) * 8

// AuthMode 是认证模式。
//
// 用字符串而不是 bool：`local: false` 无法表达"外部身份"与"还没想好"的区别，
// 而这两者需要完全不同的启动行为。
type AuthMode string

const (
	// AuthModeLocal 由本服务保管用户凭据，注册/登录/刷新/登出端点全部注册。
	AuthModeLocal AuthMode = "local"
	// AuthModeExternal 身份由外部 IdP 提供，本服务只验签与授权。
	//
	// 母版不预置任何具体 IdP 的对接：Keycloak、Auth0、自研网关在 claims 形状、
	// 用户同步时机与撤销通知方式上差异极大，预置任何一家都会让另外几家的使用者
	// 先删掉再重写——比没有更费事。接线点见 SCAFFOLD.md。
	AuthModeExternal AuthMode = "external"
)

// AuthConfig 是认证配置。
type AuthConfig struct {
	// Mode 是认证模式，**零值非法**。
	//
	// 不给默认值是刻意的。默认到 local，打算接外部 IdP 的部署会平白多出一组
	// 无人看管的凭据端点；默认到 external，使用者会以为认证已就绪，
	// 而实际上没有任何签发入口——两种默认都会静默地给出错误的系统。
	Mode AuthMode `mapstructure:"mode"`

	// BcryptCost 是密码哈希代价，留空按 defaultBcryptCost。
	//
	// 选 bcrypt 而非 argon2id：argon2id 要同时调 memory/time/parallelism 三个
	// 参数，而它们的正确取值取决于部署机器的内存与核数——母版无从知道。
	// 母版给的默认值要么在小机器上打满内存，要么在大机器上形同虚设，
	// 且两种情况都不会报错。bcrypt 只有一个参数，语义单一，调错的空间小。
	BcryptCost int `mapstructure:"bcrypt_cost"`

	// RefreshReplaySecret 是刷新令牌重放材料的加密密钥（本地认证模式必填）。
	//
	// **必须专用**，禁止复用 JWT、限流或任何其他业务密钥：它保护的是"轮换出的
	// 下一代刷新令牌"——那是可直接使用的凭据，明文落库时一次存储快照泄漏即可
	// 拼出当前有效的刷新令牌。经 SHA-256 归一到 32 字节做 AES-256-GCM 密钥。
	//
	// 轮换语义：换密钥 = 当时处于重放窗口（秒级）内的刷新失败一次，客户端
	// 重新登录即可；不触发令牌族撤销。
	RefreshReplaySecret string `mapstructure:"refresh_replay_secret"`

	// BcryptMaxConcurrent 是口令哈希校验的进程级并发上限，留空按 CPU 核数。
	//
	// bcrypt 是刻意昂贵的计算：限流约束的是单来源/单账号的速率，约束不了
	// **聚合并发**——一批分布式登录请求就能把全部 CPU 吃光。超限的校验排队。
	BcryptMaxConcurrent int `mapstructure:"bcrypt_max_concurrent"`
	// BcryptMaxWaiters 限制正在等候密码运算的请求数。
	// 省略/null 使用有效执行并发数，0 禁止排队，正数显式指定；负数非法。
	BcryptMaxWaiters *int `mapstructure:"bcrypt_max_waiters"`
	// BcryptQueueTimeout 是排队上限；0/省略用 1s，负数非法。
	// 仅等待者使用，且不超过调用方剩余预算；不会中断已开始的 bcrypt。
	BcryptQueueTimeout time.Duration `mapstructure:"bcrypt_queue_timeout"`

	// RefreshTTL 是刷新令牌寿命，留空按 defaultRefreshTTL。
	//
	// **访问令牌的寿命不在这里**：它由 jwt.token_ttl_hours 决定，因为签发
	// 令牌的是 JWT 装配。在这里再放一个 access_ttl 会产生两个来源——
	// 客户端拿到的 expires_in 与令牌里真实的 exp 一旦对不上，客户端会在
	// 令牌已经失效之后才去刷新，或者反过来提前把好用的令牌丢掉。
	RefreshTTL time.Duration `mapstructure:"refresh_ttl"`
}

const (
	// defaultBcryptCost 取 12。
	//
	// bcrypt 的代价按 2^cost 增长。10 在现代硬件上约几十毫秒，12 约在
	// 200~300 毫秒量级——对登录接口是可接受的延迟，对离线爆破则是 4 倍的成本。
	defaultBcryptCost = 12
	// minBcryptCost 是允许的下限。
	//
	// bcrypt 包自身允许低至 4，那个下限是为测试留的，用在生产上等于没有哈希。
	minBcryptCost = 10
	// maxBcryptCost 是 bcrypt 包的硬上限。
	//
	// 校验它而不是让 bcrypt 在第一次注册时才报错：配置错误应当在启动期暴露，
	// 而不是等到第一个用户注册。
	maxBcryptCost = 31

	defaultRefreshTTL = 30 * 24 * time.Hour
)

// LocalEnabled 报告是否启用本地认证。
func (c AuthConfig) LocalEnabled() bool { return c.Mode == AuthModeLocal }

// EffectiveBcryptCost 返回生效的哈希代价。
func (c AuthConfig) EffectiveBcryptCost() int {
	if c.BcryptCost > 0 {
		return c.BcryptCost
	}
	return defaultBcryptCost
}

// EffectiveBcryptConcurrency 返回生效的口令校验并发上限。
func (c AuthConfig) EffectiveBcryptConcurrency() int {
	if c.BcryptMaxConcurrent > 0 {
		return c.BcryptMaxConcurrent
	}
	return runtime.GOMAXPROCS(0)
}

// EffectiveBcryptMaxWaiters 保留显式 0 的快速拒绝语义。
func (c AuthConfig) EffectiveBcryptMaxWaiters() int {
	if c.BcryptMaxWaiters != nil {
		return *c.BcryptMaxWaiters
	}
	return c.EffectiveBcryptConcurrency()
}

// EffectiveBcryptQueueTimeout 返回等待名额的最大时间。
func (c AuthConfig) EffectiveBcryptQueueTimeout() time.Duration {
	if c.BcryptQueueTimeout > 0 {
		return c.BcryptQueueTimeout
	}
	return time.Second
}

// EffectiveRefreshTTL 返回生效的刷新令牌寿命。
func (c AuthConfig) EffectiveRefreshTTL() time.Duration {
	if c.RefreshTTL > 0 {
		return c.RefreshTTL
	}
	return defaultRefreshTTL
}

// validateAuthConfig 校验认证配置，并检查本地认证的前置条件。
//
// 需要 redis 参数：本地认证的撤销能力依赖它，而"缺撤销能力"必须在启动期
// 就拦下——见下方 fail-closed 的理由。
func validateAuthConfig(c AuthConfig, redis RedisConfig, accessTTL time.Duration) error {
	var errs []error

	switch c.Mode {
	case AuthModeLocal, AuthModeExternal:
	case "":
		errs = append(errs, errors.New(
			"auth.mode is required: set it to \"local\" (this service stores user credentials "+
				"and serves the register/login/refresh/logout endpoints) or \"external\" "+
				"(identity comes from an external provider; no auth endpoint is registered)"))
	default:
		errs = append(errs, fmt.Errorf(
			"auth.mode %q is invalid: must be exactly one of local|external", c.Mode))
	}

	if c.BcryptCost != 0 {
		if c.BcryptCost < minBcryptCost {
			errs = append(errs, fmt.Errorf(
				"auth.bcrypt_cost %d is too low: use at least %d (below that the hash is fast "+
					"enough to brute-force offline, which defeats the point of hashing)",
				c.BcryptCost, minBcryptCost))
		}
		if c.BcryptCost > maxBcryptCost {
			errs = append(errs, fmt.Errorf(
				"auth.bcrypt_cost %d exceeds the maximum of %d", c.BcryptCost, maxBcryptCost))
		}
	}

	// 并发上限非法时报错而不是静默取默认：一个手误的负值会让"我配了上限"
	// 与"上限按 CPU 核数"变成同一个观察结果，而两者的容量规划完全不同。
	if c.BcryptMaxConcurrent < 0 {
		errs = append(errs, fmt.Errorf(
			"auth.bcrypt_max_concurrent must not be negative, got %d (leave it empty to take "+
				"the CPU count)", c.BcryptMaxConcurrent))
	}
	if c.BcryptMaxConcurrent > maxBcryptConcurrency {
		errs = append(errs, fmt.Errorf(
			"auth.bcrypt_max_concurrent %d exceeds %d: bcrypt is deliberately CPU-bound, and a "+
				"limit far above the core count defeats the point of having one",
			c.BcryptMaxConcurrent, maxBcryptConcurrency))
	}

	if c.RefreshTTL < 0 {
		errs = append(errs, errors.New("auth.refresh_ttl must not be negative"))
	}
	if c.BcryptMaxWaiters != nil && *c.BcryptMaxWaiters < 0 {
		errs = append(errs, errors.New("auth.bcrypt_max_waiters must not be negative"))
	}
	if c.BcryptQueueTimeout < 0 {
		errs = append(errs, errors.New("auth.bcrypt_queue_timeout must not be negative"))
	}
	// 刷新令牌比访问令牌还短就没有存在意义：它的整个作用是在访问令牌过期后
	// 换取新的，寿命更短意味着它先死，用户被迫重新登录——那等于没有刷新机制。
	//
	// 只在本地认证模式下检查：外部身份模式不签发这两种令牌，那里的
	// jwt.token_ttl_hours 描述的是本服务**接受**的外部令牌，与刷新无关。
	if c.Mode == AuthModeLocal && accessTTL > 0 && c.EffectiveRefreshTTL() <= accessTTL {
		errs = append(errs, fmt.Errorf(
			"auth.refresh_ttl (%s) must be longer than the access token lifetime "+
				"(jwt.token_ttl_hours = %s): a refresh token that expires first can never be used",
			c.EffectiveRefreshTTL(), accessTTL))
	}

	// 重放材料的加密密钥：本地认证模式必填且有长度下限。
	//
	// 缺它就只能把轮换出的令牌明文写进 Redis——那与"存储里只有哈希"的契约
	// 相反，且一次快照泄漏即可拼出当前有效的刷新令牌。
	if c.Mode == AuthModeLocal {
		if secret := strings.TrimSpace(c.RefreshReplaySecret); len(secret) < minSecretLen {
			errs = append(errs, fmt.Errorf(
				"auth.refresh_replay_secret must be at least %d bytes when auth.mode is \"local\": "+
					"it encrypts the rotated refresh token kept for the replay window, and without "+
					"it that token would have to be stored in plaintext. Use a dedicated secret — "+
					"never reuse the JWT, rate limit or database secret", minSecretLen))
		}
	}

	// 本地认证要求可用的撤销存储，缺失时 fail-closed。
	//
	// 没有它，登出与刷新令牌撤销都无法实现。而"能登录但登不出"比完全没有认证
	// 更危险：系统看起来是完整的，使用者不会再去补这一块，直到需要回收某个
	// 泄漏的令牌时才发现无从下手。
	//
	// 错误里给两条出路而不是一条：使用者的真实意图可能是任何一种，
	// 只说"请配置 redis"会让打算接外部 IdP 的人以为自己必须装一个 Redis。
	if c.Mode == AuthModeLocal && redis.Addr == "" {
		errs = append(errs, errors.New(
			"auth.mode is \"local\" but redis.addr is empty: local auth needs a revocation "+
				"store for logout and refresh-token rotation, and an auth system that can "+
				"issue tokens but never revoke them is worse than none — it looks complete. "+
				"Either configure redis, or set auth.mode to \"external\" and plug in your "+
				"identity provider (see SCAFFOLD.md)"))
	}

	return errors.Join(errs...)
}

// jwt.token_ttl_hours 留空时的兜底值**分认证模式**（local-authentication spec）：
//
//	本地模式默认 1 小时——续期由刷新令牌承担，长寿命访问令牌只放大泄露窗口；
//	外部身份模式默认 720 小时——该配置在那种部署里描述的是"本服务接受的
//	外部令牌"，实际寿命由外部签发方决定，兜底保持宽松以兼容存量。
const (
	defaultLocalTokenTTL         = time.Hour
	defaultExternalTokenTTLHours = 720
)

// EffectiveTokenTTL 返回访问令牌的生效寿命。
//
// 它是访问令牌寿命的**唯一**来源：JWT 签发（jwtauth.Assemble）、认证模块的
// expires_in 与撤销记录的存活时间都从它推导，不存在第二份兜底。
func (c JWTConfig) EffectiveTokenTTL(mode AuthMode) time.Duration {
	if c.TokenTTLHours > 0 {
		return time.Duration(c.TokenTTLHours) * time.Hour
	}
	if mode == AuthModeLocal {
		return defaultLocalTokenTTL
	}
	return defaultExternalTokenTTLHours * time.Hour
}

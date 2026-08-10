package config

import (
	"time"
)

// Config 是 v2 的 typed runtime config。
type Config struct {
	Env      Env            `mapstructure:"env"`
	App      AppConfig      `mapstructure:"application"`
	Database DatabaseConfig `mapstructure:"database"`
	// Email / SMS 是出站通知通道：host / provider 为空即不启用（不构造客户端），
	// 启用时其余必填字段在 Validate fail-fast。告警多通道消费见 internal/pkg/news。
	Email EmailConfig `mapstructure:"email"`
	SMS   SMSConfig   `mapstructure:"sms"`
	Log   LogConfig   `mapstructure:"log"`
	Redis RedisConfig `mapstructure:"redis"`
	JWT   JWTConfig   `mapstructure:"jwt"`
	// Auth 是认证模式与本地认证参数。模式零值非法，见 AuthConfig.Mode。
	Auth  AuthConfig  `mapstructure:"auth"`
	SSL   SSLConfig   `mapstructure:"ssl"`
	News  NewsConfig  `mapstructure:"news"`
	CORS  CORSConfig  `mapstructure:"cors"`
	Pprof PprofConfig `mapstructure:"pprof"`
	Proxy ProxyConfig `mapstructure:"proxy"`
	// RateLimit 是限流的进程级配置（密钥与策略集合）。
	RateLimit RateLimitConfig `mapstructure:"ratelimit"`
	// Outbound 是出站 HTTP 客户端的具名声明。clients 为空即不启用。
	Outbound OutboundConfig `mapstructure:"outbound"`
	// Cache 是 GET 响应缓存配置。默认关闭——它把响应字节原样复用，
	// 挂错路由就是把 A 的数据发给 B，母版不替使用者打开。
	Cache CacheConfig `mapstructure:"cache"`
	// Telemetry 是 OpenTelemetry 导出配置。默认关闭，此时埋点仍在但走 no-op。
	Telemetry TelemetryConfig `mapstructure:"telemetry"`
	// Tasks 是异步任务队列配置。默认关闭——队列一旦开始入队就必须有人消费。
	Tasks TasksConfig `mapstructure:"tasks"`
	// AlertBot 是群机器人告警通道。默认关闭，启用时与 news 通道并存。
	AlertBot AlertBotConfig `mapstructure:"alertbot"`
	// Modules 是各业务模块的强类型配置子树，由生成器逐模块注入字段。
	Modules ModuleConfigs `mapstructure:"modules"`
}

type AppConfig struct {
	Domain      string `mapstructure:"domain"`
	Host        string `mapstructure:"host"`
	EnableHTTPS bool   `mapstructure:"ishttps"`
	Name        string `mapstructure:"name"`
	Port        string `mapstructure:"port"`
	// ReadTimeoutSeconds 是**已废弃**的合并键。
	//
	// 保留字段只为让旧配置得到一条可读的迁移错误，而不是严格解码给出的
	// "未知键"——后者不会告诉任何人该往哪改。
	//
	// 它此前被赋给 ReadHeaderTimeout，于是 `readtimeout: 2` 让人以为整个请求
	// 的读取上限是 2 秒，而读 body 仍可占用固定的 60 秒。
	ReadTimeoutSeconds int `mapstructure:"readtimeout"`
	// ReadHeaderTimeoutSeconds 是读取请求头的上限；<=0 时用内置默认值。
	ReadHeaderTimeoutSeconds int `mapstructure:"read_header_timeout"`
	// ReadTimeoutFullSeconds 是读取**整个请求（头 + 体）**的上限；<=0 时用内置默认值。
	//
	// 与上一项分开：只限读头挡不住慢 body，而那正是 slowloris 的形态。
	ReadTimeoutFullSeconds int `mapstructure:"read_timeout"`
	// WriteTimeoutSeconds 已废弃：非零即启动失败，见 validateAppConfig 的说明。
	// 字段保留是为了让旧配置得到**一条可读的迁移错误**，而不是严格解码给出的
	// "未知键"——后者不会告诉任何人该往哪改。
	WriteTimeoutSeconds int    `mapstructure:"writertimeout"`
	Timezone            string `mapstructure:"timezone"`
	// MaxBodyBytes 全局请求体大小上限（字节）。<=0 时由中间件兜底为默认 10MB。
	MaxBodyBytes int64 `mapstructure:"max_body_bytes"`
	// DrainWaitSeconds 是优雅关停时「/ready 翻成 503」与「关闭 HTTP 监听」之间的等待秒数。
	//
	// 它等的是**负载均衡器的传播延迟**：本实例已经宣告自己不再就绪，但 LB 还需要
	// 一次探测周期才会把它摘出转发目标。这段时间不等，那期间到达的请求会直接
	// 收到连接错误，而不是被优雅地处理完。
	//
	// <=0 时兜底为 DefaultDrainWait。取值应不小于 LB 的探测间隔。
	DrainWaitSeconds int `mapstructure:"drain_wait_seconds"`
}

// DefaultDrainWait 是未配置 drain_wait_seconds 时的等待时长。
//
// 5s 覆盖常见 LB（Nginx upstream check / k8s readinessProbe）的默认探测间隔。
const DefaultDrainWait = 5 * time.Second

// ProxyConfig 描述本进程前面的反向代理拓扑。
//
// 为什么必须显式配置：Gin 默认 ForwardedByClientIP=true 且可信代理集合为 0.0.0.0/0，
// 于是 c.ClientIP() 会直接采信客户端完全可控的 X-Forwarded-For / X-Real-IP——
// 任何人都能零成本把自己伪造成任意 IP，访问日志与业务审计里的 IP 因此不可信。
//
// 本配置的默认值（两个空列表）语义是**不信任任何代理**：客户端 IP 只取真实 TCP peer 地址。
// 部署在反向代理之后时必须把代理网段配进 TrustedProxies，否则记录到的会是代理自身的地址。
// 这个代价是可见的（IP 全变成同一个，一眼能发现），而"默认信任全部"的错误是不可见的。
type ProxyConfig struct {
	// TrustedProxies 是可信代理的 CIDR 或单个 IP 列表。空 = 不信任任何代理。
	// 生产环境拒绝 0.0.0.0/0 与 ::/0——它们等价于回到不安全默认值，只是写法更隐蔽。
	TrustedProxies []string `mapstructure:"trusted_proxies"`
	// RemoteIPHeaders 是允许用于提取客户端 IP 的转发头，显式列出，不依赖框架默认值。
	// 仅当请求的 TCP peer 落在 TrustedProxies 内时才会被采信。
	RemoteIPHeaders []string `mapstructure:"remote_ip_headers"`
}

type DatabaseConfig struct {
	DBType   string `mapstructure:"dbtype"`
	Host     string `mapstructure:"host"`
	Port     string `mapstructure:"port"`
	Name     string `mapstructure:"name"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	// MigrateUsername / MigratePassword 是可选的迁移专用凭据。
	//
	// 启用开关是**非空的 MigrateUsername**，不是单独的布尔字段：布尔开关会产生
	// 「enabled=true 但用户名为空」和「enabled=false 但配了用户名」两种需要额外规则的矛盾组合。
	// 密码允许为空（Socket 认证、受控本地环境是真实场景），因此密码不能作开关。
	//
	// 目的是让运行账号不必长期持有 DDL 权限：推荐运行账号只授 DML，DDL 只授迁移账号。
	MigrateUsername string `mapstructure:"migrate_username"`
	MigratePassword string `mapstructure:"migrate_password"`
	MaxIdleConn     int    `mapstructure:"maxidleconn"`
	MaxOpenConn     int    `mapstructure:"maxopenconn"`
	MaxLifeSeconds  int    `mapstructure:"maxlifeseconds"`
	// MaxIdleTimeSeconds 是空闲连接的最长存活时间。
	// 与 MaxLifeSeconds 一样必须为正：0 会被底层解释成"无限空闲"，等于无意关闭了
	// 连接轮换——数据库侧单方面断开的连接会一直留在池里，直到某次请求才发现它已死。
	MaxIdleTimeSeconds int `mapstructure:"maxidletimeseconds"`
	// Charset / Collation 是连接与迁移**共用**的单一来源。
	// 不依赖数据库环境默认值：同一份 SQL 在不同默认 collation 的实例上，
	// 比较与排序结果会不同，而这种差异只在数据量上来后才暴露。
	Charset   string `mapstructure:"charset"`
	Collation string `mapstructure:"collation"`
	// MinVersion 是受支持的最低 MySQL 版本（形如 "8.0"）。
	// 它同时用于跳过 GORM 的版本探测（WithServerVersion）与启动期的实测校验。
	MinVersion string `mapstructure:"min_version"`
}

// 数据库连接的默认值。留空时使用，避免存量配置因新增必填键而无法启动。
//
// 连接池默认值刻意都是正数：0 在底层的含义分别是"无限连接数"与"无限生命周期"，
// 都不是安全的默认——前者让数据库被打满，后者让已被服务端单方面断开的连接一直留在池里。
const (
	DefaultDBCharset            = "utf8mb4"
	DefaultDBCollation          = "utf8mb4_unicode_ci"
	DefaultDBMinVersion         = "8.0"
	DefaultDBMaxOpenConn        = 25
	DefaultDBMaxIdleConn        = 10
	DefaultDBMaxLifeSeconds     = 1800
	DefaultDBMaxIdleTimeSeconds = 300
)

// DBCredentials 是一次数据库连接使用的凭据。
//
// 让调用方先解析出本次要用的凭据再建连，而不是把整个 *Config 传下去让底层猜用途——
// 同一份配置里有运行与迁移两套账号，底层无从判断该用哪个。
type DBCredentials struct {
	Username string
	Password string
}

// EmailConfig 是 SMTP 出站通道。Host 为空 = 不启用；启用时 From 必填。
// AlertTo 非空即启用邮件告警通道（经 internal/pkg/news 扇出）。
type EmailConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"` // 0 走库默认 25；587 提交口；465 配 implicit_ssl
	User     string `mapstructure:"user"` // 空 = 不认证（可信内网中继）
	Pass     string `mapstructure:"pass"`
	From     string `mapstructure:"from"`
	FromName string `mapstructure:"from_name"`
	// ImplicitSSL 使用 465 端口式 SMTPS；默认策略为 STARTTLS 强制 TLS。
	ImplicitSSL bool     `mapstructure:"implicit_ssl"`
	AlertTo     []string `mapstructure:"alert_to"`
}

// Enabled 报告邮件通道是否启用。
func (c EmailConfig) Enabled() bool { return c.Host != "" }

// SMSConfig 是出站短信通道。Provider 为空 = 不启用；当前仅支持 "aliyun"。
// AlertPhones 与 AlertTemplate 同时非空即启用短信告警通道；
// 告警模板必须声明 ${env} 变量（发送参数固定为 {"env": 环境名}）。
type SMSConfig struct {
	Provider     string `mapstructure:"provider"`
	AccessID     string `mapstructure:"access_id"`
	AccessSecret string `mapstructure:"access_secret"`
	RegionID     string `mapstructure:"region_id"` // 空走驱动默认 cn-hangzhou
	SignName     string `mapstructure:"sign_name"`
	// Templates 逻辑模板名 → 供应商模板；业务与告警都以逻辑名引用，
	// 供应商编号只出现在配置里。
	Templates     map[string]SMSTemplateConfig `mapstructure:"templates"`
	AlertPhones   []string                     `mapstructure:"alert_phones"`
	AlertTemplate string                       `mapstructure:"alert_template"`
}

// SMSTemplateConfig 是一条逻辑模板到供应商模板的映射。
type SMSTemplateConfig struct {
	Code string `mapstructure:"code"`
	// SignName 逐模板签名，空回退 SMSConfig.SignName。
	SignName string `mapstructure:"sign_name"`
}

// Enabled 报告短信通道是否启用。
func (c SMSConfig) Enabled() bool { return c.Provider != "" }

type LogConfig struct {
	SQL           int    `mapstructure:"sql"`
	Compress      int    `mapstructure:"compress"`
	ConsoleStdout int    `mapstructure:"consolestdout"`
	FileStdout    int    `mapstructure:"filestdout"`
	Level         string `mapstructure:"level"`
	MaxAge        int    `mapstructure:"maxage"`
	MaxBackups    int    `mapstructure:"maxbackups"`
	MaxSize       int    `mapstructure:"maxsize"`
	Path          string `mapstructure:"path"`
	// AccessRequestBody 控制是否在 HTTP 访问日志中记录请求体。默认 false（不记录）。
	// 开启后请求体仍会经敏感字段脱敏；非结构化 body 只记录 content-type 与大小占位。
	AccessRequestBody bool `mapstructure:"access_request_body"`
	// AccessResponseBody 控制是否在 HTTP 访问日志中记录响应体。默认 false（不记录）。
	// 与 AccessRequestBody 独立；开启后响应体同样经脱敏。
	AccessResponseBody bool `mapstructure:"access_response_body"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DBs      []int  `mapstructure:"dbs"`
	Prefix   string `mapstructure:"prefix"`

	// 连接池与超时。全部允许留空——留空时由 redisx 自身的默认值生效，
	// 不在本层复制一份可能与上游漂移的默认值。填了就必须自洽（见 validateRedisConfig）。
	//
	// 单位统一用毫秒而不是秒：Redis 的合理超时普遍在百毫秒量级，
	// 用秒做单位会让"最小可表达值"就已经过大。
	PoolSize       int `mapstructure:"pool_size"`
	MinIdleConns   int `mapstructure:"min_idle_conns"`
	DialTimeoutMS  int `mapstructure:"dial_timeout_ms"`
	ReadTimeoutMS  int `mapstructure:"read_timeout_ms"`
	WriteTimeoutMS int `mapstructure:"write_timeout_ms"`
}

// RateLimitConfig 是限流的进程级配置。
type RateLimitConfig struct {
	// KeyHMACSecret 用于派生限流键里的主体标识。
	//
	// **必须是专用密钥**，禁止复用 JWT、数据库或任何其他业务密钥：
	// 复用意味着任何一处泄漏都会同时击穿多个用途，而限流键泄漏与
	// 令牌签发密钥泄漏在响应流程上完全不同。
	//
	// **轮换语义写死为「桶全部重算 = 一次性限额重置」**：换密钥后所有主体
	// 的键都变了，等于每个人重新拿到一个满桶。不做双密钥灰度——
	// 那需要同时维护两套键空间，而收益只是避免一次性重置。
	KeyHMACSecret string `mapstructure:"key_hmac_secret"`
	// Policies 是策略集合。为空表示不启用限流。
	Policies []RateLimitPolicyConfig `mapstructure:"policies"`
}

// RateLimitPolicyConfig 是一条限流策略的配置形态。
//
// 与 ratelimit.Policy 分开：配置层用字符串表达枚举（yaml 友好），
// 装配层翻译成强类型并做全部校验。让配置结构直接复用强类型枚举会逼着
// 使用者在 yaml 里写数字，而数字的含义只有读代码才知道。
type RateLimitPolicyConfig struct {
	ID          string  `mapstructure:"id"`
	Rate        float64 `mapstructure:"rate"`
	Burst       int     `mapstructure:"burst"`
	Scope       string  `mapstructure:"scope"`
	Store       string  `mapstructure:"store"`
	FailureMode string  `mapstructure:"failure_mode"`
	MaxKeys     int     `mapstructure:"max_keys"`
}

// JWTKeyConfig 是一把带 kid 的密钥。
//
// active 需要私钥与公钥；previous 只需要公钥（它们只用于验签，
// 保留私钥意味着一把已经退役的密钥仍能签发新令牌——那正是轮换要终结的）。
type JWTKeyConfig struct {
	KID            string `mapstructure:"kid"`
	PrivateKeyPath string `mapstructure:"private_key_path"`
	PublicKeyPath  string `mapstructure:"public_key_path"`
}

type JWTConfig struct {
	// Active 是当前签发用的密钥。
	Active JWTKeyConfig `mapstructure:"active"`
	// Previous 是仍接受验签的旧公钥，数量上限由 jwtauth 强制。
	//
	// 重叠窗口必须覆盖旧令牌的剩余 TTL，否则轮换仍会造成批量失效；
	// 但也不能无限期保留——每多一把旧公钥，无 kid 存量令牌的验签成本就多一次尝试。
	Previous []JWTKeyConfig `mapstructure:"previous"`
	// Issuer 与 Audiences 是**必填**的令牌作用域声明。
	//
	// 一个不声明自己是谁、也不声明发给谁的令牌，在多服务共享密钥或密钥
	// 意外复用时无法区分用途——拿到 A 服务的令牌就能用在 B 服务上。
	Issuer    string   `mapstructure:"issuer"`
	Audiences []string `mapstructure:"audiences"`
	// ClockSkewSeconds 是允许的时钟偏移，上限固定 120 秒。
	ClockSkewSeconds int `mapstructure:"clock_skew_seconds"`
	// CacheMaxEntries 是验签结果缓存的容量；0 禁用，负值即配置错误。
	CacheMaxEntries int `mapstructure:"cache_max_entries"`
	// UnkeyedTokenAcceptUntil 是无 kid 存量令牌的**绝对** RFC3339 截止时刻。
	//
	// 用绝对时刻而不是时长：时长会让每次重启都自动顺延，兼容口因此变成永久的。
	// 默认空 = 不接受任何无 kid 令牌。
	UnkeyedTokenAcceptUntil string `mapstructure:"unkeyed_token_accept_until"`
	// LaravelAcceptUntil 是 Laravel 存量令牌的绝对截止时刻。
	//
	// 与上一条**分开**：两者是不同的兼容维度（缺 kid vs 不同算法族），
	// 共用一个开关会让其中一条的删除被另一条阻塞。
	LaravelAcceptUntil string `mapstructure:"laravel_accept_until"`
	// PrivateKeyPath / PublicKeyPath 是**已废弃**的单密钥路径。
	//
	// 保留字段是为了让旧配置得到一条可读的迁移错误，而不是严格解码给出的
	// "未知键"——后者不会告诉任何人该往哪改。
	PrivateKeyPath string `mapstructure:"private_key_path"`
	PublicKeyPath  string `mapstructure:"public_key_path"`
	// TokenTTLHours 是签发 token 的有效期（小时）；<=0 时兜底为 720（30 天）。
	//
	// 这是访问令牌寿命的**唯一来源**：认证模块的 expires_in 与撤销记录的
	// 存活时间都从它推导，不另设一个可能与之漂移的副本。
	TokenTTLHours int `mapstructure:"token_ttl_hours"`
	// Laravel 是旧 Laravel（tymon/jwt-auth）存量 token 的过渡期兼容开关：
	// 只验不签，Laravel 下线且存量 token 全部过期后整块删除。
	Laravel LaravelJWTConfig `mapstructure:"laravel"`
}

// LaravelJWTConfig 配置 Laravel 存量 token 的单向兼容验证。
type LaravelJWTConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Algorithm 是验签算法白名单中的单一取值：hs256 | hs384 | hs512（大小写不敏感）；
	// 留空按 hs256。验证器只接受该算法，不信任 token header 的 alg 声明。
	Algorithm string `mapstructure:"algorithm"`
	// Secret 是 Laravel 侧的 JWT_SECRET；enabled 时必填，只进各机 env.yml。
	Secret string `mapstructure:"secret"`
}

type SSLConfig struct {
	Key string `mapstructure:"key"`
	PEM string `mapstructure:"pem"`
}

type NewsConfig struct {
	FSURL     string `mapstructure:"fsurl"`
	AppID     string `mapstructure:"appid"`
	AppSecret string `mapstructure:"appsecret"`
}

type CORSConfig struct {
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

// PprofConfig 控制独立的 pprof 性能剖析端点。
// 默认不启用（配置缺失时 Enabled 为零值 false）；仅在需要排查性能问题时按环境打开。
// 端点与业务 engine 物理隔离，绑独立端口，线上通过 SSH 端口转发访问。
type PprofConfig struct {
	// Enabled 是否启动独立的 pprof HTTP 端点。
	Enabled bool `mapstructure:"enabled"`
	// Host pprof 监听地址。强烈建议保持 127.0.0.1，线上通过 SSH 端口转发访问。
	// 留空时代码内兜底为 "127.0.0.1"。
	Host string `mapstructure:"host"`
	// Port pprof 监听端口，留空时代码内兜底为 "6060"。
	Port string `mapstructure:"port"`
	// BlockProfileRate block profile 采样率：每发生 N 纳秒阻塞采样一次，0 表示关闭。
	// 典型排查值 10000，排查完应改回 0。该采样有运行时开销。
	BlockProfileRate int `mapstructure:"block_profile_rate"`
	// MutexProfileFraction mutex profile 采样比例：1/N 的锁竞争事件被采样，0 表示关闭。
	// 典型排查值 100，排查完应改回 0。该采样有运行时开销。
	MutexProfileFraction int `mapstructure:"mutex_profile_fraction"`
}

// laravelAlgorithms 是 Laravel 存量 token 验证器允许的 HMAC 算法白名单。
var laravelAlgorithms = []string{"hs256", "hs384", "hs512"}

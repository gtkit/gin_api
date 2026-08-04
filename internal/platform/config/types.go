package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config 是 v2 的 typed runtime config。
type Config struct {
	Env      Env            `mapstructure:"env"`
	App      AppConfig      `mapstructure:"application"`
	Database DatabaseConfig `mapstructure:"database"`
	Email    EmailConfig    `mapstructure:"email"`
	SMS      SMSConfig      `mapstructure:"sms"`
	Pay      PayConfig      `mapstructure:"pay"`
	LLM      LLMConfig      `mapstructure:"llm"`
	Log      LogConfig      `mapstructure:"log"`
	Redis    RedisConfig    `mapstructure:"redis"`
	JWT      JWTConfig      `mapstructure:"jwt"`
	SSL      SSLConfig      `mapstructure:"ssl"`
	News     NewsConfig     `mapstructure:"news"`
	Aliyun   AliyunConfig   `mapstructure:"aliyun"`
	Baidu    BaiduConfig    `mapstructure:"baidu"`
	CORS     CORSConfig     `mapstructure:"cors"`
	Pprof    PprofConfig    `mapstructure:"pprof"`
	Proxy    ProxyConfig    `mapstructure:"proxy"`
	// RateLimit 是限流的进程级配置（密钥与策略集合）。
	RateLimit RateLimitConfig `mapstructure:"ratelimit"`
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

// EffectiveDrainWait 返回生效的 LB 传播等待时长。
func (a AppConfig) EffectiveDrainWait() time.Duration {
	if a.DrainWaitSeconds <= 0 {
		return DefaultDrainWait
	}
	return time.Duration(a.DrainWaitSeconds) * time.Second
}

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

// EffectiveMaxOpenConn 返回生效的最大打开连接数。
func (c DatabaseConfig) EffectiveMaxOpenConn() int {
	if c.MaxOpenConn > 0 {
		return c.MaxOpenConn
	}
	return DefaultDBMaxOpenConn
}

// EffectiveMaxIdleConn 返回生效的最大空闲连接数，且永不超过生效的打开上限。
func (c DatabaseConfig) EffectiveMaxIdleConn() int {
	idle := c.MaxIdleConn
	if c.MaxOpenConn <= 0 && idle <= 0 {
		idle = DefaultDBMaxIdleConn
	}
	if open := c.EffectiveMaxOpenConn(); idle > open {
		return open
	}
	return idle
}

// EffectiveMaxLifeSeconds 返回生效的连接最大存活时间（秒）。
func (c DatabaseConfig) EffectiveMaxLifeSeconds() int {
	if c.MaxLifeSeconds > 0 {
		return c.MaxLifeSeconds
	}
	return DefaultDBMaxLifeSeconds
}

// EffectiveCharset 返回生效的连接字符集。
func (c DatabaseConfig) EffectiveCharset() string {
	if s := strings.TrimSpace(c.Charset); s != "" {
		return s
	}
	return DefaultDBCharset
}

// EffectiveCollation 返回生效的连接 collation。
func (c DatabaseConfig) EffectiveCollation() string {
	if s := strings.TrimSpace(c.Collation); s != "" {
		return s
	}
	return DefaultDBCollation
}

// EffectiveMinVersion 返回受支持的最低 MySQL 版本。
func (c DatabaseConfig) EffectiveMinVersion() string {
	if s := strings.TrimSpace(c.MinVersion); s != "" {
		return s
	}
	return DefaultDBMinVersion
}

// EffectiveMaxIdleTimeSeconds 返回生效的空闲连接最长存活时间（秒）。
func (c DatabaseConfig) EffectiveMaxIdleTimeSeconds() int {
	if c.MaxIdleTimeSeconds > 0 {
		return c.MaxIdleTimeSeconds
	}
	return DefaultDBMaxIdleTimeSeconds
}

// DBCredentials 是一次数据库连接使用的凭据。
//
// 让调用方先解析出本次要用的凭据再建连，而不是把整个 *Config 传下去让底层猜用途——
// 同一份配置里有运行与迁移两套账号，底层无从判断该用哪个。
type DBCredentials struct {
	Username string
	Password string
}

// RuntimeCredentials 返回服务进程使用的凭据。
// 服务永远只用这一套，不会因为配置了迁移凭据而改变自身连接身份。
func (c DatabaseConfig) RuntimeCredentials() DBCredentials {
	return DBCredentials{Username: c.Username, Password: c.Password}
}

// MigrationCredentials 返回迁移使用的凭据；ok=false 表示未启用，调用方应回退到运行凭据并告警。
func (c DatabaseConfig) MigrationCredentials() (DBCredentials, bool) {
	if strings.TrimSpace(c.MigrateUsername) == "" {
		return DBCredentials{}, false
	}
	return DBCredentials{Username: c.MigrateUsername, Password: c.MigratePassword}, true
}

type EmailConfig struct {
	Alias string `mapstructure:"alias"`
	Host  string `mapstructure:"host"`
	Pass  string `mapstructure:"pass"`
	Port  int    `mapstructure:"port"`
	User  string `mapstructure:"user"`
}

type SMSConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// SendLimitEnabled 开启 60 秒发送频控；显式配置,替代按 Env.IsProd() 推导。
	SendLimitEnabled bool `mapstructure:"send_limit_enabled"`
	// DeleteCodeOnVerify 验证成功后立即作废 code 防重放；显式配置,替代按 Env.IsProd() 推导。
	DeleteCodeOnVerify bool     `mapstructure:"delete_code_on_verify"`
	AllowTestCodes     bool     `mapstructure:"allow_test_codes"`
	TestPhones         []string `mapstructure:"test_phones"`
	TestCode           string   `mapstructure:"test_code"`
	CodeTTLSeconds     int      `mapstructure:"code_ttl_seconds"`
	MaxVerifyAttempts  int      `mapstructure:"max_verify_attempts"`
}

type PayConfig struct {
	Wechat WechatPayConfig `mapstructure:"wechat"`
	Alipay AlipayConfig    `mapstructure:"alipay"`
}

type WechatPayConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	AppID   string `mapstructure:"app_id"`
	MchID   string `mapstructure:"mch_id"`

	// === 微信支付 V3 ===
	MchCertSerialNumber      string `mapstructure:"mch_cert_serial_number"`
	MchAPIv3Key              string `mapstructure:"mch_apiv3_key"`
	MchPrivateKeyPath        string `mapstructure:"mch_private_key_path"`
	MchPrivateKeyPEM         string `mapstructure:"mch_private_key_pem"`
	WechatPayCertificatePath string `mapstructure:"wechatpay_certificate_path"`
	WechatPayCertificatePEM  string `mapstructure:"wechatpay_certificate_pem"`
	// 微信支付公钥模式（2024 年起新进件商户只下发公钥，不再签发平台证书）。
	// 配置 WechatPayPublicKeyID 且提供公钥来源之一（路径或 PEM）即启用公钥验签。
	WechatPayPublicKeyID   string `mapstructure:"wechatpay_public_key_id"`
	WechatPayPublicKeyPath string `mapstructure:"wechatpay_public_key_path"`
	WechatPayPublicKeyPEM  string `mapstructure:"wechatpay_public_key_pem"`
}

type AlipayConfig struct {
	Enabled                 bool   `mapstructure:"enabled"`
	AppID                   string `mapstructure:"app_id"`
	PrivateKey              string `mapstructure:"private_key"`
	PrivateKeyPath          string `mapstructure:"private_key_path"`
	IsProduction            bool   `mapstructure:"is_production"`
	AppCertPublicKey        string `mapstructure:"app_cert_public_key"`
	AppCertPublicKeyPath    string `mapstructure:"app_cert_public_key_path"`
	AlipayRootCert          string `mapstructure:"alipay_root_cert"`
	AlipayRootCertPath      string `mapstructure:"alipay_root_cert_path"`
	AlipayCertPublicKey     string `mapstructure:"alipay_cert_public_key"`
	AlipayCertPublicKeyPath string `mapstructure:"alipay_cert_public_key_path"`
	AlipayPublicKey         string `mapstructure:"alipay_public_key"`
}

type LLMConfig struct {
	// Providers 按优先级从高到低排列；每家的 Models 同样按优先级从高到低。
	// 深度优先故障转移：同家模型全部失败后再切换到下一家。
	Providers          []LLMProviderConfig `mapstructure:"providers"`
	SystemPrompt       string              `mapstructure:"system_prompt"`
	TimeoutSeconds     int                 `mapstructure:"timeout_seconds"`
	SessionTTLSeconds  int                 `mapstructure:"session_ttl_seconds"`
	MaxHistoryMessages int                 `mapstructure:"max_history_messages"`
	MaxMessageChars    int                 `mapstructure:"max_message_chars"`
	RedisDB            int                 `mapstructure:"redis_db"`
}

type LLMProviderConfig struct {
	Name   string   `mapstructure:"name"`
	APIKey string   `mapstructure:"api_key"`
	Models []string `mapstructure:"models"`
}

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

type AliyunConfig struct {
	SMS AliyunSMSConfig `mapstructure:"-"`
}

type AliyunSMSConfig struct {
	AccessID     string
	AccessSecret string
	SignName     string
	TemplateCode string
}

type BaiduConfig struct {
	Voice BaiduVoiceConfig `mapstructure:"voice"`
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

type BaiduVoiceConfig struct {
	AppID     string `mapstructure:"appid"`
	APIKey    string `mapstructure:"apikey"`
	SecretKey string `mapstructure:"secretkey"`
}

// Validate 校验 v2 运行所需的关键配置。
func (c *Config) Validate() error {
	var errs []error

	// env 只接受精确值 dev|test|prod，不归一化、不降级：
	// 未知值（如 production/prd/Prod）若静默当作非生产，会整体绕过 pprof
	// 绑定限制、生产日志路径校验与 gin ReleaseMode 等所有生产保护。
	switch c.Env {
	case EnvDev, EnvTest, EnvProd:
	case "":
		errs = append(errs, errors.New("env is required"))
	default:
		errs = append(errs, fmt.Errorf("env %q is invalid: must be exactly one of dev|test|prod", c.Env))
	}
	if err := validateAppConfig(c.App); err != nil {
		errs = append(errs, err)
	}
	if err := validateDatabaseConfig(c.Database); err != nil {
		errs = append(errs, err)
	}
	if err := validateRedisConfig(c.Redis); err != nil {
		errs = append(errs, err)
	}
	if err := validateLogConfig(c.Env, c.Log, os.Getenv("APP_ROOT")); err != nil {
		errs = append(errs, err)
	}
	if err := validateSMSConfig(c.SMS, c.Aliyun.SMS); err != nil {
		errs = append(errs, err)
	}
	if err := validatePayConfig(c.Pay); err != nil {
		errs = append(errs, err)
	}
	if err := validatePprofConfig(c.Env, c.Pprof); err != nil {
		errs = append(errs, err)
	}
	if err := validateJWTConfig(c.JWT); err != nil {
		errs = append(errs, err)
	}
	if err := validateJWTRotation(c.JWT, c.Env); err != nil {
		errs = append(errs, err)
	}
	if err := validateProxyConfig(c.Proxy, c.Env); err != nil {
		errs = append(errs, err)
	}
	if err := validateModuleConfigs(c.Modules); err != nil {
		errs = append(errs, err)
	}
	if err := validateRateLimitConfig(c.RateLimit); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// laravelAlgorithms 是 Laravel 存量 token 验证器允许的 HMAC 算法白名单。
var laravelAlgorithms = []string{"hs256", "hs384", "hs512"}

func validateJWTConfig(jwt JWTConfig) error {
	laravel := jwt.Laravel
	if !laravel.Enabled {
		return nil
	}

	var errs []error

	if strings.TrimSpace(laravel.Secret) == "" {
		errs = append(errs, errors.New("jwt.laravel.secret is required when jwt.laravel.enabled is true"))
	}
	alg := strings.ToLower(strings.TrimSpace(laravel.Algorithm))
	if alg != "" && !slices.Contains(laravelAlgorithms, alg) {
		errs = append(errs, fmt.Errorf("jwt.laravel.algorithm %q is invalid: must be one of %s", laravel.Algorithm, strings.Join(laravelAlgorithms, "|")))
	}

	return errors.Join(errs...)
}

// validatePprofConfig 强制 pprof 只绑**字面回环 IP**，且不分环境。
//
// 三条都有具体理由：
//
//  1. **不分环境**。pprof 没有任何鉴权，谁能连上谁就能拿到堆栈、内存快照与
//     CPU profile——里面有请求参数、密钥片段与完整调用路径。"这是测试环境"
//     不构成放宽的理由，而按环境区分的规则终究会被复制到生产。
//  2. **必须是字面 IP，不接受主机名**。`localhost` 依赖 DNS 与 /etc/hosts；
//     它们被改写时（容器编排、恶意注入）会让端点绑到一个非回环地址上，
//     而配置文件看起来完全正常。
//  3. **用 netip.Addr.IsLoopback() 判定**，不是字符串白名单。
//     127.0.0.2 与 0:0:0:0:0:0:0:1 同样是回环地址，字符串白名单会误拒；
//     而白名单一旦被人"顺手扩充"，扩进一个非回环地址不会有任何提示。
//
// 远程排障走 SSH 端口转发。
func validatePprofConfig(_ Env, pprof PprofConfig) error {
	if !pprof.Enabled {
		return nil
	}

	var errs []error

	host := strings.TrimSpace(pprof.Host)
	if host != "" {
		addr, err := netip.ParseAddr(host)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf(
				"pprof.host %q must be a literal loopback IP, not a hostname: "+
					"a hostname resolves through DNS/hosts and can be redirected to a non-loopback "+
					"address while the config still looks correct", pprof.Host))
		case !addr.IsLoopback():
			errs = append(errs, fmt.Errorf(
				"pprof.host %q is not a loopback address: pprof has no authentication, "+
					"anyone who can reach it gets stacks, heap snapshots and CPU profiles; "+
					"access it via an SSH port-forward instead", pprof.Host))
		}
	}

	// 采样率是进程全局状态且有真实运行时开销，负值无意义。
	if pprof.BlockProfileRate < 0 {
		errs = append(errs, fmt.Errorf(
			"pprof.block_profile_rate must not be negative, got %d", pprof.BlockProfileRate))
	}
	if pprof.MutexProfileFraction < 0 {
		errs = append(errs, fmt.Errorf(
			"pprof.mutex_profile_fraction must not be negative, got %d", pprof.MutexProfileFraction))
	}

	return errors.Join(errs...)
}

func validateAppConfig(app AppConfig) error {
	var errs []error

	if app.Name == "" {
		errs = append(errs, errors.New("application.name is required"))
	}
	if app.Host == "" {
		errs = append(errs, errors.New("application.host is required"))
	}
	if app.Port == "" {
		errs = append(errs, errors.New("application.port is required"))
	} else if _, err := strconv.Atoi(app.Port); err != nil {
		errs = append(errs, fmt.Errorf("application.port must be numeric: %w", err))
	}
	if app.ReadTimeoutSeconds != 0 {
		errs = append(errs, errors.New(
			"application.readtimeout is no longer supported: it only bounded reading the request "+
				"headers while the body still had a fixed 60s budget, so the configured value "+
				"understated the exposure. Split it into application.read_header_timeout and "+
				"application.read_timeout"))
	}
	if app.ReadHeaderTimeoutSeconds < 0 {
		errs = append(errs, errors.New("application.read_header_timeout must be >= 0"))
	}
	if app.ReadTimeoutFullSeconds < 0 {
		errs = append(errs, errors.New("application.read_timeout must be >= 0"))
	}
	if app.WriteTimeoutSeconds != 0 {
		// 该键已废弃且**不提供静默兼容**。
		//
		// 它曾经设置 http.Server.WriteTimeout —— 一个所有路由共享的绝对写上限。
		// 现在写超时由每条路由的 TimeoutPolicy 在连接级设置，服务器层恒为 0。
		// 保留这个键并静默忽略，会让人以为自己配置了保护而实际没有；
		// 因此这里直接拒绝启动，并在错误里给出迁移方向。
		errs = append(errs, errors.New(
			"application.writertimeout is no longer supported: the server-level write timeout is "+
				"now fixed at 0 and each route sets its own connection-level write deadline via routespec.TimeoutPolicy; "+
				"remove this key from your config"))
	}
	if app.Timezone != "" {
		if _, err := time.LoadLocation(app.Timezone); err != nil {
			errs = append(errs, fmt.Errorf("application.timezone is invalid: %w", err))
		}
	}

	return errors.Join(errs...)
}

func validateDatabaseConfig(database DatabaseConfig) error {
	var errs []error

	if database.DBType == "" {
		errs = append(errs, errors.New("database.dbtype is required"))
	}
	if database.Host == "" {
		errs = append(errs, errors.New("database.host is required"))
	}
	if database.Port == "" {
		errs = append(errs, errors.New("database.port is required"))
	} else if _, err := strconv.Atoi(database.Port); err != nil {
		errs = append(errs, fmt.Errorf("database.port must be numeric: %w", err))
	}
	if database.Name == "" {
		errs = append(errs, errors.New("database.name is required"))
	}
	if database.Username == "" {
		errs = append(errs, errors.New("database.username is required"))
	}
	// 连接池必须交叉校验，不能留到底层构造时才报错。
	//
	// 口径：未设置（零值）走 Effective* 的默认值，不算错误——否则省略这些键的存量部署
	// 会因为一次校验加强而起不动，而它们原本工作正常。显式写了负数，或写出
	// max_idle > max_open 这种自相矛盾的组合，才是必须拦下的真错误。
	//
	// max_idle > max_open 尤其危险：底层会静默把空闲上限压到打开上限，
	// 于是配置写的和实际生效的不一致，而没有任何地方会告诉你。
	if database.MaxOpenConn < 0 {
		errs = append(errs, fmt.Errorf("database.maxopenconn must not be negative, got %d", database.MaxOpenConn))
	}
	if database.MaxIdleConn < 0 {
		errs = append(errs, fmt.Errorf("database.maxidleconn must not be negative, got %d", database.MaxIdleConn))
	}
	if database.MaxOpenConn > 0 && database.MaxIdleConn > database.MaxOpenConn {
		errs = append(errs, fmt.Errorf(
			"database.maxidleconn (%d) must not exceed database.maxopenconn (%d)",
			database.MaxIdleConn, database.MaxOpenConn))
	}
	if database.MaxLifeSeconds < 0 {
		errs = append(errs, fmt.Errorf("database.maxlifeseconds must not be negative, got %d", database.MaxLifeSeconds))
	}
	if database.MaxIdleTimeSeconds < 0 {
		errs = append(errs, fmt.Errorf(
			"database.maxidletimeseconds must not be negative, got %d", database.MaxIdleTimeSeconds))
	}
	if err := validateMinVersion(database.EffectiveMinVersion()); err != nil {
		errs = append(errs, err)
	}

	// 只配 migrate_password 而 migrate_username 为空是**非法组合**，不是「未启用」：
	// 静默忽略密码并回退到运行凭据，会让运维以为迁移正在用独立账号执行——
	// 那是危险的静默降级，必须 fail-fast。
	if strings.TrimSpace(database.MigrateUsername) == "" && database.MigratePassword != "" {
		errs = append(errs, errors.New(
			"database.migrate_password is set but database.migrate_username is empty: "+
				"migration credentials are enabled by a non-empty migrate_username"))
	}

	return errors.Join(errs...)
}

func validateRedisConfig(redis RedisConfig) error {
	var errs []error

	if redis.Addr == "" {
		errs = append(errs, errors.New("redis.addr is required"))
	}
	if len(redis.DBs) == 0 {
		errs = append(errs, errors.New("redis.dbs must not be empty"))
	}
	if hasDuplicateInts(redis.DBs) {
		// 重复 DB 号不会报错，只是白白建一份重复的连接池，且让 dbs 的长度不再等于实际 DB 数。
		errs = append(errs, errors.New("redis.dbs contains duplicate database numbers"))
	}
	for _, db := range redis.DBs {
		if db < 0 {
			errs = append(errs, fmt.Errorf("redis.dbs contains a negative database number %d", db))
			break
		}
	}

	errs = append(errs, validateRedisPool(redis)...)

	return errors.Join(errs...)
}

// validateRedisPool 做池与超时的**交叉**校验。
//
// 单字段合法不等于组合合法：min_idle_conns 大于 pool_size 时，go-redis 永远无法把空闲连接
// 补到目标值，表现为持续的建连尝试——一个只在压测或高峰期才显形的配置错误。
// 这类错误在启动时是零成本可判的，留到运行期就要靠猜。
func validateRedisPool(redis RedisConfig) []error {
	var errs []error

	if redis.PoolSize < 0 {
		errs = append(errs, fmt.Errorf("redis.pool_size must not be negative, got %d", redis.PoolSize))
	}
	if redis.MinIdleConns < 0 {
		errs = append(errs, fmt.Errorf("redis.min_idle_conns must not be negative, got %d", redis.MinIdleConns))
	}
	if redis.PoolSize > 0 && redis.MinIdleConns > redis.PoolSize {
		errs = append(errs, fmt.Errorf(
			"redis.min_idle_conns (%d) must not exceed redis.pool_size (%d): "+
				"the pool can never reach the idle target and will retry connections forever",
			redis.MinIdleConns, redis.PoolSize))
	}

	for _, tc := range []struct {
		key   string
		value int
	}{
		{"redis.dial_timeout_ms", redis.DialTimeoutMS},
		{"redis.read_timeout_ms", redis.ReadTimeoutMS},
		{"redis.write_timeout_ms", redis.WriteTimeoutMS},
	} {
		if tc.value < 0 {
			errs = append(errs, fmt.Errorf("%s must not be negative, got %d", tc.key, tc.value))
		}
	}

	return errs
}

// hasDuplicateInts 报告切片中是否存在重复元素。
func hasDuplicateInts(values []int) bool {
	seen := make(map[int]struct{}, len(values))
	for _, v := range values {
		if _, dup := seen[v]; dup {
			return true
		}
		seen[v] = struct{}{}
	}
	return false
}

func validateSMSConfig(sms SMSConfig, aliyunSMS AliyunSMSConfig) error {
	if !sms.Enabled {
		return nil
	}

	var errs []error

	if aliyunSMS.AccessID == "" {
		errs = append(errs, errors.New("aliyun.sms_access_id is required when sms.enabled is true"))
	}
	if aliyunSMS.AccessSecret == "" {
		errs = append(errs, errors.New("aliyun.sms_access_secret is required when sms.enabled is true"))
	}
	if aliyunSMS.SignName == "" {
		errs = append(errs, errors.New("aliyun.sms_sign_name is required when sms.enabled is true"))
	}
	if aliyunSMS.TemplateCode == "" {
		errs = append(errs, errors.New("aliyun.sms_template_code is required when sms.enabled is true"))
	}

	return errors.Join(errs...)
}

func validatePayConfig(pay PayConfig) error {
	return errors.Join(
		validateWechatPayConfig(pay.Wechat),
		validateAlipayConfig(pay.Alipay),
	)
}

func validateWechatPayConfig(wechat WechatPayConfig) error {
	if !wechat.Enabled {
		return nil
	}

	var errs []error

	if wechat.AppID == "" {
		errs = append(errs, errors.New("pay.wechat.app_id is required when pay.wechat.enabled is true"))
	}
	if wechat.MchID == "" {
		errs = append(errs, errors.New("pay.wechat.mch_id is required when pay.wechat.enabled is true"))
	}

	// V3 校验。
	if wechat.MchCertSerialNumber == "" {
		errs = append(errs, errors.New("pay.wechat.mch_cert_serial_number is required when pay.wechat.enabled is true"))
	}
	if wechat.MchAPIv3Key == "" {
		errs = append(errs, errors.New("pay.wechat.mch_apiv3_key is required when pay.wechat.enabled is true"))
	}
	if wechat.MchPrivateKeyPath == "" && wechat.MchPrivateKeyPEM == "" {
		errs = append(errs, errors.New("pay.wechat.mch_private_key_path or pay.wechat.mch_private_key_pem is required when pay.wechat.enabled is true"))
	}
	if !hasWechatCertSource(wechat) && !hasWechatPublicKeySource(wechat) {
		errs = append(errs, errors.New("pay.wechat requires either wechatpay_certificate_path/pem (证书模式) or wechatpay_public_key_id with wechatpay_public_key_path/pem (公钥模式) when pay.wechat.enabled is true"))
	}

	return errors.Join(errs...)
}

func hasWechatCertSource(wechat WechatPayConfig) bool {
	return wechat.WechatPayCertificatePath != "" || wechat.WechatPayCertificatePEM != ""
}

func hasWechatPublicKeySource(wechat WechatPayConfig) bool {
	return wechat.WechatPayPublicKeyID != "" &&
		(wechat.WechatPayPublicKeyPath != "" || wechat.WechatPayPublicKeyPEM != "")
}

func validateAlipayConfig(alipay AlipayConfig) error {
	if !alipay.Enabled {
		return nil
	}

	var errs []error

	if alipay.AppID == "" {
		errs = append(errs, errors.New("pay.alipay.app_id is required when pay.alipay.enabled is true"))
	}
	if alipay.PrivateKey == "" && alipay.PrivateKeyPath == "" {
		errs = append(errs, errors.New("pay.alipay.private_key or pay.alipay.private_key_path is required when pay.alipay.enabled is true"))
	}
	if hasAlipayCertSource(alipay) && !hasCompleteAlipayCertSource(alipay) {
		errs = append(errs, errors.New("pay.alipay cert mode requires app cert, root cert and alipay cert when pay.alipay.enabled is true"))
	}
	if !hasAlipayCertSource(alipay) && alipay.AlipayPublicKey == "" {
		errs = append(errs, errors.New("pay.alipay.alipay_public_key is required when pay.alipay.enabled is true and cert mode is not configured"))
	}

	return errors.Join(errs...)
}

func hasAlipayCertSource(alipay AlipayConfig) bool {
	return alipay.AppCertPublicKey != "" || alipay.AppCertPublicKeyPath != "" ||
		alipay.AlipayRootCert != "" || alipay.AlipayRootCertPath != "" ||
		alipay.AlipayCertPublicKey != "" || alipay.AlipayCertPublicKeyPath != ""
}

func hasCompleteAlipayCertSource(alipay AlipayConfig) bool {
	return (alipay.AppCertPublicKey != "" || alipay.AppCertPublicKeyPath != "") &&
		(alipay.AlipayRootCert != "" || alipay.AlipayRootCertPath != "") &&
		(alipay.AlipayCertPublicKey != "" || alipay.AlipayCertPublicKeyPath != "")
}

func validateLogConfig(env Env, logCfg LogConfig, appRoot string) error {
	var errs []error

	if logCfg.Level == "" {
		errs = append(errs, errors.New("log.level is required"))
	}
	if logCfg.Path == "" {
		errs = append(errs, errors.New("log.path is required"))
	} else if err := validateLogPathLocation(env, logCfg.Path, appRoot); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func validateLogPathLocation(env Env, logPath, appRoot string) error {
	logPath = strings.TrimSpace(logPath)
	if logPath == "" || !env.IsProd() || !filepath.IsAbs(logPath) {
		return nil
	}

	appRoot = strings.TrimSpace(appRoot)
	if appRoot == "" {
		return errors.New("log.path absolute path is not allowed in prod unless APP_ROOT is set; use a relative path or set APP_ROOT")
	}
	if !filepath.IsAbs(appRoot) {
		return fmt.Errorf("APP_ROOT must be absolute when prod log.path is absolute: %q", appRoot)
	}
	if !pathWithinRoot(logPath, appRoot) {
		return fmt.Errorf("log.path absolute path %q must be under APP_ROOT %q in prod", filepath.Clean(logPath), filepath.Clean(appRoot))
	}

	return nil
}

func pathWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}

	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

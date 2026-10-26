// 本文件汇总配置校验：Validate 及其各段实现，以及公用的校验 helper。
//
// 与 types.go 分开是因为两者的变更节奏与关注点都不同：改字段类型时不该在
// 校验函数之间翻找它的全部出现处，加一条校验也不该在类型定义中间插入。
package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/shared/env"
)

// signedNum 覆盖需要做数值区间校验的全部类型。
//
// `~int64` 天然涵盖 time.Duration（它就是 int64 的别名），因此一个泛型 helper
// 同时处理 int、int64 与 time.Duration，不必按类型各写一份。
type signedNum interface {
	~int | ~int64
}

// mustNonEmpty 校验字符串字段非空。
func mustNonEmpty(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

// mustPositive 校验数值字段 > 0。
//
// 错误文案不写死"when enabled"：本项目大量字段并不带 Enabled 开关，
// 那样的措辞会让人去找一个根本不存在的开关。
func mustPositive[T signedNum](field string, value T) error {
	if value <= 0 {
		return fmt.Errorf("%s must be > 0, got %v", field, value)
	}
	return nil
}

// mustNonNegative 校验数值字段 >= 0：允许显式 0，拒绝负数。
func mustNonNegative[T signedNum](field string, value T) error {
	if value < 0 {
		return fmt.Errorf("%s must be >= 0, got %v", field, value)
	}
	return nil
}

// maxTCPPort 是 TCP 端口的上界。
const maxTCPPort = 65535

// mustPortRange 校验端口既是数值、又落在 [1, 65535]。
//
// **范围检查是必需的**，只查 Atoi 不够：`99999` 与 `-1` 都能解析成整数，
// 于是它们能通过启动校验，直到监听或建连时才失败——那时的错误来自系统调用，
// 与"配置写错了"这个真实原因隔着好几层。
//
// 空字符串跳过：缺失由 mustNonEmpty 单独报，避免同一个字段触发两条错误。
func mustPortRange(field, value string) error {
	if value == "" {
		return nil
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s %q must be numeric: %w", field, value, err)
	}
	if port < 1 || port > maxTCPPort {
		return fmt.Errorf("%s %q is out of the TCP port range [1, %d]", field, value, maxTCPPort)
	}
	return nil
}

// mustTimezone 校验 IANA 时区名；空字符串表示不设置，跳过。
func mustTimezone(field, value string) error {
	if value == "" {
		return nil
	}
	if _, err := time.LoadLocation(value); err != nil {
		return fmt.Errorf("%s %q is invalid: %w", field, value, err)
	}
	return nil
}

// Validate 校验 v2 运行所需的关键配置。
func (c *Config) Validate() error {
	var errs []error

	// env 只接受精确值 dev|test|prod，不归一化、不降级：
	// 未知值（如 production/prd/Prod）若静默当作非生产，会整体绕过 pprof
	// 绑定限制、生产日志路径校验与 gin ReleaseMode 等所有生产保护。
	switch c.Env {
	case env.Dev, env.Test, env.Prod:
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
	if err := validateRedisCapabilities(*c); err != nil {
		errs = append(errs, err)
	}
	if err := validateRedisDBZeroConsumers(*c); err != nil {
		errs = append(errs, err)
	}
	if err := validateLogConfig(c.Env, c.Log, os.Getenv("APP_ROOT")); err != nil {
		errs = append(errs, err)
	}
	if err := validateEmailConfig(c.Email); err != nil {
		errs = append(errs, err)
	}
	if err := validateSMSConfig(c.SMS); err != nil {
		errs = append(errs, err)
	}
	if err := validatePprofConfig(c.Env, c.Pprof); err != nil {
		errs = append(errs, err)
	}
	if err := validateAuthConfig(c.Auth, c.Redis, c.JWT.EffectiveTokenTTL(c.Auth.Mode)); err != nil {
		errs = append(errs, err)
	}
	if err := validateJWTConfig(c.JWT); err != nil {
		errs = append(errs, err)
	}
	if err := validateJWTRotation(c.JWT, c.Auth.Mode, c.Env); err != nil {
		errs = append(errs, err)
	}
	if err := validateAuthJWTCompatibility(c.Auth, c.JWT); err != nil {
		errs = append(errs, err)
	}
	if err := validateProxyConfig(c.Proxy, c.Env); err != nil {
		errs = append(errs, err)
	}
	if err := validateRateLimitConfig(c.RateLimit); err != nil {
		errs = append(errs, err)
	}
	// OUTBOUND: 恢复 Outbound 字段后，取消下面配置校验的注释。
	// wiring:begin config.outbound.validate outbound line
	// if err := c.Outbound.Validate(); err != nil {
	// 	errs = append(errs, err)
	// }
	// wiring:end config.outbound.validate
	if err := validateCacheConfig(c.Cache, c.Redis); err != nil {
		errs = append(errs, err)
	}
	if err := validateTelemetryConfig(c.Telemetry); err != nil {
		errs = append(errs, err)
	}
	// TASKQUEUE: 恢复 Tasks 字段后，取消下面配置校验的注释。
	// wiring:begin config.taskqueue.validate taskqueue line
	// if err := c.Tasks.Validate(c.Redis.Addr, c.Redis.DBs); err != nil {
	// 	errs = append(errs, err)
	// }
	// wiring:end config.taskqueue.validate
	if err := validateNewsConfig(c.News); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// validateNewsConfig 校验告警通道配置。
//
// 必须在启动期拦下：告警通道的配置错误只在告警发送时暴露，
// 而那正是最不能失去告警的时刻。
func validateNewsConfig(news NewsConfig) error {
	var errs []error

	if p := strings.ToLower(strings.TrimSpace(news.Platform)); p != "" && !slices.Contains(newsPlatforms, p) {
		errs = append(errs, fmt.Errorf(
			"news.platform %q is invalid: must be one of %s (or empty for the %s default)",
			news.Platform, strings.Join(newsPlatforms, "|"), NewsPlatformFeishu))
	}

	if raw := strings.TrimSpace(news.FSURL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, fmt.Errorf(
				"news.fsurl %q is not an absolute http(s) URL: the alert webhook would fail "+
					"on its first delivery, which is exactly when an alert must not be lost", news.FSURL))
		}
	}

	return errors.Join(errs...)
}

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

	// 密钥长度按算法有下限（laravel-legacy-token-verify spec）。下限取哈希
	// 输出长度：HMAC 的安全强度上界是密钥熵，短于哈希输出长度的密钥让暴力
	// 搜索密钥比伪造签名更便宜——"非空"完全表达不了这个约束。
	// 只在密钥与算法各自合法时判长度，避免同一根因重复报错。
	if secret := laravel.Secret; strings.TrimSpace(secret) != "" &&
		(alg == "" || slices.Contains(laravelAlgorithms, alg)) {
		if minLen := laravelSecretMinBytes(alg); len(secret) < minLen {
			errs = append(errs, fmt.Errorf(
				"jwt.laravel.secret is %d bytes but %s requires at least %d bytes: "+
					"rotate in a longer shared secret before enabling the fallback",
				len(secret), effectiveLaravelAlgorithm(alg), minLen))
		}
	}

	return errors.Join(errs...)
}

// laravelSecretMinBytes 返回算法要求的最小密钥长度（字节），空算法按默认 hs256。
func laravelSecretMinBytes(normalizedAlg string) int {
	switch normalizedAlg {
	case "hs384":
		return 48
	case "hs512":
		return 64
	default:
		return 32
	}
}

// effectiveLaravelAlgorithm 把空算法解析为生效的默认值，供报错文本使用。
func effectiveLaravelAlgorithm(normalizedAlg string) string {
	if normalizedAlg == "" {
		return "hs256 (the default)"
	}
	return normalizedAlg
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
func validatePprofConfig(_ env.Env, pprof PprofConfig) error {
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
	errs := []error{
		mustNonEmpty("application.name", app.Name),
		mustNonEmpty("application.host", app.Host),
		mustNonEmpty("application.port", app.Port),
		mustPortRange("application.port", app.Port),
	}
	errs = append(errs,
		mustNonNegative("application.read_header_timeout", app.ReadHeaderTimeoutSeconds),
		mustNonNegative("application.read_timeout", app.ReadTimeoutFullSeconds),
		mustNonNegative("application.max_inflight_requests", app.MaxInflightRequests),
	)
	errs = append(errs, mustTimezone("application.timezone", app.Timezone))

	return errors.Join(errs...)
}

func validateDatabaseConfig(database DatabaseConfig) error {
	errs := []error{
		mustNonEmpty("database.dbtype", database.DBType),
		mustNonEmpty("database.host", database.Host),
		mustNonEmpty("database.port", database.Port),
		mustPortRange("database.port", database.Port),
		mustNonEmpty("database.name", database.Name),
		mustNonEmpty("database.username", database.Username),
		mustNonNegative("database.maxopenconn", database.MaxOpenConn),
		mustNonNegative("database.maxidleconn", database.MaxIdleConn),
		mustNonNegative("database.maxlifeseconds", database.MaxLifeSeconds),
	}

	if strings.TrimSpace(database.DBType) != "" && database.DBType != "mysql" {
		errs = append(errs, errors.New("database.dbtype must be mysql"))
	}

	// 连接池必须交叉校验，不能留到底层构造时才报错。
	//
	// 口径：未设置（零值）走 Effective* 的默认值，不算错误——否则省略这些键的存量部署
	// 会因为一次校验加强而起不动，而它们原本工作正常。显式写了负数，或写出
	// max_idle > max_open 这种自相矛盾的组合，才是必须拦下的真错误。
	//
	// max_idle > max_open 尤其危险：底层会静默把空闲上限压到打开上限，
	// 于是配置写的和实际生效的不一致，而没有任何地方会告诉你。
	if database.MaxOpenConn > 0 && database.MaxIdleConn > database.MaxOpenConn {
		errs = append(errs, fmt.Errorf(
			"database.maxidleconn (%d) must not exceed database.maxopenconn (%d)",
			database.MaxIdleConn, database.MaxOpenConn))
	}
	errs = append(errs,
		mustNonNegative("database.maxidletimeseconds", database.MaxIdleTimeSeconds))
	if database.PrepareStmtMaxSize < 0 || database.PrepareStmtMaxSize > maxDBPrepareStmtMaxSize {
		errs = append(errs, fmt.Errorf(
			"database.prepare_stmt_max_size must be 0 (default %d) or between 1 and %d",
			DefaultDBPrepareStmtMaxSize, maxDBPrepareStmtMaxSize))
	}
	// 在转换为 time.Duration 前限制整数秒，避免溢出或底层回退到无界默认值。
	if database.PrepareStmtTTLSeconds < 0 || database.PrepareStmtTTLSeconds > maxDBPrepareStmtTTLSeconds {
		errs = append(errs, fmt.Errorf(
			"database.prepare_stmt_ttl_seconds must be 0 (default %d) or between 1 and %d",
			DefaultDBPrepareStmtTTLSeconds, maxDBPrepareStmtTTLSeconds))
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

// fixedRedisDB 是若干能力在装配处固定取用的 Redis 逻辑库号。
//
// 它们不像 cache / tasks 那样各有配置项：把数据放到非 0 库目前没有需求驱动，
// 而缺库号导致的误诊断只需要一条校验就能消除。
const fixedRedisDB = 0

// validateRedisDBZeroConsumers 校验取固定逻辑库的能力所需的库号已被声明。
//
// 为什么必须在配置层拦：资源层只打开 redis.dbs 声明过的库，取未声明的库得到
// nil 客户端。而 nil 客户端与"Redis 启动期不可达"在装配处**无法区分**，于是
// 缺库号的后果是指向错误方向的诊断——fail_open 的限流策略静默进入进程级永久
// 降级、本地认证的刷新令牌与撤销记录无处存放、日志声称 "redis is configured
// but unreachable"，而 Redis 完全健康。运维会去查一个没有问题的 Redis。
//
// cache.redis_db 有同款校验，理由与此完全一致。
//
// 聚合成一条错误而不是逐项报：各项说的是同一件事，逐项会让运维看到几行
// 几乎相同的消息。
func validateRedisDBZeroConsumers(c Config) error {
	var consumers []string

	if slices.ContainsFunc(c.RateLimit.Policies, func(p RateLimitPolicyConfig) bool {
		return strings.TrimSpace(p.Store) == storeRedis
	}) {
		consumers = append(consumers, "rate limit policies using the redis store")
	}
	if c.Auth.LocalEnabled() {
		consumers = append(consumers, "local authentication (refresh tokens and revocations)")
	}

	if len(consumers) == 0 || slices.Contains(c.Redis.DBs, fixedRedisDB) {
		return nil
	}

	return fmt.Errorf(
		"redis.dbs %v must include database %d because these enabled capabilities take it at "+
			"assembly time: %s. Without it the resource layer hands them a nil client, which is "+
			"indistinguishable from an unreachable Redis: rate limiting would silently degrade for "+
			"the whole process and local authentication could not store refresh tokens, while "+
			"Redis itself is healthy. Fix it by adding %d to redis.dbs",
		c.Redis.DBs, fixedRedisDB, strings.Join(consumers, "; "), fixedRedisDB)
}

func validateRedisConfig(redis RedisConfig) error {
	var errs []error
	if redis.Prefix != "" && strings.TrimSpace(redis.Prefix) == "" {
		errs = append(errs, errors.New("redis.prefix must not be whitespace-only"))
	}
	if redis.Addr != "" {
		errs = append(errs, mustHostPort("redis.addr", redis.Addr))
	}

	if redis.Addr != "" && len(redis.DBs) == 0 {
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

// validateRedisCapabilities 拦截声明使用 Redis 却未配置地址的能力。
// required/optional 决定故障策略；即便可降级的 Redis 能力也必须明确配置地址。
func validateRedisCapabilities(c Config) error {
	if strings.TrimSpace(c.Redis.Addr) != "" {
		return nil
	}
	var consumers []string
	if c.Auth.LocalEnabled() {
		consumers = append(consumers, "local authentication")
	}
	if slices.ContainsFunc(c.RateLimit.Policies, func(p RateLimitPolicyConfig) bool {
		return strings.TrimSpace(p.Store) == storeRedis
	}) {
		consumers = append(consumers, "redis rate limiting")
	}
	if c.Cache.Enabled && strings.TrimSpace(c.Cache.Store) == CacheStoreRedis {
		consumers = append(consumers, "redis response cache")
	}
	// TASKQUEUE: 启用后取消注释，使队列必须配置 Redis 地址。
	// wiring:begin config.taskqueue.redis taskqueue line
	// if c.Tasks.Enabled {
	// 	consumers = append(consumers, "task queue")
	// }
	// wiring:end config.taskqueue.redis
	if len(consumers) == 0 {
		return nil
	}
	return fmt.Errorf("redis.addr is required by enabled capabilities: %s", strings.Join(consumers, ", "))
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

// validateEmailConfig 校验 SMTP 出站通道。
//
// 启用键（host）为空时其余字段必须同为零值：填了一半的配置意味着使用者
// 以为通道已启用，而它静默不存在——那比校验失败更难排查。
func validateEmailConfig(email EmailConfig) error {
	if !email.Enabled() {
		if email.Port != 0 || email.User != "" || email.Pass != "" || email.From != "" ||
			email.FromName != "" || email.ImplicitSSL || len(email.AlertTo) > 0 {
			return errors.New("email.host is empty but other email.* fields are set: " +
				"set email.host to enable the channel, or remove the leftover fields")
		}
		return nil
	}

	var errs []error
	if email.From == "" {
		errs = append(errs, errors.New("email.from is required when email.host is set"))
	} else if _, err := mail.ParseAddress(email.From); err != nil {
		errs = append(errs, fmt.Errorf("email.from %q is not a valid address: %w", email.From, err))
	}
	if email.Port < 0 || email.Port > maxTCPPort {
		errs = append(errs, fmt.Errorf("email.port %d is out of the TCP port range [0, %d] (0 = library default)", email.Port, maxTCPPort))
	}
	for _, to := range email.AlertTo {
		if _, err := mail.ParseAddress(to); err != nil {
			errs = append(errs, fmt.Errorf("email.alert_to %q is not a valid address: %w", to, err))
		}
	}
	return errors.Join(errs...)
}

// validateSMSConfig 校验出站短信通道。
func validateSMSConfig(sms SMSConfig) error {
	if !sms.Enabled() {
		if sms.AccessID != "" || sms.AccessSecret != "" || sms.RegionID != "" || sms.SignName != "" ||
			len(sms.Templates) > 0 || len(sms.AlertPhones) > 0 || sms.AlertTemplate != "" {
			return errors.New("sms.provider is empty but other sms.* fields are set: " +
				"set sms.provider to enable the channel, or remove the leftover fields")
		}
		return nil
	}

	var errs []error
	if sms.Provider != "aliyun" {
		errs = append(errs, fmt.Errorf("sms.provider %q is invalid: only \"aliyun\" is supported", sms.Provider))
	}
	if sms.AccessID == "" {
		errs = append(errs, errors.New("sms.access_id is required when sms.provider is set"))
	}
	if sms.AccessSecret == "" {
		errs = append(errs, errors.New("sms.access_secret is required when sms.provider is set"))
	}
	if sms.SignName == "" {
		errs = append(errs, errors.New("sms.sign_name is required when sms.provider is set"))
	}
	for name, tpl := range sms.Templates {
		if tpl.Code == "" {
			errs = append(errs, fmt.Errorf("sms.templates.%s.code is required", name))
		}
	}
	// 告警通道两个键必须成对：只配其一等于"以为开了告警而它不存在"。
	if (len(sms.AlertPhones) > 0) != (sms.AlertTemplate != "") {
		errs = append(errs, errors.New("sms.alert_phones and sms.alert_template must be set together"))
	}
	if sms.AlertTemplate != "" {
		if _, ok := sms.Templates[sms.AlertTemplate]; !ok {
			errs = append(errs, fmt.Errorf("sms.alert_template %q is not defined in sms.templates", sms.AlertTemplate))
		}
	}
	for _, phone := range sms.AlertPhones {
		if strings.TrimSpace(phone) == "" {
			errs = append(errs, errors.New("sms.alert_phones contains an empty entry"))
		}
	}
	return errors.Join(errs...)
}

func validateLogConfig(environment env.Env, logCfg LogConfig, appRoot string) error {
	errs := []error{
		mustNonEmpty("log.level", logCfg.Level),
		mustNonEmpty("log.path", logCfg.Path),
	}

	// 与日志消费者一致，仅在校验时去除首尾空白，不改写原配置。
	switch strings.TrimSpace(logCfg.Level) {
	case "", "debug", "info", "warn", "error", "dpanic", "panic", "fatal":
	default:
		errs = append(errs, errors.New("log.level must be one of debug, info, warn, error, dpanic, panic, fatal"))
	}
	if environment.IsProd() && strings.TrimSpace(logCfg.Level) == "debug" {
		errs = append(errs, errors.New("log.level debug is not allowed in prod"))
	}
	if threshold := logCfg.SQLSlowThresholdMS; threshold != nil && (*threshold < 0 || *threshold > math.MaxInt64/int64(time.Millisecond)) {
		errs = append(errs, errors.New("log.sql_slow_threshold_ms must be nonnegative and fit time.Duration"))
	}
	if err := validateLogPathLocation(environment, logCfg.Path, appRoot); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func validateLogPathLocation(environment env.Env, logPath, appRoot string) error {
	logPath = strings.TrimSpace(logPath)
	if logPath == "" || !environment.IsProd() || !filepath.IsAbs(logPath) {
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

// mustHostPort 校验 `host:port` 形式的地址，端口须落在合法 TCP 范围。
//
// 与 mustPortRange 分开：那个收单独的端口字段，这个收合并写法。
// 两者都必须查范围——只查"能不能拆开"的话，`127.0.0.1:99999` 会一路通到
// 建连时才失败，而那时的错误来自网络栈，看不出是配置写错了。
func mustHostPort(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%s %q must be in host:port form: %w", field, value, err)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("%s %q has an empty host", field, value)
	}
	return mustPortRange(field+" port", port)
}

// validateAuthJWTCompatibility 拒绝运行期不可用的认证组合
// （local-authentication spec）。
//
// Laravel 回退 + 本地认证：Laravel 存量令牌不携带 jti，而本地认证对每个
// 请求强制查询撤销表（那是它能登出的前提）——拿不到 jti 就无法判定是否
// 已撤销，唯一安全的处置是拒绝，于是每个 Laravel 令牌恒定失败。
//
// 在启动期拒绝而不是让它跑起来：这个组合的表现是"配置看起来两个都开了，
// 而其中一条永远走不通"，只有在真实流量上才暴露，且日志里看不出根因。
func validateAuthJWTCompatibility(auth AuthConfig, jwt JWTConfig) error {
	if auth.Mode == AuthModeLocal && jwt.Laravel.Enabled {
		return errors.New(
			"jwt.laravel.enabled cannot be combined with auth.mode \"local\": Laravel legacy " +
				"tokens carry no jti, while local auth checks the revocation list on every " +
				"request (that is what makes logout work) — every Laravel token would be " +
				"rejected. Either finish the Laravel migration and disable the fallback, " +
				"or set auth.mode to \"external\" while the fallback is still needed")
	}
	return nil
}

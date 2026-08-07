// 本文件汇总配置校验：Validate 及其各段实现，以及公用的校验 helper。
//
// 与 types.go 分开是因为两者的变更节奏与关注点都不同：改字段类型时不该在
// 校验函数之间翻找它的全部出现处，加一条校验也不该在类型定义中间插入。
package config

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
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
	if err := validateJWTRotation(c.JWT, c.Env); err != nil {
		errs = append(errs, err)
	}
	if err := validateAuthJWTCompatibility(c.Auth, c.JWT); err != nil {
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
	if err := validateOutboundConfig(c.Outbound); err != nil {
		errs = append(errs, err)
	}
	if err := validateCacheConfig(c.Cache, c.Redis); err != nil {
		errs = append(errs, err)
	}
	if err := validateTelemetryConfig(c.Telemetry); err != nil {
		errs = append(errs, err)
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
	errs := []error{
		mustNonEmpty("application.name", app.Name),
		mustNonEmpty("application.host", app.Host),
		mustNonEmpty("application.port", app.Port),
		mustPortRange("application.port", app.Port),
	}
	if app.ReadTimeoutSeconds != 0 {
		errs = append(errs, errors.New(
			"application.readtimeout is no longer supported: it only bounded reading the request "+
				"headers while the body still had a fixed 60s budget, so the configured value "+
				"understated the exposure. Split it into application.read_header_timeout and "+
				"application.read_timeout"))
	}
	errs = append(errs,
		mustNonNegative("application.read_header_timeout", app.ReadHeaderTimeoutSeconds),
		mustNonNegative("application.read_timeout", app.ReadTimeoutFullSeconds),
	)
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
	if database.MaxLifeSeconds < 0 {
		errs = append(errs, fmt.Errorf("database.maxlifeseconds must not be negative, got %d", database.MaxLifeSeconds))
	}
	errs = append(errs,
		mustNonNegative("database.maxidletimeseconds", database.MaxIdleTimeSeconds))
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
	errs := []error{
		mustNonEmpty("redis.addr", redis.Addr),
		mustHostPort("redis.addr", redis.Addr),
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

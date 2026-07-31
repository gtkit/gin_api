package config

import (
	"errors"
	"fmt"
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
}

type AppConfig struct {
	Domain              string `mapstructure:"domain"`
	Host                string `mapstructure:"host"`
	EnableHTTPS         bool   `mapstructure:"ishttps"`
	Name                string `mapstructure:"name"`
	Port                string `mapstructure:"port"`
	ReadTimeoutSeconds  int    `mapstructure:"readtimeout"`
	WriteTimeoutSeconds int    `mapstructure:"writertimeout"`
	Timezone            string `mapstructure:"timezone"`
	// MaxBodyBytes 全局请求体大小上限（字节）。<=0 时由中间件兜底为默认 10MB。
	MaxBodyBytes int64 `mapstructure:"max_body_bytes"`
}

type DatabaseConfig struct {
	DBType         string `mapstructure:"dbtype"`
	Host           string `mapstructure:"host"`
	Port           string `mapstructure:"port"`
	Name           string `mapstructure:"name"`
	Username       string `mapstructure:"username"`
	Password       string `mapstructure:"password"`
	MaxIdleConn    int    `mapstructure:"maxidleconn"`
	MaxOpenConn    int    `mapstructure:"maxopenconn"`
	MaxLifeSeconds int    `mapstructure:"maxlifeseconds"`
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
}

type JWTConfig struct {
	// PrivateKeyPath / PublicKeyPath 是 Ed25519 PEM 路径；
	// 留空时由 jwtauth 兜底为 config/pem/jwtpri.pem 与 config/pem/jwtpub.pem。
	PrivateKeyPath string `mapstructure:"private_key_path"`
	PublicKeyPath  string `mapstructure:"public_key_path"`
	// TokenTTLHours 是签发 token 的有效期（小时）；<=0 时兜底为 720（30 天）。
	TokenTTLHours int `mapstructure:"token_ttl_hours"`
	// LegacyLaravel 是旧 Laravel（tymon/jwt-auth）存量 token 的过渡期兼容开关：
	// 只验不签，Laravel 下线且存量 token 全部过期后整块删除。
	LegacyLaravel LegacyLaravelJWTConfig `mapstructure:"legacy_laravel"`
}

// LegacyLaravelJWTConfig 配置 Laravel 存量 token 的单向兼容验证。
type LegacyLaravelJWTConfig struct {
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

	return errors.Join(errs...)
}

// legacyLaravelAlgorithms 是 legacy 验证器允许的 HMAC 算法白名单。
var legacyLaravelAlgorithms = []string{"hs256", "hs384", "hs512"}

func validateJWTConfig(jwt JWTConfig) error {
	legacy := jwt.LegacyLaravel
	if !legacy.Enabled {
		return nil
	}

	var errs []error

	if strings.TrimSpace(legacy.Secret) == "" {
		errs = append(errs, errors.New("jwt.legacy_laravel.secret is required when jwt.legacy_laravel.enabled is true"))
	}
	alg := strings.ToLower(strings.TrimSpace(legacy.Algorithm))
	if alg != "" && !slices.Contains(legacyLaravelAlgorithms, alg) {
		errs = append(errs, fmt.Errorf("jwt.legacy_laravel.algorithm %q is invalid: must be one of %s", legacy.Algorithm, strings.Join(legacyLaravelAlgorithms, "|")))
	}

	return errors.Join(errs...)
}

// validatePprofConfig 生产环境强制 pprof 只绑回环地址。
// pprof 暴露堆栈/内存/CPU 等敏感运行时信息，线上必须走 SSH 端口转发，
// 绝不允许监听 0.0.0.0 或对外地址。
func validatePprofConfig(env Env, pprof PprofConfig) error {
	if !env.IsProd() || !pprof.Enabled {
		return nil
	}
	switch strings.TrimSpace(pprof.Host) {
	case "", "127.0.0.1", "::1", "localhost":
		return nil
	default:
		return fmt.Errorf("pprof.host %q is not allowed in prod: bind loopback (127.0.0.1/::1/localhost) and access via SSH port-forward", pprof.Host)
	}
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
	if app.ReadTimeoutSeconds < 0 {
		errs = append(errs, errors.New("application.readtimeout must be >= 0"))
	}
	if app.WriteTimeoutSeconds < 0 {
		errs = append(errs, errors.New("application.writertimeout must be >= 0"))
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

	return errors.Join(errs...)
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

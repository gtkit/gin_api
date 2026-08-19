// Package verifycode 提供一次性验证码的签发与校验（渠道无关，短信/邮件共用）。
//
// 正确性由本包兑现，调用方只需注入存储与发送器：
//   - 校验在 Redis 侧原子完成「取码 + 尝试计数 + 超限销毁 + 命中即删」，
//     同一验证码不可能通过第二次校验（含并发提交）；
//   - 签发先过发送节流（SetNX 原子），间隔内的重复请求不生成、不发送——
//     节流保护的是真实计费动作；
//   - 验证码由加密安全随机源生成；Redis 键由目标经 SHA-256 派生，
//     明文手机号/邮箱不落存储键。
//
// 母版不构造实例：派生项目在装配处按需构造，并用闭包把
// internal/pkg/sms / internal/pkg/mail 适配成 Sender。
package verifycode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// 校验结果的错误集合。调用方按类型映射对客文案，不解析文本。
var (
	// ErrThrottled 表示目标处于发送间隔内，本次签发被拒绝。
	ErrThrottled = errors.New("verifycode: send throttled")
	// ErrCodeNotFound 表示验证码不存在或已过期（含已被消费/销毁）。
	ErrCodeNotFound = errors.New("verifycode: code not found or expired")
	// ErrCodeMismatch 表示验证码不匹配（已计入尝试次数）。
	ErrCodeMismatch = errors.New("verifycode: code mismatch")
	// ErrTooManyAttempts 表示尝试次数超限，验证码已销毁。
	ErrTooManyAttempts = errors.New("verifycode: too many attempts")
)

// RedisClient 是本包需要的最小 Redis 能力。
// 定义窄接口而不是直接吃 *redis.Client：单测不需要真实 Redis 也能替身。
type RedisClient interface {
	ScriptLoad(ctx context.Context, script string) *redis.StringCmd
	EvalSha(ctx context.Context, sha1 string, keys []string, args ...any) *redis.Cmd
	SetNX(ctx context.Context, key string, value any, ttl time.Duration) *redis.BoolCmd
	Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// Sender 把验证码发往目标（手机号 / 邮箱）。实现方负责渠道细节与模板。
type Sender interface {
	Send(ctx context.Context, target, code string) error
}

// SenderFunc 允许用闭包适配 Sender。
type SenderFunc func(ctx context.Context, target, code string) error

// Send 实现 Sender。
func (f SenderFunc) Send(ctx context.Context, target, code string) error {
	return f(ctx, target, code)
}

// Debug 是本地/自动化测试豁免：命中 Match 的目标跳过节流与发送，
// 把 FixedCode 按正常 TTL 落库，校验路径与真实码完全一致。
//
// ⚠ 生产环境不得注入本选项：知道固定码的人可以直接通过匹配集合内
// 任何目标的校验，这是一个显式后门。是否注入由装配处按环境显式决定，
// 本包不做环境判断。
type Debug struct {
	// Match 判定目标是否豁免（如手机号前缀、邮箱后缀）。
	Match func(target string) bool
	// FixedCode 是豁免目标的固定验证码，长度必须等于 CodeLength。
	FixedCode string
}

// Options 是构造参数。零值字段取默认值；越界值构造报错而不是静默修正。
type Options struct {
	// KeyPrefix 是 Redis 键前缀，默认 "verifycode"。多实例（如短信与邮件
	// 各一个 Verifier）应使用不同前缀，避免同一目标跨渠道互相覆盖。
	KeyPrefix string
	// CodeLength 是验证码位数，默认 6，允许 [4, 10]。
	CodeLength int
	// TTL 是验证码有效期，默认 5 分钟。
	TTL time.Duration
	// SendInterval 是同一目标的最小发送间隔，默认 60 秒。
	SendInterval time.Duration
	// MaxAttempts 是校验尝试上限（含不匹配），默认 5，允许 [1, 100]。
	// 超限即销毁验证码，正确码也不再通过。
	MaxAttempts int
	// Debug 是测试豁免，零值禁用。生产不得注入，见 Debug 类型注释。
	Debug *Debug
}

const (
	defaultKeyPrefix    = "verifycode"
	defaultCodeLength   = 6
	minCodeLength       = 4
	maxCodeLength       = 10
	defaultTTL          = 5 * time.Minute
	defaultSendInterval = time.Minute
	defaultMaxAttempts  = 5
	maxAttemptsCeiling  = 100
)

// verifyScript 原子完成校验的全部状态迁移。返回值契约：
//
//	 1 命中：删除验证码与计数
//	 0 不匹配：计数 +1（首个计数与验证码同款 TTL）
//	-1 不存在或已过期
//	-2 尝试超限：销毁验证码
//
// KEYS[1] 码键，KEYS[2] 计数键；ARGV[1] 提交码，ARGV[2] 上限，ARGV[3] TTL 毫秒。
const verifyScript = `
local code = redis.call('GET', KEYS[1])
if not code then
  return -1
end
local n = redis.call('INCR', KEYS[2])
if n == 1 then
  redis.call('PEXPIRE', KEYS[2], tonumber(ARGV[3]))
end
if n > tonumber(ARGV[2]) then
  redis.call('DEL', KEYS[1])
  redis.call('DEL', KEYS[2])
  return -2
end
if code == ARGV[1] then
  redis.call('DEL', KEYS[1])
  redis.call('DEL', KEYS[2])
  return 1
end
return 0
`

// Verifier 签发并校验一次性验证码。并发安全。
type Verifier struct {
	client RedisClient
	sender Sender
	opts   Options
	// verifySHA 缓存校验脚本的 SHA；Redis 重启清空脚本缓存时
	// 在 NOSCRIPT 上重载重写（先例：tokenstore）。
	verifySHA atomic.Pointer[string]
}

// New 构造 Verifier。必需依赖与参数边界在装配期校验；
// 校验脚本在构造期加载，Redis 不可达即装配失败（fail-fast）。
func New(ctx context.Context, client RedisClient, sender Sender, opts Options) (*Verifier, error) {
	if client == nil {
		return nil, errors.New("verifycode: redis client is required")
	}
	if sender == nil {
		return nil, errors.New("verifycode: sender is required")
	}
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}

	v := &Verifier{client: client, sender: sender, opts: normalized}
	sha, err := client.ScriptLoad(ctx, verifyScript).Result()
	if err != nil {
		return nil, fmt.Errorf("verifycode: load verify script: %w", err)
	}
	v.verifySHA.Store(&sha)
	return v, nil
}

func normalizeOptions(opts Options) (Options, error) {
	if opts.KeyPrefix == "" {
		opts.KeyPrefix = defaultKeyPrefix
	}
	if opts.CodeLength == 0 {
		opts.CodeLength = defaultCodeLength
	}
	if opts.CodeLength < minCodeLength || opts.CodeLength > maxCodeLength {
		return Options{}, fmt.Errorf("verifycode: code length must be within [%d, %d], got %d",
			minCodeLength, maxCodeLength, opts.CodeLength)
	}
	if opts.TTL == 0 {
		opts.TTL = defaultTTL
	}
	if opts.TTL < time.Second {
		return Options{}, fmt.Errorf("verifycode: ttl must be at least 1s, got %s", opts.TTL)
	}
	if opts.SendInterval == 0 {
		opts.SendInterval = defaultSendInterval
	}
	if opts.SendInterval < time.Second {
		return Options{}, fmt.Errorf("verifycode: send interval must be at least 1s, got %s", opts.SendInterval)
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = defaultMaxAttempts
	}
	if opts.MaxAttempts < 1 || opts.MaxAttempts > maxAttemptsCeiling {
		return Options{}, fmt.Errorf("verifycode: max attempts must be within [1, %d], got %d",
			maxAttemptsCeiling, opts.MaxAttempts)
	}
	if opts.Debug != nil {
		if opts.Debug.Match == nil {
			return Options{}, errors.New("verifycode: debug exemption requires a match function")
		}
		if len(opts.Debug.FixedCode) != opts.CodeLength {
			return Options{}, fmt.Errorf("verifycode: debug fixed code length must equal code length %d",
				opts.CodeLength)
		}
	}
	return opts, nil
}

// Send 为目标签发验证码：节流 → 生成 → 落库 → 发送。
// 命中 Debug 豁免的目标只存固定码不发送（也不占用节流窗口）。
// 返回 ErrThrottled 表示处于发送间隔内。
func (v *Verifier) Send(ctx context.Context, target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("verifycode: target is required")
	}

	h := v.hash(target)
	if v.opts.Debug != nil && v.opts.Debug.Match(target) {
		return v.store(ctx, h, v.opts.Debug.FixedCode)
	}

	ok, err := v.client.SetNX(ctx, v.throttleKey(h), 1, v.opts.SendInterval).Result()
	if err != nil {
		return fmt.Errorf("verifycode: check send throttle: %w", err)
	}
	if !ok {
		return ErrThrottled
	}

	code, err := randomDigits(v.opts.CodeLength)
	if err != nil {
		return fmt.Errorf("verifycode: generate code: %w", err)
	}
	if err := v.store(ctx, h, code); err != nil {
		return err
	}
	// 落库在发送之前：反过来会出现「短信已发出但库里没有码」，
	// 用户拿着有效码却永远校验不过。
	if err := v.sender.Send(ctx, target, code); err != nil {
		return fmt.Errorf("verifycode: send code: %w", err)
	}
	return nil
}

// store 落码并清零尝试计数。两步非原子的最坏结果是新码继承旧计数，
// 方向是 fail-closed（更早触发超限），可接受。
func (v *Verifier) store(ctx context.Context, hashed, code string) error {
	if err := v.client.Set(ctx, v.codeKey(hashed), code, v.opts.TTL).Err(); err != nil {
		return fmt.Errorf("verifycode: store code: %w", err)
	}
	if err := v.client.Del(ctx, v.attemptsKey(hashed)).Err(); err != nil {
		return fmt.Errorf("verifycode: reset attempts: %w", err)
	}
	return nil
}

// Verify 校验并消费验证码。返回 nil 表示命中且验证码已销毁；
// 其余结果见包级错误集合。基础设施故障原样包装返回，不与业务结果混淆。
func (v *Verifier) Verify(ctx context.Context, target, code string) error {
	if strings.TrimSpace(target) == "" || len(code) != v.opts.CodeLength {
		// 长度不符的提交不可能命中，直接判不匹配；
		// 不打存储往返，也不计入尝试（垃圾输入烧不掉真实提交者的额度）。
		return ErrCodeMismatch
	}

	h := v.hash(target)
	result, err := v.runVerify(ctx, h, code)
	if err != nil {
		return err
	}
	switch result {
	case 1:
		return nil
	case 0:
		return ErrCodeMismatch
	case -1:
		return ErrCodeNotFound
	case -2:
		return ErrTooManyAttempts
	default:
		return fmt.Errorf("verifycode: unexpected script result %d", result)
	}
}

// runVerify 执行校验脚本，并在 NOSCRIPT 时重载后重试一次。
func (v *Verifier) runVerify(ctx context.Context, hashed, code string) (int64, error) {
	keys := []string{v.codeKey(hashed), v.attemptsKey(hashed)}
	args := []any{code, v.opts.MaxAttempts, v.opts.TTL.Milliseconds()}

	res, err := v.client.EvalSha(ctx, *v.verifySHA.Load(), keys, args...).Int64()
	if err != nil && isNoScript(err) {
		// Redis 重启会清空脚本缓存。重载后重试，而不是让这次校验失败。
		sha, loadErr := v.client.ScriptLoad(ctx, verifyScript).Result()
		if loadErr != nil {
			return 0, fmt.Errorf("verifycode: reload verify script: %w", loadErr)
		}
		v.verifySHA.Store(&sha)
		res, err = v.client.EvalSha(ctx, sha, keys, args...).Int64()
	}
	if err != nil {
		return 0, fmt.Errorf("verifycode: run verify script: %w", err)
	}
	return res, nil
}

// hash 把目标散列进键：明文手机号/邮箱不得出现在 Redis 键里
// （键会进监控、慢日志与 RDB）。截断 128 bit，碰撞概率可忽略。
func (v *Verifier) hash(target string) string {
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:16])
}

func (v *Verifier) codeKey(hashed string) string     { return v.opts.KeyPrefix + ":code:" + hashed }
func (v *Verifier) throttleKey(hashed string) string { return v.opts.KeyPrefix + ":throttle:" + hashed }
func (v *Verifier) attemptsKey(hashed string) string { return v.opts.KeyPrefix + ":attempts:" + hashed }

// randomDigits 生成加密安全的固定长度数字码。
// 逐字节拒绝采样消除模偏差：验证码的熵是防爆破预算的一部分。
func randomDigits(length int) (string, error) {
	digits := make([]byte, 0, length)
	buf := make([]byte, 1)
	for len(digits) < length {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		// 250 是 10 的最大整倍数上界（0..249），超出即重采样。
		if buf[0] >= 250 {
			continue
		}
		digits = append(digits, '0'+buf[0]%10)
	}
	return string(digits), nil
}

func isNoScript(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "NOSCRIPT")
}

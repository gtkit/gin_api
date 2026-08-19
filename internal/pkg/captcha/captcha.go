// Package captcha 提供图形验证码：生成 base64 图片、答案存 Redis、一次性校验。
//
// 图形绘制复用 mojocn/base64Captcha 的 Driver；答案存取由本包直连 Redis
// 完成（GETDEL 一次性消费），不走该库的 Store 接口——那个接口无 ctx、
// 读取无 error，适配它只能吞错误。
//
// 防爆破由一次性消费天然兑现：无论提交对错，同一 ID 校验一次即销毁，
// 猜错必须换新图。母版不构造实例：派生项目在装配处按需构造。
package captcha

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mojocn/base64Captcha"
	"github.com/redis/go-redis/v9"
)

// RedisClient 是本包需要的最小 Redis 能力。
type RedisClient interface {
	Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd
	GetDel(ctx context.Context, key string) *redis.StringCmd
}

// Options 是构造参数。零值字段取默认值；越界值构造报错。
type Options struct {
	// KeyPrefix 是 Redis 键前缀，默认 "captcha"。
	KeyPrefix string
	// TTL 是验证码有效期，默认 3 分钟。
	TTL time.Duration
	// Width / Height / Length 是图片尺寸与数字位数，默认 240×80、5 位。
	Width  int
	Height int
	Length int
}

const (
	defaultKeyPrefix = "captcha"
	defaultTTL       = 3 * time.Minute
	defaultWidth     = 240
	defaultHeight    = 80
	defaultLength    = 5
	// 数字驱动的默认噪点参数，取上游 DefaultDriverDigit 同款。
	defaultMaxSkew  = 0.7
	defaultDotCount = 80
)

// Captcha 生成并一次性校验图形验证码。并发安全。
type Captcha struct {
	client RedisClient
	driver base64Captcha.Driver
	opts   Options
}

// New 构造 Captcha。必需依赖与参数边界在装配期校验。
func New(client RedisClient, opts Options) (*Captcha, error) {
	if client == nil {
		return nil, errors.New("captcha: redis client is required")
	}
	if opts.KeyPrefix == "" {
		opts.KeyPrefix = defaultKeyPrefix
	}
	if opts.TTL == 0 {
		opts.TTL = defaultTTL
	}
	if opts.TTL < time.Second {
		return nil, fmt.Errorf("captcha: ttl must be at least 1s, got %s", opts.TTL)
	}
	if opts.Width == 0 {
		opts.Width = defaultWidth
	}
	if opts.Height == 0 {
		opts.Height = defaultHeight
	}
	if opts.Length == 0 {
		opts.Length = defaultLength
	}
	if opts.Width < 0 || opts.Height < 0 || opts.Length < 4 || opts.Length > 10 {
		return nil, fmt.Errorf("captcha: invalid dimensions or length (width=%d height=%d length=%d)",
			opts.Width, opts.Height, opts.Length)
	}

	return &Captcha{
		client: client,
		driver: base64Captcha.NewDriverDigit(opts.Height, opts.Width, opts.Length, defaultMaxSkew, defaultDotCount),
		opts:   opts,
	}, nil
}

// Generate 生成一张图形验证码，返回 ID 与 base64 图片（data URI）。
// 答案只进 Redis（带 TTL），不出现在返回值中；落库失败即报错，
// 绝不发出一张无法校验的图。
func (c *Captcha) Generate(ctx context.Context) (id, b64 string, err error) {
	id, content, answer := c.driver.GenerateIdQuestionAnswer()
	item, err := c.driver.DrawCaptcha(content)
	if err != nil {
		return "", "", fmt.Errorf("captcha: draw: %w", err)
	}
	if err := c.client.Set(ctx, c.key(id), answer, c.opts.TTL).Err(); err != nil {
		return "", "", fmt.Errorf("captcha: store answer: %w", err)
	}
	return id, item.EncodeB64string(), nil
}

// Verify 校验并销毁验证码：无论对错，同一 ID 只有一次校验机会。
// 返回 (false, nil) 覆盖「答案错误」与「ID 不存在/已过期」两种业务结果；
// 存储故障返回 error，不与业务结果混淆。
func (c *Captcha) Verify(ctx context.Context, id, answer string) (bool, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(answer) == "" {
		return false, nil
	}
	stored, err := c.client.GetDel(ctx, c.key(id)).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("captcha: load answer: %w", err)
	}
	// 大小写无关与去空白和上游 base64Captcha 的校验语义一致。
	return strings.EqualFold(strings.TrimSpace(stored), strings.TrimSpace(answer)), nil
}

func (c *Captcha) key(id string) string { return c.opts.KeyPrefix + ":" + id }

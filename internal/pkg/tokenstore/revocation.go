package tokenstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RevocationClient 是撤销表需要的 Redis 能力。
type RevocationClient interface {
	Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd
	Exists(ctx context.Context, keys ...string) *redis.IntCmd
}

// RevocationList 记录被撤销的访问令牌。
//
// 访问令牌是自包含的：不查这张表就无法在它自然到期前收回。表本身不会无限增长
// ——每条记录的存活时间就是对应令牌的剩余寿命，到期后 Redis 自动清除。
// 访问令牌寿命越短，这张表越小，这也是 access_ttl 该配短的原因之一。
type RevocationList struct {
	client RevocationClient
	prefix string
	now    func() time.Time
	// clockSkew 是验签允许的时钟偏移（local-authentication spec）。
	//
	// 撤销记录必须活过 exp + clockSkew：验签接受 leeway 而记录只存到原始
	// exp 时，已登出的令牌会在偏移窗口内重新被接受——撤销在它最需要生效的
	// 时刻失效，且完全静默。
	clockSkew time.Duration
}

// NewRevocationList 构造撤销表。
//
// clockSkew 必须与**验签实际使用的 leeway** 同源（由 JWT 装配传入），
// 否则两处取值一旦分叉，撤销记录会比验签窗口先消失。
func NewRevocationList(
	client RevocationClient, prefix string, clockSkew time.Duration,
) (*RevocationList, error) {
	if client == nil {
		return nil, errors.New("tokenstore: redis client is required")
	}
	if clockSkew < 0 {
		return nil, fmt.Errorf("tokenstore: clock skew must not be negative, got %s", clockSkew)
	}
	return &RevocationList{
		client: client, prefix: prefix, now: time.Now, clockSkew: clockSkew,
	}, nil
}

// Revoke 撤销一个访问令牌，记录存活到 **exp + 时钟偏移** 为止。
//
// 加上偏移是必需的：验签用 leeway 接受"刚过期一点"的令牌，若撤销记录只存到
// 原始 exp，已登出的令牌会在那段偏移窗口内重新被接受。
//
// 整个窗口都已过去时直接返回：那个令牌本来就不会再被接受，为它写一条
// 立即过期的记录只是白白多一次往返。
func (l *RevocationList) Revoke(ctx context.Context, tokenID string, expiresAt time.Time) error {
	if tokenID == "" {
		return errors.New("tokenstore: token id is required")
	}

	ttl := expiresAt.Add(l.clockSkew).Sub(l.now())
	if ttl <= 0 {
		return nil
	}

	if err := l.client.Set(ctx, l.key(tokenID), "1", ttl).Err(); err != nil {
		return fmt.Errorf("tokenstore: revoke access token: %w", err)
	}
	return nil
}

// IsRevoked 报告令牌是否已被撤销。
//
// **查询失败必须返回错误，不能当作"未撤销"**：把故障降级成放行，会让一次
// Redis 抖动变成"所有已登出的令牌重新生效"。调用方据此 fail-closed。
func (l *RevocationList) IsRevoked(ctx context.Context, tokenID string) (bool, error) {
	if tokenID == "" {
		// 没有 jti 的令牌无法被撤销，也就无法证明它没被撤销。
		// 报错而不是放行：这是签发侧的问题，不该在校验侧被静默接受。
		return false, errors.New("tokenstore: token has no id, so its revocation status is unknowable")
	}

	n, err := l.client.Exists(ctx, l.key(tokenID)).Result()
	if err != nil {
		return false, fmt.Errorf("tokenstore: check access token revocation: %w", err)
	}
	return n > 0, nil
}

func (l *RevocationList) key(tokenID string) string {
	return l.prefix + ":revoked:" + hashSegment(tokenID)
}

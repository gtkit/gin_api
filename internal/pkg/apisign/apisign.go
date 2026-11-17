// Package apisign 提供服务端间请求签名的验签原语与 nonce 去重存储。
//
// 它只管"怎么算、怎么建串、怎么原子去重"，不碰 HTTP 语义；HTTP 侧的
// 头提取、请求体缓存与响应写字型由 middleware.Signature 完成，保持
// 纯函数包可脱离 Gin 单独测试。
package apisign

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// CanonicalString 按约定拼接待签名字符串。
//
// 调用方与被调方必须用**完全一致**的规则拼串，签名才能对上；任一字段
// 被篡改都会改变这条串，签名随即失配。换行分隔是为了消除拼接歧义
// （a=1 与 b=2 若不分隔可能拼出与 a= 与 1b=2 相同的串）。
//
// query 必须是**已排序、已编码**的查询串（即 url.Values.Encode() 的输出，
// 该实现按键字典序排序并对值做百分号编码）；body 是原始请求体字节。
func CanonicalString(method, path, query, body, appID, timestamp, nonce string) string {
	return strings.Join([]string{method, path, query, body, appID, timestamp, nonce}, "\n")
}

// Sign 用 appSecret 对 canonical 串做 HMAC-SHA256，返回小写十六进制摘要。
func Sign(secret, canonical string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

// ConstantTimeEquals 恒定时间比较两段字符串，防时序攻击。
//
// 普通 == 在第一个不同字符即短路返回，响应耗时泄露了"前几位是否相同"，
// 攻击者可据此逐字符爆破签名。这里无论哪里不同都遍历完，耗时一致。
func ConstantTimeEquals(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// NonceStore 是一次性随机数（nonce）的原子去重存储。
//
// Record 返回"是否首次见到该 nonce"：首次见返回 true（可放行）并记录；
// 已存在返回 false（重放），必须原子完成——"先查后存"在并发下会同时查到
// 不存在，进而都放行，重放成功。
type NonceStore interface {
	Record(ctx context.Context, appID, nonce string, ttl time.Duration) (bool, error)
}

// RedisNonceStore 用 Redis SETNX 实现 nonce 原子去重。
//
// TTL 自动过期清理，无需手动删除；键按 appID 隔离，避免两个不同调用方
// 恰好生成相同 nonce 时互相踩踏。前缀沿用 redis.prefix 的命名空间。
type RedisNonceStore struct {
	rdb    redis.Cmdable
	prefix string
}

// NewRedisNonceStore 构造 Redis 下的 nonce 去重存储。
func NewRedisNonceStore(rdb redis.Cmdable, prefix string) *RedisNonceStore {
	return &RedisNonceStore{rdb: rdb, prefix: prefix}
}

// Record 原子记录 nonce，首次见返回 true。
func (s *RedisNonceStore) Record(ctx context.Context, appID, nonce string, ttl time.Duration) (bool, error) {
	key := s.prefix + ":apisign:nonce:" + appID + ":" + nonce
	ok, err := s.rdb.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

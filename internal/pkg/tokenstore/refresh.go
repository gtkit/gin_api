// Package tokenstore 保管刷新令牌与访问令牌的撤销状态。
//
// 存 Redis 而不是数据库：这些记录全都自带过期时间，写入频率与登录/刷新同频，
// 且过期后没有任何保留价值。放进关系库意味着要么留一张只增不减的表，
// 要么再写一个清理任务去删本来就该自己消失的行。
package tokenstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// 错误集合。调用方按类型决定响应，不解析文本。
var (
	// ErrRefreshNotFound 表示令牌不存在或已过期。
	ErrRefreshNotFound = errors.New("tokenstore: refresh token not found")

	// ErrRefreshReused 表示一个已被使用过的刷新令牌再次出现。
	//
	// 它与 ErrRefreshNotFound 是不同的事件：找不到多半是过期或客户端记错，
	// 而重用意味着令牌被复制到了别处。前者拒绝这一次即可，后者必须撤销整族。
	ErrRefreshReused = errors.New("tokenstore: refresh token was already used")

	// ErrFamilyRevoked 表示令牌族已被撤销（登出，或此前检测到重用）。
	ErrFamilyRevoked = errors.New("tokenstore: token family is revoked")

	// ErrMalformedToken 表示令牌不是本包签发的形状。
	ErrMalformedToken = errors.New("tokenstore: malformed refresh token")
)

// RefreshClient 是本包需要的 Redis 能力，便于测试替换。
//
// 定义窄接口而不是直接吃 *redis.Client：单测因此不需要一个真实的 Redis，
// 而本包的核心逻辑（重用检测、族撤销）恰恰是最需要被测到的部分。
type RefreshClient interface {
	ScriptLoad(ctx context.Context, script string) *redis.StringCmd
	EvalSha(ctx context.Context, sha1 string, keys []string, args ...any) *redis.Cmd
	HSet(ctx context.Context, key string, values ...any) *redis.IntCmd
	Expire(ctx context.Context, key string, ttl time.Duration) *redis.BoolCmd
	Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd
}

// tokenSeparator 分隔令牌里的族标识与秘密段。
const tokenSeparator = "."

// RefreshStore 保管刷新令牌。
//
// # 令牌形状
//
// 明文令牌是 `<family>.<secret>`，两段都是 256 位随机值。
//
// 族标识放在令牌里是**必需的**，不是为了省事：消费一个令牌时要同时检查
// "这个令牌本身"与"它所属的族有没有被撤销"，而 Lua 脚本要求所有 key 在调用时
// 就声明——族标识若只存在记录内部，脚本读到它时已经来不及把族 key 加进 KEYS。
// 让令牌自带族标识，两个 key 在发起调用前就都能算出来。
//
// 族标识明文可见不构成弱点：它只是一个随机标识符，凭它无法推导出秘密段，
// 而秘密段才是凭据。
type RefreshStore struct {
	client RefreshClient
	prefix string
	ttl    time.Duration

	// replayWindow 是"响应丢失后客户端重试"的容忍窗口。
	replayWindow time.Duration
	// now 可注入，测试用它确定地驱动窗口边界。
	now func() time.Time

	// sha 是已加载脚本的摘要。
	//
	// 用原子指针：它在每次刷新时被读，并在 Redis 重启后的 NOSCRIPT 上被重写。
	// 普通字段在这两条路径并发时是数据竞争——限流包踩过一次，这里不重蹈。
	sha atomic.Pointer[string]
}

// consumeScript 原子地消费一个刷新令牌。
//
// **必须是一个脚本**：读取、判断已用、标记已用这三步若分开发出，两个并发的
// 刷新请求会同时读到"未使用"，于是同一个令牌被消费两次——而重用检测的全部
// 意义就是发现这种情况。分步实现会让检测本身成为它要检测的漏洞。
//
// 族撤销检查也在脚本内：放到 Go 侧会留出"消费成功但族刚被撤销"的窗口，
// 那个窗口里签发的新令牌属于一个已被撤销的族。
//
// # 重放窗口
//
// 消费成功后，把**轮换出的秘密段**记在原记录上并保留一小段时间。窗口内用
// 同一个令牌再来一次，直接返回那个秘密段，不再轮换、不报重用。
//
// 没有这个窗口的话，一次丢失的响应会被判成令牌泄漏：客户端手里仍是旧令牌，
// 重试是它唯一合理的动作，而服务端撤销整族——一次网络抖动因此升级成一次
// 强制登出。窗口取秒级即可覆盖这种重试，而攻击者拿到泄漏令牌后的使用时间
// 几乎总是远大于此，重用检测的实际效力不受影响。
//
// **窗口过后记录必须继续存在**：过期删除会让泄漏令牌的迟到重放变成
// "找不到"，而那条路径不撤销任何东西——重用检测就此失效，且完全静默。
// 因此记录保留完整寿命，窗口用一个截止时刻字段表达。
//
// 时刻由调用方传入而不是脚本里取 `TIME`：那让脚本保持确定性，
// 也让测试能确定地驱动窗口边界。同一个服务集群内的时钟偏差
// 在秒级窗口上无关紧要。
//
// KEYS[1] 令牌记录；KEYS[2] 族撤销标记；KEYS[3] 轮换出的新记录
// ARGV[1] 记录存活秒数；ARGV[2] 新秘密段；ARGV[3] 重放截止时刻（毫秒）
// ARGV[4] 当前时刻（毫秒）
//
// 返回 {code, uid, replaySecret}：
//
//	0 正常消费  1 令牌不存在  2 重用  3 族已撤销  4 窗口内重放
const consumeScript = `
if redis.call('EXISTS', KEYS[2]) == 1 then
  return {3, '', ''}
end

local record = redis.call('HMGET', KEYS[1], 'uid', 'used', 'next', 'replay_until')
local uid, used, next_secret, replay_until = record[1], record[2], record[3], record[4]

if not uid then
  return {1, '', ''}
end
if used == '1' then
  -- 窗口内：把首次轮换出的秘密段原样返回，等价于那次调用的重放。
  if next_secret and next_secret ~= '' and replay_until
     and tonumber(ARGV[4]) < tonumber(replay_until) then
    return {4, uid, next_secret}
  end
  -- 窗口外：这次重放说明令牌被复制了出去。
  return {2, uid, ''}
end

redis.call('HSET', KEYS[1], 'used', '1', 'next', ARGV[2], 'replay_until', ARGV[3])
-- 原记录保留**完整寿命**，不缩短到重放窗口：缩短的话，窗口一过记录就消失，
-- 泄漏令牌的迟到重放会走到"找不到"分支，而那条路径什么也不撤销。
redis.call('EXPIRE', KEYS[1], ARGV[1])
redis.call('HSET', KEYS[3], 'uid', uid, 'used', '0')
redis.call('EXPIRE', KEYS[3], ARGV[1])
return {0, uid, ''}
`

// NewRefreshStore 构造 RefreshStore 并预加载脚本。
//
// 预加载而不是首次使用时加载：Redis 不可达是本地认证的致命问题，
// 在装配期暴露远好过在第一个用户登录时。
func NewRefreshStore(ctx context.Context, client RefreshClient, prefix string, ttl time.Duration) (*RefreshStore, error) {
	if client == nil {
		return nil, errors.New("tokenstore: redis client is required")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("tokenstore: refresh ttl must be positive, got %s", ttl)
	}

	sha, err := client.ScriptLoad(ctx, consumeScript).Result()
	if err != nil {
		return nil, fmt.Errorf("tokenstore: load refresh script: %w", err)
	}

	s := &RefreshStore{
		client: client, prefix: prefix, ttl: ttl,
		replayWindow: defaultReplayWindow,
		now:          time.Now,
	}
	s.sha.Store(&sha)
	return s, nil
}

// defaultReplayWindow 是刷新重放的容忍窗口。
//
// 10 秒：足够覆盖一次丢失响应后的立即重试（客户端通常在几百毫秒到几秒内
// 重试），又远短于攻击者拿到泄漏令牌后的典型使用时间。
//
// 这个取值是在两种错误之间选：太短会让正常重试被判成泄漏（一次网络抖动
// 就把用户踢下线），太长会让真正的泄漏多出一段可用时间。前者天天发生，
// 后者需要令牌先被窃取，因此宁可偏向前者。
//
// **窗口的实际代价要说清楚**：窗口内，持有同一个旧令牌的两方（合法客户端
// 与攻击者）会拿到同一个新令牌，此后两者都能继续用下去，而重用检测看不到
// 异常——它们用的是同一条链。这是所有重放窗口方案的共同代价，也是它必须
// 取得足够短的原因。
const defaultReplayWindow = 10 * time.Second

// Issue 签发一个新的刷新令牌，开启一个新的令牌族。
//
// 登录时调用。返回的是**明文令牌**，只此一次——存储里只有它的哈希。
func (s *RefreshStore) Issue(ctx context.Context, uid int64) (string, error) {
	family, err := randomSegment()
	if err != nil {
		return "", err
	}
	return s.issueInFamily(ctx, uid, family)
}

// issueInFamily 在指定族内签发令牌。
func (s *RefreshStore) issueInFamily(ctx context.Context, uid int64, family string) (string, error) {
	secret, err := randomSegment()
	if err != nil {
		return "", err
	}

	// 先 HSet 后 Expire：中途失败最坏留下一条永不过期的记录，而它对应的令牌
	// 客户端从未收到，不构成可用凭据。反过来才是错的——那会产生一条
	// 没有过期时间的**可用**记录。
	key := s.recordKey(family, secret)
	if err := s.client.HSet(ctx, key, "uid", uid, "used", "0").Err(); err != nil {
		return "", fmt.Errorf("tokenstore: store refresh token: %w", err)
	}
	if err := s.client.Expire(ctx, key, s.ttl).Err(); err != nil {
		return "", fmt.Errorf("tokenstore: set refresh token ttl: %w", err)
	}
	return family + tokenSeparator + secret, nil
}

// Consume 消费一个刷新令牌并轮换出新的。
//
// 返回用户 ID 与新的刷新令牌。检测到重用时撤销整族并返回 ErrRefreshReused。
func (s *RefreshStore) Consume(ctx context.Context, token string) (uid int64, newToken string, err error) {
	family, secret, err := splitToken(token)
	if err != nil {
		return 0, "", err
	}

	nextSecret, err := randomSegment()
	if err != nil {
		return 0, "", err
	}

	code, uid, replaySecret, err := s.runConsume(ctx, family, secret, nextSecret)
	if err != nil {
		return 0, "", err
	}

	switch code {
	case 0:
		return uid, family + tokenSeparator + nextSecret, nil
	case 4:
		// 窗口内重放：返回首次轮换出的那个令牌，与首次调用完全一致。
		// **不报重用**——这次重放几乎可以肯定是客户端在重试一个丢失的响应。
		return uid, family + tokenSeparator + replaySecret, nil
	case 1:
		return 0, "", ErrRefreshNotFound
	case 2:
		// 一个已被消费的令牌再次出现，意味着它被复制到了别处。仅拒绝这一次
		// 会让攻击者继续持有可用凭据；撤销整族会同时切断攻击者与合法用户，
		// 迫使重新登录——这是 OAuth 2.0 Security BCP 对该信号的标准处置。
		if revokeErr := s.RevokeFamily(ctx, family); revokeErr != nil {
			// 撤销失败必须上报：此时重用已经发生，而防线没有生效。
			return 0, "", fmt.Errorf("tokenstore: refresh token reuse detected but revoking "+
				"the family failed, the leaked token chain is still live: %w", revokeErr)
		}
		return 0, "", ErrRefreshReused
	case 3:
		return 0, "", ErrFamilyRevoked
	default:
		return 0, "", fmt.Errorf("tokenstore: unexpected consume result %d", code)
	}
}

// runConsume 执行脚本，并在 NOSCRIPT 时重载后重试一次。
func (s *RefreshStore) runConsume(
	ctx context.Context, family, secret, nextSecret string,
) (code, uid int64, replaySecret string, err error) {
	keys := []string{
		s.recordKey(family, secret),
		s.familyKey(family),
		s.recordKey(family, nextSecret),
	}
	now := s.now()
	args := []any{
		strconv.FormatInt(int64(s.ttl.Seconds()), 10),
		nextSecret,
		strconv.FormatInt(now.Add(s.replayWindow).UnixMilli(), 10),
		strconv.FormatInt(now.UnixMilli(), 10),
	}

	raw, err := s.client.EvalSha(ctx, *s.sha.Load(), keys, args...).Result()
	if err != nil && isNoScript(err) {
		// Redis 重启会清空脚本缓存。重载后重试，而不是让这次刷新失败——
		// 一次重启不该把所有在线用户都踢下线。
		sha, loadErr := s.client.ScriptLoad(ctx, consumeScript).Result()
		if loadErr != nil {
			return 0, 0, "", fmt.Errorf("tokenstore: reload refresh script: %w", loadErr)
		}
		s.sha.Store(&sha)
		raw, err = s.client.EvalSha(ctx, sha, keys, args...).Result()
	}
	if err != nil {
		return 0, 0, "", fmt.Errorf("tokenstore: consume refresh token: %w", err)
	}

	return parseConsumeResult(raw)
}

// parseConsumeResult 解析脚本返回的三元组。
func parseConsumeResult(raw any) (code, uid int64, replaySecret string, err error) {
	values, ok := raw.([]any)
	if !ok || len(values) != 3 {
		return 0, 0, "", fmt.Errorf("tokenstore: malformed consume result %#v", raw)
	}

	code, ok = values[0].(int64)
	if !ok {
		return 0, 0, "", fmt.Errorf("tokenstore: malformed consume code %#v", values[0])
	}

	uidStr, _ := values[1].(string)
	replaySecret, _ = values[2].(string)
	if uidStr == "" {
		return code, 0, replaySecret, nil
	}
	uid, err = strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		return 0, 0, "", fmt.Errorf("tokenstore: malformed uid %q in refresh record: %w", uidStr, err)
	}
	return code, uid, replaySecret, nil
}

// RevokeFamily 撤销一个令牌族的全部令牌。
//
// 标记族而不是遍历删除该族的每个令牌：Redis 没有反向索引，遍历需要 SCAN
// 整个键空间。标记是 O(1) 的，且天然覆盖该族**尚未签发**的后续令牌——
// 而那恰恰是重用检测最需要挡住的东西。
func (s *RefreshStore) RevokeFamily(ctx context.Context, family string) error {
	if family == "" {
		return errors.New("tokenstore: family is required")
	}

	// TTL 取刷新令牌寿命：该族最后一个令牌过期后，撤销标记就没有存在意义了，
	// 留着只会让撤销标记成为一张只增不减的表。
	if err := s.client.Set(ctx, s.familyKey(family), "1", s.ttl).Err(); err != nil {
		return fmt.Errorf("tokenstore: revoke token family: %w", err)
	}
	return nil
}

// RevokeFamilyOfToken 撤销令牌所属的族。登出时调用。
func (s *RefreshStore) RevokeFamilyOfToken(ctx context.Context, token string) error {
	family, _, err := splitToken(token)
	if err != nil {
		return err
	}
	return s.RevokeFamily(ctx, family)
}

// recordKey 是令牌记录的键。
//
// `{family}` 是 Redis 集群的 hash tag：它保证同一族的记录键与撤销键落在同一个
// slot 上。没有它，消费脚本会因为跨 slot 访问被集群直接拒绝——而那是只在
// 集群部署上才出现的失败，单机测试永远发现不了。
func (s *RefreshStore) recordKey(family, secret string) string {
	return s.prefix + ":refresh:{" + family + "}:" + hashSegment(secret)
}

// familyKey 是族撤销标记的键，与 recordKey 共用 hash tag。
func (s *RefreshStore) familyKey(family string) string {
	return s.prefix + ":refresh:{" + family + "}:revoked"
}

// splitToken 拆出族标识与秘密段。
func splitToken(token string) (family, secret string, err error) {
	family, secret, ok := strings.Cut(token, tokenSeparator)
	if !ok || family == "" || secret == "" {
		return "", "", ErrMalformedToken
	}
	return family, secret, nil
}

// randomSegment 生成一个 256 位的随机段。
//
// 用 crypto/rand：令牌是纯粹的凭据，可预测的令牌等于没有令牌。
func randomSegment() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("tokenstore: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashSegment 返回秘密段的存储形式。
//
// 存哈希而不是明文：一次 Redis 快照泄漏或一条误打的 KEYS 命令，
// 不应该等同于交出所有在线用户的凭据。
//
// 不加盐、不用慢哈希是刻意的：秘密段本身是 256 位的随机值，没有可猜测的结构，
// 彩虹表与暴力破解都不适用；而刷新路径要求这次计算足够快。
func hashSegment(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// isNoScript 判断错误是否为脚本缓存缺失。
func isNoScript(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "NOSCRIPT")
}

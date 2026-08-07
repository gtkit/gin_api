// Package singleton 提供跨副本的单实例执行租约。
//
// # 锁不是正确性保证
//
// 这一点必须先说清楚，因为它决定了本包的接口形状。网络分区、进程长时间
// 暂停（GC、宿主机挂起）、租约过期后旧实例恢复——这些情况下会有两个实例
// 同时认为自己持有租约，而任何基于超时的锁都无法避免。
//
// 因此本包提供的是**两层**保护：
//
//   - 租约（token）把"同时运行"的概率压到很低，并保证释放与续租不会误伤
//     其他持有者；
//   - fence 序号让下游能够**确定地**拒绝过期实例的写入。
//
// 只用第一层的任务必须是严格幂等的。这不是建议——注册时要显式声明，
// 两者都不满足的任务不允许注册（见 Task 的文档）。
//
// # 随机 token 与 fence 序号是两件不同的东西
//
// 把随机 token 当 fencing 用是一个看起来能工作、实际毫无保护的组合：
// 两个随机值之间没有先后关系，下游拿到两个 token 时无从判断哪个更旧。
// fencing 必须是**单调递增序号**，且由下游写操作拒绝比已见值更小的序号
// （条件更新 `WHERE fence < :fence`，或唯一约束加版本列）。
package singleton

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// 错误集合。
var (
	// ErrNotAcquired 表示租约当前被别的实例持有。
	//
	// 它是**正常情况**，不是故障：多副本部署下每轮只有一个实例能拿到。
	ErrNotAcquired = errors.New("singleton: lease is held by another instance")

	// ErrLeaseLost 表示续租时发现租约已不属于本实例。
	ErrLeaseLost = errors.New("singleton: lease was lost")
)

// LeaseClient 是本包需要的 Redis 能力。
//
// 定义窄接口而不是直接吃 *redis.Client：本包的核心逻辑是三个 Lua 脚本的
// 语义，而那正是最需要在没有真实 Redis 的情况下也能被测到的部分。
type LeaseClient interface {
	ScriptLoad(ctx context.Context, script string) *redis.StringCmd
	EvalSha(ctx context.Context, sha1 string, keys []string, args ...any) *redis.Cmd
}

// acquireScript 原子地抢锁并生成 fence 序号。
//
// **抢锁与 INCR 必须在同一个脚本里**。分成两条客户端命令会留下一个真实的
// 崩溃窗口：进程在 SET 成功、INCR 之前退出，锁在那里而 fence 没生成——
// 接手的实例拿到的序号无法与前一任比较，fencing 因此失效。
//
// 抢锁失败时**不执行 INCR**：失败也消耗序号会让 fence 随失败次数虚增。
// 那不破坏单调性，但让"序号跳了很多"这个诊断信号失去意义——本来它说明
// 发生过多次租约易主，虚增之后它只说明有很多实例在抢。
//
// KEYS[1] 锁；KEYS[2] fence 计数器
// ARGV[1] token；ARGV[2] 租期毫秒；ARGV[3] fence 计数器存活毫秒（<=0 表示不过期）
//
// 返回 {acquired, fence}：acquired 为 1 时 fence 有效。
//
// fence 计数器的存活时间**默认为不过期**：它必须活得比任何可能仍在运行的
// 旧实例长，否则计数器被清掉后重新从 1 开始，新持有者的序号会比某个仍在
// 运行的旧实例更小——fencing 因此完全失效，而且是静默失效。
//
// 只有探测租约（用完即弃、名字随机、不会有下游写入）才设它，
// 避免每次启动都在 Redis 里留下一个永久键。
const acquireScript = `
if redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2]) then
  local fence = redis.call('INCR', KEYS[2])
  if tonumber(ARGV[3]) > 0 then
    redis.call('PEXPIRE', KEYS[2], ARGV[3])
  end
  return {1, fence}
end
return {0, 0}
`

// renewScript 是 compare-and-renew。
//
// **只在锁的当前值等于本实例 token 时才延长**。裸 PEXPIRE 会在"旧持有者
// 租约已过期、新持有者已抢到锁"时错误地延长**新持有者**的锁：旧实例因此
// 以为自己仍持有，两个实例同时在跑，而其中一个的租约还被另一个不断续期。
//
// KEYS[1] 锁；ARGV[1] token；ARGV[2] 租期毫秒
// 返回 1 表示续租成功，0 表示已失去租约。
const renewScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`

// releaseScript 是 compare-and-delete。
//
// 租约过期后锁可能已被另一个实例持有。无条件 DEL 会把别人正在用的锁删掉，
// 让第三个实例立刻抢到——一次超时因此放大成多个实例并发。
//
// KEYS[1] 锁；ARGV[1] token
// 返回删除的键数。
const releaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

// Lease 是一个具名的单实例租约。
type Lease struct {
	client LeaseClient
	name   string
	// prefix 隔离同一 Redis 上不同服务的键空间。
	prefix string
	ttl    time.Duration
	// fenceTTL 是 fence 计数器的存活时间；0 表示不过期。
	//
	// 生产租约必须是 0：计数器被清掉会让序号重新从 1 开始，而某个仍在运行的
	// 旧实例可能持有更大的序号——下游会因此拒绝新持有者、接受旧持有者，
	// fencing 反过来起了反作用。
	fenceTTL time.Duration

	// 三个脚本的 sha。用原子指针：它们在运行期被读，
	// 并在 Redis 重启后的 NOSCRIPT 上被重写。
	acquireSHA atomic.Pointer[string]
	renewSHA   atomic.Pointer[string]
	releaseSHA atomic.Pointer[string]
}

// Held 描述一次成功获取的租约。
type Held struct {
	// Token 是本次持有的随机标识，用于续租与释放的比对。
	//
	// **它不是 fencing token**：随机值之间没有先后关系。
	Token string
	// Fence 是单调递增序号，交给下游做过期写入检测。
	//
	// 用法：把它随写操作一起传下去，写路径拒绝比已见值更小的序号
	// （`WHERE fence < :fence`）。不这样用的任务必须是严格幂等的。
	Fence int64
}

// NewLease 构造租约。**不做任何 I/O**（runtime-state-machine spec 的
// optional 语义前提）：构造期触网会让一次 Redis 抖动阻断整个进程启动，
// 而本包消费者（optional worker）的语义恰恰是"失败降级、不拖垮进程"。
// 脚本经运行期的 NOSCRIPT 自愈路径惰性装载（见 eval）；启动期的端到端
// 能力验证由 Probe 在 worker 的 Prepare 里承担。
//
// ttl 是租期：它决定了"实例崩溃后多久另一个实例能接手"，也决定了
// 续租的频率。太短会让一次网络抖动就丢锁，太长会让崩溃后的空窗期变长。
func NewLease(client LeaseClient, prefix, name string, ttl time.Duration) (*Lease, error) {
	if client == nil {
		return nil, errors.New("singleton: redis client is required")
	}
	if name == "" {
		return nil, errors.New("singleton: lease name is required")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("singleton: lease ttl must be positive, got %s", ttl)
	}

	l := &Lease{client: client, name: name, prefix: prefix, ttl: ttl}
	// sha 指针初始化为空串：首次 EVALSHA 必然 NOSCRIPT，自愈路径就地装载。
	for _, target := range []*atomic.Pointer[string]{&l.acquireSHA, &l.renewSHA, &l.releaseSHA} {
		empty := ""
		target.Store(&empty)
	}
	return l, nil
}

// lockKey 与 fenceKey 共用 hash tag。
//
// `{...}` 是 Redis 集群的 hash tag：它保证两个键落在同一个 slot 上。
// 没有它，抢锁脚本会因为跨 slot 访问被集群直接拒绝——而那是**只在集群
// 部署上**出现的失败，单机测试永远发现不了。
func (l *Lease) lockKey() string  { return l.prefix + ":singleton:{" + l.name + "}:lock" }
func (l *Lease) fenceKey() string { return l.prefix + ":singleton:{" + l.name + "}:fence" }

// Acquire 尝试获取租约。
//
// 未抢到时返回 ErrNotAcquired——那是正常情况，多副本下每轮只有一个赢家。
func (l *Lease) Acquire(ctx context.Context) (Held, error) {
	token, err := randomToken()
	if err != nil {
		return Held{}, err
	}

	raw, err := l.eval(ctx, &l.acquireSHA, acquireScript,
		[]string{l.lockKey(), l.fenceKey()},
		token,
		strconv.FormatInt(l.ttl.Milliseconds(), 10),
		strconv.FormatInt(l.fenceTTL.Milliseconds(), 10))
	if err != nil {
		return Held{}, fmt.Errorf("singleton: acquire lease %q: %w", l.name, err)
	}

	values, ok := raw.([]any)
	if !ok || len(values) != 2 {
		return Held{}, fmt.Errorf("singleton: malformed acquire result %#v", raw)
	}
	acquired, _ := values[0].(int64)
	if acquired != 1 {
		return Held{}, ErrNotAcquired
	}
	fence, ok := values[1].(int64)
	if !ok {
		return Held{}, fmt.Errorf("singleton: malformed fence %#v", values[1])
	}

	return Held{Token: token, Fence: fence}, nil
}

// Renew 续租。返回 ErrLeaseLost 表示租约已不属于本实例。
func (l *Lease) Renew(ctx context.Context, held Held) error {
	raw, err := l.eval(ctx, &l.renewSHA, renewScript,
		[]string{l.lockKey()},
		held.Token, strconv.FormatInt(l.ttl.Milliseconds(), 10))
	if err != nil {
		return fmt.Errorf("singleton: renew lease %q: %w", l.name, err)
	}
	if ok, _ := raw.(int64); ok != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Release 释放租约。
//
// 只删除自己的锁：租约过期后它可能已属于别人，无条件删除会把别人正在用的
// 锁删掉，让第三个实例立刻抢到。
func (l *Lease) Release(ctx context.Context, held Held) error {
	if _, err := l.eval(ctx, &l.releaseSHA, releaseScript,
		[]string{l.lockKey()}, held.Token); err != nil {
		return fmt.Errorf("singleton: release lease %q: %w", l.name, err)
	}
	return nil
}

// eval 执行脚本，NOSCRIPT 时重载后重试一次。
func (l *Lease) eval(
	ctx context.Context, sha *atomic.Pointer[string], script string, keys []string, args ...any,
) (any, error) {
	raw, err := l.client.EvalSha(ctx, *sha.Load(), keys, args...).Result()
	if err == nil || !isNoScript(err) {
		return raw, err
	}

	// Redis 重启会清空脚本缓存。重载后重试，而不是让这一轮失败——
	// 一次重启不该让单实例任务停摆到下一个周期。
	reloaded, loadErr := l.client.ScriptLoad(ctx, script).Result()
	if loadErr != nil {
		return nil, fmt.Errorf("reload script: %w", loadErr)
	}
	sha.Store(&reloaded)
	return l.client.EvalSha(ctx, reloaded, keys, args...).Result()
}

// randomToken 生成 128 位随机 token。
//
// 用 crypto/rand：token 是"这把锁是我的"的唯一证据，可预测的 token
// 意味着别的实例能构造出它，从而删掉或续期不属于自己的锁。
func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("singleton: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// isNoScript 判断错误是否为脚本缓存缺失。
func isNoScript(err error) bool {
	return err != nil && len(err.Error()) >= 8 && err.Error()[:8] == "NOSCRIPT"
}

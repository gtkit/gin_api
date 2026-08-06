// Package ratelimit 实现限流策略的执行层。
//
// 本包只负责"给定策略与键，判定是否放行"，不认识 HTTP。
// 键的组成与响应头由中间件层负责。
package ratelimit

// tokenBucketScript 是定点整数令牌桶。
//
// 全部用整数 microtokens 运算，不出现浮点：浮点 token 在长时间运行与多副本下
// 会漂移，而漂移方向不可预测——可能超发配额也可能误杀。Lua 的 number 是
// IEEE-754 double，整数在 2^53 以内精确，策略校验保证不越界。
//
// KEYS[1] = 桶的键
// ARGV[1] = 每秒补充的 microtokens（整数，> 0）
// ARGV[2] = 桶容量 microtokens（整数，> 0）
// ARGV[3] = 键的 TTL（毫秒，整数）
//
// 返回 {status, remaining_micro, retry_after_ms}
//
//	status: 1 = 放行, 0 = 限速
//
// **时间基准取自 Redis 自己**（`redis.call('TIME')`），调用方不提供时间戳。
//
// 早先由调用方传入时间，并配一个"前向漂移上限"防止它把虚拟时间往前推而白得
// token。那个上限只能拿"上次请求时刻"作参照，于是**任何超过上限的正常空闲都被
// 判成故障**：fail_open 下限流被绕过，fail_closed 下持续拒绝且不写回状态，
// 客户端重试到键过期为止。证据是下面"截到足以补满容量"那段专为长空闲而写，
// 却因为守卫先返回而永远不可达——两段代码互相矛盾。
//
// 改由 Redis 供时后，调用方无法影响时间推进，超发 token 的风险从根上消失，
// 那个无法正确判定的上限也就没有存在理由。副作用是多副本一致性变强：
// 所有应用节点共享同一个钟，节点间时钟分歧不再影响判定。
//
// 仍保留**回拨保护**：Redis 宿主自身的时钟步退仍可能发生，倒退虚拟时间会让
// 下一次正常请求补出一大段本不该有的 token。
//
// 依赖：Lua 内调用 TIME 需要 effect replication（Redis 3.2+ 可开启，5.0+ 默认）。
//
// 豁免理由：gosec 的 G101 是按变量名与字面量内容做启发式判定的，
// 这里命中的是脚本文本里的 `tokens` 一词。它是令牌桶的令牌，不是凭据。
//
//nolint:gosec // G101 误报：字面量是 Lua 脚本，"tokens" 指令牌桶的令牌
const tokenBucketScript = `
local rate_micro   = tonumber(ARGV[1])
local capacity     = tonumber(ARGV[2])
local ttl_ms       = tonumber(ARGV[3])
local cost         = 1000000

-- 时间基准取自 Redis 自己：调用方不供时，因此无法把虚拟时间往前推。
local t = redis.call('TIME')
local now_ms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

local state = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(state[1])
local last_ms = tonumber(state[2])

if tokens == nil or last_ms == nil then
  tokens = capacity
  last_ms = now_ms
end

-- 回拨：不倒退虚拟时间。
local elapsed_ms = now_ms - last_ms
if elapsed_ms < 0 then
  elapsed_ms = 0
end

-- 先把流逝时间截到"足以补满容量"的最大值，再做乘法。
-- 不截的话，一段很长的空闲之后 elapsed*rate 会越过精确整数范围。
local max_useful_ms = math.ceil(capacity / rate_micro * 1000)
if elapsed_ms > max_useful_ms then
  elapsed_ms = max_useful_ms
end

tokens = tokens + math.floor(elapsed_ms * rate_micro / 1000)
if tokens > capacity then
  tokens = capacity
end

local status
local retry_after_ms = 0
if tokens >= cost then
  tokens = tokens - cost
  status = 1
else
  status = 0
  -- 补足一次调用所需的缺口需要多久。向上取整，避免客户端提前重试。
  retry_after_ms = math.ceil((cost - tokens) * 1000 / rate_micro)
end

redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now_ms)
redis.call('PEXPIRE', KEYS[1], ttl_ms)

return {status, tokens, retry_after_ms}
`

// Lua 脚本的返回状态。
const (
	statusRejected = 0
	statusAllowed  = 1
)

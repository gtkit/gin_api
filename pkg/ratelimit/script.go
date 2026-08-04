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
// ARGV[3] = 本次调用的时间戳（毫秒，整数）
// ARGV[4] = 允许的最大前向时钟漂移（毫秒，整数）
// ARGV[5] = 键的 TTL（毫秒，整数）
//
// 返回 {status, remaining_micro, retry_after_ms}
//
//	status: 1 = 放行, 0 = 限速, -1 = 时钟漂移超限（调用方按 FailureMode 处理）
//
// 时钟处理有两条独立规则：
//
//  1. **回拨保护**：传入时间早于上次补充时刻时，不倒退虚拟时间，按"零流逝"处理。
//     倒退会让下一次正常请求补出一大段本不该有的 token。
//  2. **前向漂移超限 = 故障态**：超过上限时**不推进、不写回任何状态**，
//     直接返回 -1。绝不能"钳制到上限再继续算"——那样每个请求都把虚拟时间
//     往前推一个上限，于是每个请求都白得 `上限 × rate` 个 token，限流完全失效。
//
// 豁免理由：gosec 的 G101 是按变量名与字面量内容做启发式判定的，
// 这里命中的是脚本文本里的 `tokens` 一词。它是令牌桶的令牌，不是凭据。
//
//nolint:gosec // G101 误报：字面量是 Lua 脚本，"tokens" 指令牌桶的令牌
const tokenBucketScript = `
local rate_micro   = tonumber(ARGV[1])
local capacity     = tonumber(ARGV[2])
local now_ms       = tonumber(ARGV[3])
local max_skew_ms  = tonumber(ARGV[4])
local ttl_ms       = tonumber(ARGV[5])
local cost         = 1000000

local state = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(state[1])
local last_ms = tonumber(state[2])

if tokens == nil or last_ms == nil then
  tokens = capacity
  last_ms = now_ms
end

-- 前向漂移超限：故障态，不动任何状态。
if now_ms > last_ms + max_skew_ms then
  return {-1, tokens, 0}
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
	statusRejected  = 0
	statusAllowed   = 1
	statusClockSkew = -1
)

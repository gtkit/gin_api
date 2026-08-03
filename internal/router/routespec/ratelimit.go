package routespec

import (
	"errors"
	"fmt"
	"math"
	"regexp"
)

// RateLimitScope 决定限流键里"主体"那一段取自哪里。
//
// 零值非法：与访问/超时策略同一原则。一个没声明 scope 的策略无法确定
// 它到底在限谁，而最可能的默认（全局）恰好是危害最大的那个——
// 一个用户的行为会影响所有人。
type RateLimitScope uint8

const (
	// ScopeUnspecified 是非法零值。
	ScopeUnspecified RateLimitScope = iota
	// ScopeIP 按客户端 IP 限流。IP 只能经唯一 accessor 取得（见架构传感器）。
	ScopeIP
	// ScopeUser 按已认证用户限流。**必须挂在鉴权之后的路由上**，
	// 否则拿不到用户身份，只能退化成一个所有匿名请求共享的桶。
	ScopeUser
	// ScopeGlobal 是该路由的全局配额，不区分主体。
	ScopeGlobal
)

func (s RateLimitScope) String() string {
	switch s {
	case ScopeIP:
		return "ip"
	case ScopeUser:
		return "user"
	case ScopeGlobal:
		return "global"
	default:
		return "unspecified"
	}
}

// RateLimitStore 是计数状态的存放位置。
type RateLimitStore uint8

const (
	// StoreUnspecified 是非法零值。
	StoreUnspecified RateLimitStore = iota
	// StoreLocal 是进程内令牌桶。
	//
	// **多副本下有效限额 = 配置值 × 副本数**。它适合"高位防护上限"，
	// 不适合精确业务限额（如"每账号每分钟 5 次登录"）。
	StoreLocal
	// StoreRedis 是跨副本共享的令牌桶。
	StoreRedis
)

func (s RateLimitStore) String() string {
	switch s {
	case StoreLocal:
		return "local"
	case StoreRedis:
		return "redis"
	default:
		return "unspecified"
	}
}

// RateLimitFailureMode 决定后端不可用时怎么办。
//
// 这是一个**业务决策**，不能有默认值：登录接口失败时必须拒绝（否则限流形同虚设），
// 而普通查询失败时拒绝会把一次 Redis 抖动放大成一次业务不可用。
type RateLimitFailureMode uint8

const (
	// FailureUnspecified 是非法零值。
	FailureUnspecified RateLimitFailureMode = iota
	// FailureOpen 后端不可用时放行。
	FailureOpen
	// FailureClosed 后端不可用时拒绝。
	FailureClosed
	// FailureLocalFallback 后端不可用时改用本地影子桶。
	//
	// 只能称"算法模型一致"，**不能称"状态连续等价"**：切换时 Redis 里的
	// token 计数不会迁移，本地桶从满桶起算，瞬时会多放行至多一个 Burst。
	FailureLocalFallback
)

func (m RateLimitFailureMode) String() string {
	switch m {
	case FailureOpen:
		return "fail_open"
	case FailureClosed:
		return "fail_closed"
	case FailureLocalFallback:
		return "local_fallback"
	default:
		return "unspecified"
	}
}

// rateLimitIDPattern 约束策略 ID 的字符集。
//
// ID 会进限流键与 metrics label，因此必须低基数且字符集受限：
// 允许任意字符的话，一个带路径分隔符或空格的 ID 会破坏键的结构。
var rateLimitIDPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

// microTokensPerToken 是定点数换算基数：1 token = 1_000_000 microtokens。
//
// 用定点整数而不是浮点：浮点 token 在长时间运行与多副本下会漂移，
// 而漂移的方向不可预测——可能变松（超发配额）也可能变紧（误杀）。
const microTokensPerToken = 1_000_000

// maxExactInteger 是 Lua number（IEEE-754 double）能精确表示的最大整数。
//
// 超过它之后整数运算开始丢精度，而丢精度的表现是**限流悄悄失准**，
// 不会有任何报错。因此所有会进 Lua 的数值都必须在启动期校验不越界。
const maxExactInteger = int64(1)<<53 - 1

// RateLimitPolicy 是一条限流策略的完整契约。
//
// 策略在装配期注册进 registry，路由经 RouteSpec.RateLimitPolicyID 引用。
// 不让路由内联策略参数：同一条限额往往被多条路由共享，内联会产生
// 多份可能不一致的副本，而改动时极易漏掉其中之一。
type RateLimitPolicy struct {
	// ID 是稳定的低基数标识，进限流键与 metrics label。
	ID string
	// Rate 是每秒补充速率，必须 > 0。允许小数（如 0.5 = 每 2 秒 1 个）。
	Rate float64
	// Burst 是桶容量，必须 > 0。
	Burst int
	// Scope 决定键里主体那一段的来源。
	Scope RateLimitScope
	// Store 决定计数状态存放位置。
	Store RateLimitStore
	// FailureMode 决定后端不可用时的行为。
	FailureMode RateLimitFailureMode
	// MaxKeys 是该策略的键基数硬上限，必须 > 0。
	//
	// 达上限时**拒绝新建**，绝不淘汰仍活跃的桶：LRU 淘汰意味着攻击者
	// 轮换超过 MaxKeys 个主体就能反复把旧桶挤掉，旧主体回来时拿到
	// 完整 Burst——限流被完全绕过。
	MaxKeys int
}

// Validate 校验策略自洽。
//
// 全部在**启动期**执行：限流配置错误的表现是"限额不是你以为的那个值"，
// 而那在生产上只有被打穿或被误杀时才会发现。
func (p RateLimitPolicy) Validate() error {
	var errs []error

	if !rateLimitIDPattern.MatchString(p.ID) {
		errs = append(errs, fmt.Errorf(
			"rate limit policy id %q must match %s: the id goes into limiter keys and metrics labels",
			p.ID, rateLimitIDPattern.String()))
	}
	if p.Rate <= 0 || math.IsNaN(p.Rate) || math.IsInf(p.Rate, 0) {
		errs = append(errs, fmt.Errorf("rate limit policy %q requires a positive finite rate, got %v", p.ID, p.Rate))
	}
	if p.Burst <= 0 {
		errs = append(errs, fmt.Errorf("rate limit policy %q requires a positive burst, got %d", p.ID, p.Burst))
	}
	if p.MaxKeys <= 0 {
		errs = append(errs, fmt.Errorf(
			"rate limit policy %q requires a positive max keys bound: without it a key-rotating client "+
				"can grow the limiter state without limit", p.ID))
	}
	if p.Scope == ScopeUnspecified {
		errs = append(errs, fmt.Errorf("rate limit policy %q must declare a scope (ip|user|global)", p.ID))
	}
	if p.Store == StoreUnspecified {
		errs = append(errs, fmt.Errorf("rate limit policy %q must declare a store (local|redis)", p.ID))
	}
	if p.FailureMode == FailureUnspecified {
		errs = append(errs, fmt.Errorf(
			"rate limit policy %q must declare a failure mode: whether an unavailable backend should "+
				"reject or allow is a business decision with no safe default", p.ID))
	}

	// local store 配 local_fallback 没有意义——它降级的目标就是它自己。
	// 这类配置说明作者对模型有误解，静默接受会让人以为配了降级保护。
	if p.Store == StoreLocal && p.FailureMode == FailureLocalFallback {
		errs = append(errs, fmt.Errorf(
			"rate limit policy %q uses the local store, so %s is meaningless: "+
				"there is nothing to fall back from", p.ID, FailureLocalFallback))
	}

	errs = append(errs, p.validateFixedPointRange()...)

	return errors.Join(errs...)
}

// validateFixedPointRange 校验定点换算不越过 Lua 的精确整数范围。
//
// 越界之后整数运算开始丢精度，而表现是限流**悄悄失准**——没有任何报错。
// 因此必须在启动期拒绝，而不是等运行时算出一个错误的剩余配额。
func (p RateLimitPolicy) validateFixedPointRange() []error {
	if p.Rate <= 0 || p.Burst <= 0 {
		return nil // 已由上面的规则报错，这里不再重复
	}

	var errs []error

	ratePerSecond := int64(math.Round(p.Rate * microTokensPerToken))
	if ratePerSecond < 1 {
		errs = append(errs, fmt.Errorf(
			"rate limit policy %q rate %v is below the smallest representable rate (1 microtoken/s)",
			p.ID, p.Rate))
	}

	capacity := int64(p.Burst) * microTokensPerToken
	if p.Burst > math.MaxInt64/microTokensPerToken || capacity > maxExactInteger {
		errs = append(errs, fmt.Errorf(
			"rate limit policy %q burst %d overflows the exact-integer range usable in the limiter script",
			p.ID, p.Burst))
	}

	return errs
}

// FixedPointRate 返回每秒补充的 microtokens（四舍五入）。
func (p RateLimitPolicy) FixedPointRate() int64 {
	return int64(math.Round(p.Rate * microTokensPerToken))
}

// FixedPointBurst 返回桶容量对应的 microtokens。
func (p RateLimitPolicy) FixedPointBurst() int64 {
	return int64(p.Burst) * microTokensPerToken
}

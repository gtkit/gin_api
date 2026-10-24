// Package auth 装配本地认证模块。
//
// 本包的主要职责是**错误翻译**：把 repository、credential、tokenstore 各自的
// 错误类型翻译成 application 的哨兵错误。application 不能 import repository
// （架构边界），也不该依赖某个具体存储包的错误类型——那会让"换一个凭据存储"
// 变成一次跨层改动。翻译集中在这里，是这条边界的实际成本。
package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/gtkit/logger/v2"

	"golang.org/x/sync/semaphore"

	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/pkg/credential"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/pkg/tokenstore"
	"github.com/gtkit/gin-api/internal/repository/authcredential"
	"github.com/gtkit/gin-api/pkg/ratelimit"
)

// credentialRepo 把仓储适配到 application 的端口。
type credentialRepo struct {
	repo *authcredential.Repository
}

func (r credentialRepo) FindByPhone(ctx context.Context, phone string) (authapp.Credential, bool, error) {
	cred, found, err := r.repo.FindByPhone(ctx, phone)
	if err != nil {
		return authapp.Credential{}, false, err
	}
	if !found {
		return authapp.Credential{}, false, nil
	}
	return authapp.Credential{
		UserID:       cred.UserID,
		PasswordHash: cred.PasswordHash,
		Active:       cred.Active,
	}, true, nil
}

func (r credentialRepo) Create(ctx context.Context, phone, passwordHash, nickname string) (int64, error) {
	uid, err := r.repo.Create(ctx, phone, passwordHash, nickname)
	if errors.Is(err, authcredential.ErrPhoneTaken) {
		return 0, authapp.ErrPhoneTaken
	}
	return uid, err
}

// passwordHasher 把密码包适配到 application 的端口。
//
// gate 是进程级并发上限（local-authentication spec）：bcrypt 是刻意昂贵的
// 计算，限流约束的是单来源/单账号速率，约束不了聚合并发——一批分布式
// 登录请求就能把全部 CPU 吃光。
//
// 用带权信号量而不是缓冲 channel：**排队必须可取消**。channel 收发没有
// 取消入口，请求被客户端放弃或被路由超时中止后仍占着名额，排队者会随
// 攻击持续累积（每个都挂着一个 goroutine 与一份请求上下文）。
type passwordHasher struct {
	hasher *credential.Hasher
	gate   *semaphore.Weighted
}

// newPasswordHasher 构造带并发闸门的适配器；maxConcurrent <= 0 时取 1。
func newPasswordHasher(hasher *credential.Hasher, maxConcurrent int) passwordHasher {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return passwordHasher{hasher: hasher, gate: semaphore.NewWeighted(int64(maxConcurrent))}
}

// acquire 占用一个校验名额；ctx 取消即离队。
func (h passwordHasher) acquire(ctx context.Context) (release func(), err error) {
	if err := h.gate.Acquire(ctx, 1); err != nil {
		// 取消/超时在这里如实返回：调用方据此知道"没算，不是算错了"。
		return nil, fmt.Errorf("auth: wait for a password hashing slot: %w", err)
	}
	return func() { h.gate.Release(1) }, nil
}

func (h passwordHasher) Hash(ctx context.Context, password string) (string, error) {
	release, err := h.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	hashed, err := h.hasher.Hash(password)
	if errors.Is(err, credential.ErrPasswordTooLong) {
		return "", authapp.ErrPasswordTooLong
	}
	return hashed, err
}

func (h passwordHasher) Compare(ctx context.Context, hashed, password string) error {
	release, err := h.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	err = h.hasher.Compare(hashed, password)
	if errors.Is(err, credential.ErrMismatch) {
		return authapp.ErrInvalidCredentials
	}
	return err
}

func (h passwordHasher) CompareWithDummy(ctx context.Context, password string) error {
	// 时间掩护同样是 bcrypt 计算，同受闸门约束。
	release, err := h.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	err = h.hasher.CompareWithDummy(password)
	if !errors.Is(err, credential.ErrMismatch) {
		// 掩护哈希异常等基础设施失败保留原因，不伪装成用户密码错误。
		// 正常比对的匹配/不匹配都由credential映射为ErrMismatch，
		// 此分支不会泄露正常情况下的账号存在性。
		return err
	}
	return authapp.ErrInvalidCredentials
}

// refreshStore 把令牌存储适配到 application 的端口。
type refreshStore struct {
	store *tokenstore.RefreshStore
}

func (s refreshStore) Issue(ctx context.Context, uid int64) (string, error) {
	return s.store.Issue(ctx, uid)
}

// Peek 只读判定，错误翻译与 Consume 同口径。
func (s refreshStore) Peek(ctx context.Context, token string) (int64, error) {
	uid, err := s.store.Peek(ctx, token)
	switch {
	case err == nil:
		return uid, nil
	case errors.Is(err, tokenstore.ErrRefreshReused):
		return 0, authapp.ErrRefreshReused
	case errors.Is(err, tokenstore.ErrRefreshNotFound),
		errors.Is(err, tokenstore.ErrFamilyRevoked),
		errors.Is(err, tokenstore.ErrMalformedToken):
		return 0, authapp.ErrRefreshRejected
	default:
		// 基础设施故障保持原样：它不该被当成"令牌无效"，否则一次 Redis
		// 抖动会把所有在线用户都告知"请重新登录"。
		return 0, err
	}
}

func (s refreshStore) Consume(ctx context.Context, token string) (int64, string, error) {
	uid, next, err := s.store.Consume(ctx, token)
	switch {
	case err == nil:
		return uid, next, nil
	case errors.Is(err, tokenstore.ErrRefreshReused):
		// 重用单独翻译：上层据此把它记成告警级安全事件，而不是日常的
		// "令牌过期"。两者对客户端同义，对运维完全不同。
		return 0, "", authapp.ErrRefreshReused
	case errors.Is(err, tokenstore.ErrRefreshNotFound),
		errors.Is(err, tokenstore.ErrFamilyRevoked),
		errors.Is(err, tokenstore.ErrMalformedToken):
		return 0, "", authapp.ErrRefreshRejected
	default:
		// 基础设施故障保持原样：它不该被当成"令牌无效"，否则一次 Redis
		// 抖动会把所有在线用户都告知"请重新登录"。
		return 0, "", err
	}
}

func (s refreshStore) RevokeFamilyOfToken(ctx context.Context, token string) error {
	err := s.store.RevokeFamilyOfToken(ctx, token)
	if errors.Is(err, tokenstore.ErrMalformedToken) {
		// 客户端给了一个畸形的刷新令牌。它本来就不对应任何令牌族，
		// 登出的访问令牌部分已经生效，这里当作完成而不是报错。
		return nil
	}
	return err
}

// authAuditor 把认证事件写进日志栈。
type authAuditor struct{}

func (authAuditor) Auditf(ctx context.Context, format string, args ...any) {
	logger.InfoCtx(ctx, fmt.Sprintf(format, args...))
}

// Failf 走 Warn：登录失败、重用检测、撤销失败都是安全排查的起点，
// 与"某某登录成功"共用一个级别，告警规则就只能靠解析文本。
func (authAuditor) Failf(ctx context.Context, format string, args ...any) {
	logger.WarnCtx(ctx, fmt.Sprintf(format, args...))
}

// eventRecorder 把认证事件转发给指标仪表。
type eventRecorder struct {
	recorder *metrics.Recorder
}

func (e eventRecorder) RecordAuthEvent(ctx context.Context, event string) {
	// application 用字符串常量表达事件，指标包用具名类型：两处都要求取值
	// 有限，而类型转换在这里是安全的——application 的常量集合与
	// metrics.AuthEvent 的取值一一对应，且都由本仓维护。
	e.recorder.RecordAuthEvent(ctx, metrics.AuthEvent(event))
}

// assertPorts 是编译期检查：适配器必须满足 application 的端口。
//
// 显式断言而不是等装配时才发现：一个端口签名变更若只在 NewService 的调用处
// 报错，错误信息会指向装配函数而不是真正需要修改的适配器。
var (
	_ authapp.CredentialRepository = credentialRepo{}
	_ authapp.PasswordHasher       = passwordHasher{}
	_ authapp.RefreshStore         = refreshStore{}
	_ authapp.RevocationList       = (*tokenstore.RevocationList)(nil)
	_ authapp.Auditor              = authAuditor{}
	_ authapp.EventRecorder        = eventRecorder{}
)

// errMissingRedis 是缺少 Redis 时的装配错误。
//
// 配置校验已经在启动更早的阶段拦下这种组合，这里是第二道：装配代码不该
// 依赖"别处一定校验过"——那个前提一旦被改动打破，这里会静默地构造出一个
// 无法撤销令牌的认证服务。
var errMissingRedis = fmt.Errorf("auth: local authentication requires redis for token revocation")

// AccountRateLimitPolicyID 是账号维度限流的策略标识（rate-limiting spec）。
//
// 策略缺席即该维度关闭：这是预置能力的配置门控语义，装配层据此决定
// 是否注入 accountLimiter。
const AccountRateLimitPolicyID = "auth.credential.account"

// NewAccountLimiter 把注册表里的账号维度策略适配到 application 端口。
func NewAccountLimiter(
	limiter ratelimit.Limiter, keys *ratelimit.KeyBuilder, recorder *metrics.Recorder,
) authapp.AccountLimiter {
	return accountLimiter{limiter: limiter, keys: keys, recorder: recorder}
}

// accountLimiter 见 AccountLimiter 端口的契约。
//
// 键经 KeyBuilder 的 HMAC 派生：账号明文不进键。method/route 用固定标签
// ——该维度不挂路由，标签把它的键空间与任何真实路由隔离。
// 后端故障由执行体按策略的 FailureMode 定局（与路由限流中间件同一事实），
// 这里只把判定记进指标。
type accountLimiter struct {
	limiter  ratelimit.Limiter
	keys     *ratelimit.KeyBuilder
	recorder *metrics.Recorder
}

func (l accountLimiter) Allow(ctx context.Context, account string) bool {
	policy := l.limiter.Policy()
	key := l.keys.Build(policy, "POST", "auth:credential-account",
		ratelimit.Subject{Scope: policy.Scope, Value: account})

	decision, err := l.limiter.Check(ctx, key)
	if err != nil {
		// 与路由限流中间件同款留痕：不留的话，一次长时间的降级不会有任何记录。
		logger.Warnf("account rate limit backend error; the policy's failure mode decided "+
			"the outcome (policy=%s outcome=%s): %v", policy.ID, decision.Outcome, err)
	}
	if l.recorder != nil {
		l.recorder.RecordRateLimitDecision(ctx, policy.ID, accountRateLimitOutcome(decision.Outcome))
	}
	return decision.Allowed()
}

// accountRateLimitOutcome 把判定映射到指标枚举（与中间件的映射同语义）。
func accountRateLimitOutcome(outcome ratelimit.Outcome) metrics.RateLimitOutcome {
	switch outcome {
	case ratelimit.OutcomeAllowed:
		return metrics.RateLimitAllowed
	case ratelimit.OutcomeRateLimited:
		return metrics.RateLimitThrottled
	case ratelimit.OutcomeKeyBudgetExhausted:
		return metrics.RateLimitKeyLimit
	case ratelimit.OutcomeBackendUnavailable:
		return metrics.RateLimitFailure
	case ratelimit.OutcomeUnspecified:
		// 未赋值的判定不是放行，指标上也不能算成放行。
		return metrics.RateLimitFailure
	default:
		return metrics.RateLimitFailure
	}
}

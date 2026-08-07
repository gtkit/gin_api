package application

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// 业务错误。transport 层按类型映射状态码，不解析文本。
var (
	// ErrInvalidCredentials 表示账号不存在或密码错误。
	//
	// **两种情况共用一个错误是刻意的**：区分它们等于提供一个不需要口令
	// 就能枚举已注册账号的接口。调用方也必须对两者返回完全相同的响应。
	ErrInvalidCredentials = errors.New("auth: invalid credentials")

	// ErrAccountRateLimited 表示该账号的凭据尝试频率超限（账号维度限流）。
	//
	// 与来源维度互补：换 IP 即可绕过来源限流，账号维度让"同一账号的
	// 尝试频率"独立受限，挡住分布式口令喷洒。
	ErrAccountRateLimited = errors.New("auth: too many attempts for this account")

	// ErrAccountInactive 表示账号存在但当前不可登录。
	ErrAccountInactive = errors.New("auth: account is not active")

	// ErrPhoneTaken 表示手机号已被注册。
	ErrPhoneTaken = errors.New("auth: phone is already registered")

	// ErrRefreshRejected 表示刷新令牌不可用（不存在、过期、已撤销或被重用）。
	//
	// 对客户端合并成一个错误：无论哪种原因，正确的处置都是重新登录。
	// 区分它们只会让攻击者知道自己手里的令牌处于哪种状态。
	ErrRefreshRejected = errors.New("auth: refresh token rejected")

	// ErrPasswordTooLong 表示口令超出可处理的长度。
	ErrPasswordTooLong = errors.New("auth: password is too long")

	// ErrRefreshReused 表示检测到刷新令牌重用。
	//
	// 它对客户端与 ErrRefreshRejected 同义（都该重新登录），单独定义只为让
	// 本层能把它记成告警级事件：重用意味着令牌被复制了出去，而"过期"只是日常。
	ErrRefreshReused = errors.New("auth: refresh token was reused")
)

// 依赖返回的错误由**装配层的 adapter 翻译成上面这些哨兵错误**，本层只用
// errors.Is 判断自己的错误。
//
// 这不是绕远路：application 不能 import repository（架构边界），也不该
// 依赖某个具体存储包的错误类型——那会让"换一个凭据存储"变成一次跨层改动。

// Service 编排本地认证。
type Service struct {
	creds     CredentialRepository
	hasher    PasswordHasher
	access    AccessIssuer
	refresh   RefreshStore
	revoked   RevocationList
	auditor   Auditor
	events    EventRecorder
	accessTTL time.Duration
	// accounts 是账号维度限流；nil 表示该维度未配置（策略缺席即关闭）。
	accounts AccountLimiter

	// now 可注入，测试用它确定性地驱动撤销记录的存活时间。
	now func() time.Time
}

// Options 是 Service 的依赖。
type Options struct {
	Credentials    CredentialRepository
	Hasher         PasswordHasher
	AccessIssuer   AccessIssuer
	RefreshStore   RefreshStore
	RevocationList RevocationList
	Auditor        Auditor
	// Events 记录指标；为 nil 时不记录。
	Events    EventRecorder
	AccessTTL time.Duration
	// Accounts 是账号维度限流；nil 表示该维度未配置（策略缺席即关闭）。
	Accounts AccountLimiter
}

// NewService 构造认证服务。
//
// 依赖缺失返回错误而不是 panic：那是装配错误，应当在启动期以错误表达。
func NewService(opts Options) (*Service, error) {
	switch {
	case opts.Credentials == nil:
		return nil, errors.New("auth: credential repository is required")
	case opts.Hasher == nil:
		return nil, errors.New("auth: password hasher is required")
	case opts.AccessIssuer == nil:
		return nil, errors.New("auth: access token issuer is required")
	case opts.RefreshStore == nil:
		return nil, errors.New("auth: refresh store is required")
	case opts.RevocationList == nil:
		return nil, errors.New("auth: revocation list is required")
	case opts.AccessTTL <= 0:
		return nil, fmt.Errorf("auth: access ttl must be positive, got %s", opts.AccessTTL)
	}

	return &Service{
		creds:     opts.Credentials,
		hasher:    opts.Hasher,
		access:    opts.AccessIssuer,
		refresh:   opts.RefreshStore,
		revoked:   opts.RevocationList,
		auditor:   opts.Auditor,
		events:    opts.Events,
		accessTTL: opts.AccessTTL,
		accounts:  opts.Accounts,
		now:       time.Now,
	}, nil
}

// TokenPair 是一次成功认证的产出。
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	// ExpiresIn 是访问令牌的剩余寿命（秒），供客户端安排刷新时机。
	ExpiresIn int64
}

// Register 建号并直接签发令牌。
//
// 注册后立即登录而不是要求再调一次登录：客户端刚提交过正确的口令，
// 再要一次除了多一个往返没有任何安全收益。
func (s *Service) Register(ctx context.Context, phone, password, nickname string) (TokenPair, error) {
	if err := s.gateAccount(ctx, phone, "register"); err != nil {
		return TokenPair{}, err
	}

	hashed, err := s.hasher.Hash(ctx, password)
	if err != nil {
		if errors.Is(err, ErrPasswordTooLong) {
			return TokenPair{}, ErrPasswordTooLong
		}
		return TokenPair{}, fmt.Errorf("auth: hash password: %w", err)
	}

	uid, err := s.creds.Create(ctx, phone, hashed, nickname)
	if err != nil {
		if errors.Is(err, ErrPhoneTaken) {
			return TokenPair{}, ErrPhoneTaken
		}
		return TokenPair{}, fmt.Errorf("auth: create account: %w", err)
	}

	s.audit("account registered: uid=%d", uid)
	return s.issuePair(ctx, uid)
}

// Login 校验凭据并签发令牌。
func (s *Service) Login(ctx context.Context, phone, password string) (TokenPair, error) {
	if err := s.gateAccount(ctx, phone, "login"); err != nil {
		return TokenPair{}, err
	}

	cred, found, err := s.creds.FindByPhone(ctx, phone)
	if err != nil {
		return TokenPair{}, fmt.Errorf("auth: look up credentials: %w", err)
	}

	if !found {
		// **必须走一次等价的比对**，不能直接返回。
		//
		// bcrypt 比对要几百毫秒，直接返回只需几微秒。这个差值稳定、可测量，
		// 攻击者据此可以枚举出哪些手机号已注册——一个不需要任何口令
		// 就能拿到的用户名单。
		_ = s.hasher.CompareWithDummy(ctx, password)
		s.fail("login rejected: no account for the supplied phone")
		s.event(ctx, EventLoginFailed)
		return TokenPair{}, ErrInvalidCredentials
	}

	if err := s.hasher.Compare(ctx, cred.PasswordHash, password); err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			s.fail("login rejected: wrong password for uid=%d", cred.UserID)
			s.event(ctx, EventLoginFailed)
			return TokenPair{}, ErrInvalidCredentials
		}
		// 哈希损坏不是"密码错误"：把它当成密码错误，一批坏掉的凭据会表现为
		// "这些用户都记错了密码"，排查方向被引向用户而不是数据。
		return TokenPair{}, fmt.Errorf("auth: compare password for uid=%d: %w", cred.UserID, err)
	}

	// 状态检查放在密码校验**之后**：放在之前，一个被禁用账号会比不存在的
	// 账号更快返回，账号是否存在因此又从时间上泄漏了出去。
	if !cred.Active {
		s.fail("login rejected: uid=%d is not active", cred.UserID)
		return TokenPair{}, ErrAccountInactive
	}

	s.audit("login succeeded: uid=%d", cred.UserID)
	s.event(ctx, EventLoginSucceeded)
	return s.issuePair(ctx, cred.UserID)
}

// Refresh 用刷新令牌换取新的令牌对。
func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	uid, next, err := s.refresh.Consume(ctx, refreshToken)
	if err != nil {
		switch {
		case errors.Is(err, ErrRefreshReused):
			// 重用是安全事件，必须以告警级别留痕：它意味着某个令牌被复制了
			// 出去，而普通的"过期/找不到"只是日常。两者共用一条日志的话，
			// 真正需要有人看的那条会淹没在噪音里。
			s.fail("refresh token reuse detected, the whole token family was revoked: %v", err)
			s.event(ctx, EventRefreshReused)
			return TokenPair{}, ErrRefreshRejected
		case errors.Is(err, ErrRefreshRejected):
			return TokenPair{}, ErrRefreshRejected
		default:
			return TokenPair{}, fmt.Errorf("auth: consume refresh token: %w", err)
		}
	}

	accessToken, err := s.access.Issue(ctx, uid)
	if err != nil {
		return TokenPair{}, fmt.Errorf("auth: issue access token for uid=%d: %w", uid, err)
	}

	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: next,
		ExpiresIn:    int64(s.accessTTL.Seconds()),
	}, nil
}

// Logout 撤销当前访问令牌与刷新令牌族。
//
// tokenID 是访问令牌的 jti，expiresAt 是它的过期时刻。refreshToken 可以为空
// ——客户端只带了访问令牌时仍然应当能登出，只是那条刷新链要等自然过期。
func (s *Service) Logout(ctx context.Context, tokenID string, expiresAt time.Time, refreshToken string) error {
	// **先撤刷新族、后撤访问令牌**（local-authentication spec）。
	//
	// 部分成功的残留必须取危害更小的一侧：反过来先撤访问令牌，族撤销一旦
	// 失败，同一个 access token 已进不了登出端点（401），重试被自己撤掉的
	// 令牌挡死，而长寿命的刷新链还活着、仍可换发新令牌；本序的残留只是
	// 一个短 TTL 的访问令牌——无法续期，且重试登出自然可行。
	if refreshToken != "" {
		if err := s.refresh.RevokeFamilyOfToken(ctx, refreshToken); err != nil {
			return fmt.Errorf("auth: revoke refresh family: %w (the access token is still "+
				"valid, retry logout)", err)
		}
	}

	if err := s.revoked.Revoke(ctx, tokenID, expiresAt); err != nil {
		// 刷新链已死，残留的访问令牌在自身 TTL 内到期且无法续期——
		// 仍然报错：登出没有完整生效，不能表现成成功。
		if refreshToken != "" {
			return fmt.Errorf("auth: the refresh family was revoked but the access token is "+
				"still live until it expires: %w", err)
		}
		return fmt.Errorf("auth: revoke access token: %w", err)
	}

	if refreshToken == "" {
		s.audit("logout: access token revoked, no refresh token supplied")
		return nil
	}

	s.audit("logout: refresh family and access token revoked")
	s.event(ctx, EventLogout)
	return nil
}

// issuePair 签发访问令牌与刷新令牌。
func (s *Service) issuePair(ctx context.Context, uid int64) (TokenPair, error) {
	accessToken, err := s.access.Issue(ctx, uid)
	if err != nil {
		return TokenPair{}, fmt.Errorf("auth: issue access token for uid=%d: %w", uid, err)
	}

	refreshToken, err := s.refresh.Issue(ctx, uid)
	if err != nil {
		return TokenPair{}, fmt.Errorf("auth: issue refresh token for uid=%d: %w", uid, err)
	}

	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(s.accessTTL.Seconds()),
	}, nil
}

func (s *Service) audit(format string, args ...any) {
	if s.auditor != nil {
		s.auditor.Auditf(format, args...)
	}
}

func (s *Service) fail(format string, args ...any) {
	if s.auditor != nil {
		s.auditor.Failf(format, args...)
	}
}

// event 记录一次认证事件的指标。
func (s *Service) event(ctx context.Context, name string) {
	if s.events != nil {
		s.events.RecordAuthEvent(ctx, name)
	}
}

// gateAccount 执行账号维度限流（rate-limiting spec）：在任何凭据查找与
// bcrypt 计算之前拦截——被限的尝试不该再消耗昂贵资源，也不该获得
// 时间信道信息。维度未配置（accounts 为 nil）时零判定。
func (s *Service) gateAccount(ctx context.Context, account, action string) error {
	if s.accounts == nil {
		return nil
	}
	if s.accounts.Allow(ctx, account) {
		return nil
	}
	// 审计不落账号明文：账号维度键已经过 HMAC，日志同样不能把明文漏出去。
	s.fail("%s rejected: account dimension rate limited", action)
	s.event(ctx, EventLoginFailed)
	return ErrAccountRateLimited
}

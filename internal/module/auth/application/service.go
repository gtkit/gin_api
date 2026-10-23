package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
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

	// ErrRegisteredLoginRequired 表示注册的账号已创建，但登录令牌签发失败。
	//
	// 单独成错是因为这是一次**部分成功**：账号这一侧的资源已经存在，
	// 把它表达成普通失败，客户端重试注册只会撞上"手机号已注册"——
	// 一个既成事实被表达成了从未发生。正确的客户端处置是直接登录。
	ErrRegisteredLoginRequired = errors.New("auth: account created but tokens were not issued; log in instead")

	// ErrInvalidAccount 表示账号标识本身不合法（空、含空白）。
	//
	// 与"凭据错误"分开：它在任何存储访问之前就能判定，且不涉及口令——
	// 合并成 ErrInvalidCredentials 会让一次明显的客户端错误消耗一次 bcrypt。
	ErrInvalidAccount = errors.New("auth: account identifier is invalid")

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

// normalizeAccount 规范化账号标识，供限流、查询与写入**共用同一个值**。
//
// 三者必须同源：数据库列用 utf8mb4_unicode_ci，那是 PAD SPACE 语义——
// "13800000000 " 与 "13800000000" 在查询里是同一行，而按原始输入派生的
// HMAC 限流键却是两个不同的桶。攻击者每次在尾部多加一个空格，就能对同一个
// 账号无限次尝试而每次都落在一个全新的桶里，账号维度限流形同虚设。
//
// 规范化在 application 入口一次完成：放到限流那一步单独做，写入路径就会
// 存进未规范化的值，同一个账号从此有两种存在形式。
//
// 只做 Trim 与"不得含内部空白"，不做大小写折叠或 Unicode 归一：手机号是
// 数字串，多做的每一步都要与数据库 collation 的口径逐条对齐，而对不上的
// 那一步会重新打开同一个洞。
func normalizeAccount(account string) (string, error) {
	normalized := strings.TrimSpace(account)
	if normalized == "" {
		return "", ErrInvalidAccount
	}
	if strings.ContainsFunc(normalized, unicode.IsSpace) {
		// 内部空白同样是 PAD SPACE 之外的等价面：直接拒绝，不猜测意图。
		return "", ErrInvalidAccount
	}
	return normalized, nil
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
	phone, err := normalizeAccount(phone)
	if err != nil {
		return TokenPair{}, err
	}
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

	s.audit(ctx, "account registered: uid=%d", uid)
	pair, err := s.issuePair(ctx, uid)
	if err != nil {
		// 账号已建、令牌未签发：必须显式表达这次部分成功。
		// 审计走告警级——它意味着令牌基础设施（Redis/签发器）在注册路径上
		// 出了问题，且已经产生了一个"注册成功但没拿到令牌"的用户。
		s.fail(ctx, "account registered but token issuance failed: uid=%d: %v", uid, err)
		return TokenPair{}, fmt.Errorf("%w (uid=%d): %w", ErrRegisteredLoginRequired, uid, err)
	}
	return pair, nil
}

// Login 校验凭据并签发令牌。
func (s *Service) Login(ctx context.Context, phone, password string) (TokenPair, error) {
	phone, err := normalizeAccount(phone)
	if err != nil {
		// 不做掩护比对：账号形态非法在任何存储访问之前就能判定，
		// 它不泄漏"这个账号是否存在"——因为这样的账号根本不可能存在。
		return TokenPair{}, err
	}
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
		//
		// 比对被取消/超时（闸门饱和、请求放弃）时必须与存在账号的路径
		// **同语义传播**：这里吞掉错误的话，存在的账号报基础设施错误、
		// 不存在的账号报 401，状态码与耗时就重新泄漏了账号是否存在。
		if err := s.hasher.CompareWithDummy(ctx, password); err != nil && !errors.Is(err, ErrInvalidCredentials) {
			return TokenPair{}, fmt.Errorf("auth: compare password: %w", err)
		}
		s.fail(ctx, "login rejected: no account for the supplied phone")
		s.event(ctx, EventLoginFailed)
		return TokenPair{}, ErrInvalidCredentials
	}

	if err := s.hasher.Compare(ctx, cred.PasswordHash, password); err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			s.fail(ctx, "login rejected: wrong password for uid=%d", cred.UserID)
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
		s.fail(ctx, "login rejected: uid=%d is not active", cred.UserID)
		return TokenPair{}, ErrAccountInactive
	}

	pair, err := s.issuePair(ctx, cred.UserID)
	if err != nil {
		return TokenPair{}, err
	}
	s.audit(ctx, "login succeeded: uid=%d", cred.UserID)
	s.event(ctx, EventLoginSucceeded)
	return pair, nil
}

// Refresh 用刷新令牌换取新的令牌对。
//
// 次序是 **只读判定 → 签发访问令牌 → 提交消费**，不能反过来。
//
// 先消费再签发时，签发失败会留下一枚已被消费的刷新令牌：客户端手里那枚
// 已经作废，而它唯一合理的动作（重试）在超过重放窗后会被判成令牌泄漏，
// 整族随之撤销——一次令牌签发的抖动升级成一次强制登出。本次序下签发失败
// 时状态零改动，重试完全安全。
//
// 判定与提交之间的竞态由 Consume 自己定局：它是唯一的原子权威，
// 并发消费仍然只有一个成功，重用检测与族撤销的语义不受影响。
func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	uid, err := s.refresh.Peek(ctx, refreshToken)
	if err != nil {
		return TokenPair{}, s.refreshRejected(ctx, refreshToken, err)
	}

	// 签发在消费之前：这一步失败时刷新令牌仍然有效。
	accessToken, err := s.access.Issue(ctx, uid)
	if err != nil {
		return TokenPair{}, fmt.Errorf(
			"auth: issue access token for uid=%d (the refresh token is untouched, retry is safe): %w",
			uid, err)
	}

	// 提交：轮换出下一代刷新令牌。到这里才第一次改动状态。
	_, next, err := s.refresh.Consume(ctx, refreshToken)
	if err != nil {
		return TokenPair{}, s.refreshRejected(ctx, refreshToken, err)
	}

	return TokenPair{
		AccessToken:  accessToken,
		RefreshToken: next,
		ExpiresIn:    int64(s.accessTTL.Seconds()),
	}, nil
}

// refreshRejected 把刷新链路的失败翻译成对外错误，并补齐重用的安全副作用。
func (s *Service) refreshRejected(ctx context.Context, refreshToken string, err error) error {
	switch {
	case errors.Is(err, ErrRefreshReused):
		// 重用是安全事件，必须以告警级别留痕：它意味着某个令牌被复制了
		// 出去，而普通的"过期/找不到"只是日常。两者共用一条日志的话，
		// 真正需要有人看的那条会淹没在噪音里。
		s.fail(ctx, "refresh token reuse detected, the whole token family was revoked: %v", err)
		s.event(ctx, EventRefreshReused)
		// **撤销必须真的发生**：只读判定不改状态，撤销与检测的原子绑定在
		// Consume 内。不补这一次调用，重用就只是被记了一笔而没有任何处置。
		// 它的结果必然仍是重用，因此只在失败时留痕，不改变对外错误。
		if _, _, revokeErr := s.refresh.Consume(ctx, refreshToken); revokeErr != nil &&
			!errors.Is(revokeErr, ErrRefreshReused) {
			s.fail(ctx, "revoking the reused refresh token family failed: %v", revokeErr)
		}
		return ErrRefreshRejected
	case errors.Is(err, ErrRefreshRejected):
		return ErrRefreshRejected
	default:
		return fmt.Errorf("auth: consume refresh token: %w", err)
	}
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
		s.audit(ctx, "logout: access token revoked, no refresh token supplied")
		return nil
	}

	s.audit(ctx, "logout: refresh family and access token revoked")
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

func (s *Service) audit(ctx context.Context, format string, args ...any) {
	if s.auditor != nil {
		s.auditor.Auditf(ctx, format, args...)
	}
}

func (s *Service) fail(ctx context.Context, format string, args ...any) {
	if s.auditor != nil {
		s.auditor.Failf(ctx, format, args...)
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
	s.fail(ctx, "%s rejected: account dimension rate limited", action)
	s.event(ctx, EventLoginFailed)
	return ErrAccountRateLimited
}

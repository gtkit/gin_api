package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- 测试替身 ---

type fakeCreds struct {
	mu sync.Mutex
	// byPhone 是已注册账号。
	byPhone map[string]Credential
	// findErr 模拟查询故障。
	findErr error
	// createErr 模拟建号故障。
	createErr error
	nextID    int64
}

func newFakeCreds() *fakeCreds {
	return &fakeCreds{byPhone: map[string]Credential{}, nextID: 1}
}

func (f *fakeCreds) FindByPhone(_ context.Context, phone string) (Credential, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.findErr != nil {
		return Credential{}, false, f.findErr
	}
	cred, ok := f.byPhone[phone]
	return cred, ok, nil
}

func (f *fakeCreds) Create(_ context.Context, phone, passwordHash, _ string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return 0, f.createErr
	}
	if _, exists := f.byPhone[phone]; exists {
		return 0, ErrPhoneTaken
	}
	id := f.nextID
	f.nextID++
	f.byPhone[phone] = Credential{UserID: id, PasswordHash: passwordHash, Active: true}
	return id, nil
}

// fakeHasher 是可预测的哈希：密文就是 "hashed:" + 明文。
//
// 不用真 bcrypt：本层测的是编排逻辑，真哈希只会让每个用例慢几百毫秒。
// 哈希本身的正确性在 internal/pkg/credential 有独立覆盖。
type fakeHasher struct {
	mu sync.Mutex
	// dummyCalls 记录"账号不存在时也比对一次"被调用的次数——
	// 那是防账号枚举的关键动作，必须能被断言。
	dummyCalls int
	hashErr    error
	compareErr error
	// dummyErr 模拟掩护比对自身的失败（闸门取消/超时）：
	// 此时没有发生比对，dummyCalls 不计数。
	dummyErr error
}

func (h *fakeHasher) Hash(_ context.Context, password string) (string, error) {
	if h.hashErr != nil {
		return "", h.hashErr
	}
	return "hashed:" + password, nil
}

func (h *fakeHasher) Compare(_ context.Context, hashed, password string) error {
	if h.compareErr != nil {
		return h.compareErr
	}
	if hashed != "hashed:"+password {
		return ErrInvalidCredentials
	}
	return nil
}

func (h *fakeHasher) CompareWithDummy(context.Context, string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dummyErr != nil {
		return h.dummyErr
	}
	h.dummyCalls++
	return ErrInvalidCredentials
}

func (h *fakeHasher) dummyCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dummyCalls
}

type fakeIssuer struct {
	issued int
	err    error
}

func (i *fakeIssuer) Issue(context.Context, int64, ...string) (string, error) {
	if i.err != nil {
		return "", i.err
	}
	i.issued++
	return "access-token", nil
}

type fakeRefresh struct {
	issueErr   error
	consumeErr error
	consumeUID int64
	revoked    []string
	revokeErr  error
	// peekErr 模拟只读判定失败；peekCalls / consumeCalls 用于钉住提交次序。
	peekErr      error
	peekCalls    int
	consumeCalls int
}

func (r *fakeRefresh) Peek(context.Context, string) (int64, error) {
	r.peekCalls++
	if r.peekErr != nil {
		return 0, r.peekErr
	}
	return r.consumeUID, nil
}

func (r *fakeRefresh) Issue(context.Context, int64) (string, error) {
	if r.issueErr != nil {
		return "", r.issueErr
	}
	return "refresh-token", nil
}

func (r *fakeRefresh) Consume(_ context.Context, _ string) (int64, string, error) {
	r.consumeCalls++
	if r.consumeErr != nil {
		return 0, "", r.consumeErr
	}
	return r.consumeUID, "rotated-refresh", nil
}

func (r *fakeRefresh) RevokeFamilyOfToken(_ context.Context, token string) error {
	if r.revokeErr != nil {
		return r.revokeErr
	}
	r.revoked = append(r.revoked, token)
	return nil
}

type fakeRevocation struct {
	revoked map[string]time.Time
	err     error
}

func newFakeRevocation() *fakeRevocation {
	return &fakeRevocation{revoked: map[string]time.Time{}}
}

func (r *fakeRevocation) Revoke(_ context.Context, tokenID string, expiresAt time.Time) error {
	if r.err != nil {
		return r.err
	}
	r.revoked[tokenID] = expiresAt
	return nil
}

type fakeAuditor struct {
	mu       sync.Mutex
	audits   []string
	failures []string
}

func (a *fakeAuditor) Auditf(format string, _ ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.audits = append(a.audits, format)
}

func (a *fakeAuditor) Failf(format string, _ ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures = append(a.failures, format)
}

func (a *fakeAuditor) failureCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.failures)
}

// harness 是一套装配好的依赖，便于逐项替换。
type harness struct {
	creds   *fakeCreds
	hasher  *fakeHasher
	issuer  *fakeIssuer
	refresh *fakeRefresh
	revoke  *fakeRevocation
	auditor *fakeAuditor
	svc     *Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{
		creds:   newFakeCreds(),
		hasher:  &fakeHasher{},
		issuer:  &fakeIssuer{},
		refresh: &fakeRefresh{},
		revoke:  newFakeRevocation(),
		auditor: &fakeAuditor{},
	}
	svc, err := NewService(Options{
		Credentials:    h.creds,
		Hasher:         h.hasher,
		AccessIssuer:   h.issuer,
		RefreshStore:   h.refresh,
		RevocationList: h.revoke,
		Auditor:        h.auditor,
		AccessTTL:      15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h.svc = svc
	return h
}

// --- 用例 ---

func TestNewServiceRequiresEveryDependency(t *testing.T) {
	t.Parallel()

	full := Options{
		Credentials:    newFakeCreds(),
		Hasher:         &fakeHasher{},
		AccessIssuer:   &fakeIssuer{},
		RefreshStore:   &fakeRefresh{},
		RevocationList: newFakeRevocation(),
		AccessTTL:      time.Minute,
	}

	tests := []struct {
		name string
		mut  func(*Options)
	}{
		{name: "no credentials", mut: func(o *Options) { o.Credentials = nil }},
		{name: "no hasher", mut: func(o *Options) { o.Hasher = nil }},
		{name: "no access issuer", mut: func(o *Options) { o.AccessIssuer = nil }},
		{name: "no refresh store", mut: func(o *Options) { o.RefreshStore = nil }},
		{name: "no revocation list", mut: func(o *Options) { o.RevocationList = nil }},
		{name: "zero access ttl", mut: func(o *Options) { o.AccessTTL = 0 }},
		{name: "negative access ttl", mut: func(o *Options) { o.AccessTTL = -time.Second }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := full
			tt.mut(&opts)
			if _, err := NewService(opts); err == nil {
				t.Fatal("assembly succeeded with a missing dependency: it must fail at startup, " +
					"not at the first request")
			}
		})
	}

	if _, err := NewService(full); err != nil {
		t.Fatalf("a complete option set was rejected: %v", err)
	}
}

func TestRegisterIssuesTokens(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	pair, err := h.svc.Register(context.Background(), "13800000000", "pw", "nick")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("Register returned an incomplete token pair")
	}
	if pair.ExpiresIn != int64((15 * time.Minute).Seconds()) {
		t.Errorf("ExpiresIn = %d, want %d", pair.ExpiresIn, int64((15 * time.Minute).Seconds()))
	}

	// 存的是哈希，不是明文。
	cred := h.creds.byPhone["13800000000"]
	if strings.Contains(cred.PasswordHash, "pw") && cred.PasswordHash == "pw" {
		t.Fatal("the password was stored in plaintext")
	}
}

func TestRegisterRejectsTakenPhone(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Register(ctx, "13800000000", "pw", "nick"); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	_, err := h.svc.Register(ctx, "13800000000", "other", "nick2")
	if !errors.Is(err, ErrPhoneTaken) {
		t.Fatalf("second Register = %v, want ErrPhoneTaken (a normal business outcome, not a 500)", err)
	}
}

func TestLoginSucceedsWithRightPassword(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Register(ctx, "13800000000", "correct", "nick"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	pair, err := h.svc.Login(ctx, "13800000000", "correct")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("Login returned an incomplete token pair")
	}
}

// TestUnknownAccountStillComparesAPassword 是本层最重要的一条。
//
// 账号不存在时直接返回只需几微秒，而真实比对要几百毫秒。这个差值稳定、
// 可测量，攻击者据此可以枚举出哪些手机号已注册——一个不需要任何口令
// 就能拿到的用户名单。
func TestUnknownAccountStillComparesAPassword(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.svc.Login(context.Background(), "13900000000", "whatever")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login with an unknown account = %v, want ErrInvalidCredentials", err)
	}
	if got := h.hasher.dummyCallCount(); got != 1 {
		t.Fatalf("dummy comparisons = %d, want 1: without it the response time tells an "+
			"attacker whether the phone is registered", got)
	}
}

// TestUnknownAccountAndWrongPasswordAreIndistinguishable 钉住响应不可区分。
func TestUnknownAccountAndWrongPasswordAreIndistinguishable(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Register(ctx, "13800000000", "correct", "nick"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, unknownErr := h.svc.Login(ctx, "13900000000", "correct")
	_, wrongErr := h.svc.Login(ctx, "13800000000", "wrong")

	if !errors.Is(unknownErr, ErrInvalidCredentials) || !errors.Is(wrongErr, ErrInvalidCredentials) {
		t.Fatalf("unknown account = %v, wrong password = %v; both must be ErrInvalidCredentials",
			unknownErr, wrongErr)
	}
	if unknownErr.Error() != wrongErr.Error() {
		t.Fatalf("the two failures differ in text (%q vs %q): the response would tell an "+
			"attacker which phones exist", unknownErr, wrongErr)
	}
}

// TestGateFailureIsUniformAcrossAccountExistence 钉住防枚举的取消路径。
//
// 闸门饱和或请求取消时，掩护比对的错误若被吞掉，不存在的账号立刻回 401，
// 存在的账号却报基础设施错误——状态码与耗时重新泄漏账号是否存在。
func TestGateFailureIsUniformAcrossAccountExistence(t *testing.T) {
	t.Parallel()

	gateErr := fmt.Errorf("auth: wait for a password hashing slot: %w", context.Canceled)
	ctx := context.Background()

	// 不存在的账号：掩护比对报闸门错误。
	unknown := newHarness(t)
	unknown.hasher.dummyErr = gateErr
	_, unknownErr := unknown.svc.Login(ctx, "13900000000", "pw")

	// 存在的账号：真实比对报同一个闸门错误。
	known := newHarness(t)
	if _, err := known.svc.Register(ctx, "13800000000", "correct", "nick"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	known.hasher.compareErr = gateErr
	_, knownErr := known.svc.Login(ctx, "13800000000", "correct")

	if errors.Is(unknownErr, ErrInvalidCredentials) {
		t.Fatalf("unknown-account path masked the gate failure as invalid credentials: %v", unknownErr)
	}
	if !errors.Is(unknownErr, context.Canceled) || !errors.Is(knownErr, context.Canceled) {
		t.Fatalf("gate cancellation must propagate on both paths: unknown=%v known=%v",
			unknownErr, knownErr)
	}
}

// TestRegisterPartialSuccessIsExplicit 钉住"账号已建、令牌未签发"的表达。
//
// 表达成普通失败的话，客户端重试注册只会撞上"手机号已注册"——
// 一个既成事实被表达成了从未发生。
func TestRegisterPartialSuccessIsExplicit(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.refresh.issueErr = errors.New("redis is down")

	_, err := h.svc.Register(context.Background(), "13800000000", "pw", "nick")
	if !errors.Is(err, ErrRegisteredLoginRequired) {
		t.Fatalf("Register = %v, want ErrRegisteredLoginRequired", err)
	}
	if _, exists := h.creds.byPhone["13800000000"]; !exists {
		t.Fatal("the account was not created; the sentinel would be lying about a partial success")
	}
	if h.auditor.failureCount() == 0 {
		t.Fatal("a partial success must be audited at failure level")
	}
}

// TestInactiveAccountIsCheckedAfterThePassword 钉住检查顺序。
//
// 状态检查若在密码校验之前，被禁用的账号会比不存在的账号更快返回，
// 账号是否存在又从时间上泄漏了出去。
func TestInactiveAccountIsCheckedAfterThePassword(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.creds.byPhone["13800000000"] = Credential{
		UserID: 1, PasswordHash: "hashed:correct", Active: false,
	}
	ctx := context.Background()

	// 密码错误 + 账号停用：必须报凭据错误，因为密码先被检查。
	if _, err := h.svc.Login(ctx, "13800000000", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password on an inactive account = %v, want ErrInvalidCredentials "+
			"(the status check must not run first)", err)
	}

	// 密码正确 + 账号停用：这时才报账号状态。
	if _, err := h.svc.Login(ctx, "13800000000", "correct"); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("right password on an inactive account = %v, want ErrAccountInactive", err)
	}
}

// TestCorruptHashIsNotACredentialError 钉住坏数据不被当成密码错误。
func TestCorruptHashIsNotACredentialError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.creds.byPhone["13800000000"] = Credential{UserID: 1, PasswordHash: "garbage", Active: true}
	h.hasher.compareErr = errors.New("hash is not a valid bcrypt digest")

	_, err := h.svc.Login(context.Background(), "13800000000", "pw")
	if err == nil {
		t.Fatal("a corrupt hash let the login through")
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("a corrupt hash was reported as a wrong password: a batch of broken " +
			"credentials would look like every user forgot their password")
	}
}

// TestLookupFailureIsNotACredentialError 钉住基础设施故障可区分。
func TestLookupFailureIsNotACredentialError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.creds.findErr = errors.New("database is down")

	_, err := h.svc.Login(context.Background(), "13800000000", "pw")
	if errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("a database outage was reported as invalid credentials: users would be told " +
			"their password is wrong while the real problem is the database")
	}
}

func TestRefreshRotates(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.refresh.consumeUID = 42

	pair, err := h.svc.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if pair.RefreshToken != "rotated-refresh" {
		t.Errorf("RefreshToken = %q, want the rotated one", pair.RefreshToken)
	}
	if pair.AccessToken == "" {
		t.Error("Refresh returned no access token")
	}
}

// TestRefreshReuseIsAuditedAtFailureLevel 钉住重用被当成安全事件。
//
// 重用意味着令牌被复制了出去，而"过期"只是日常。两者共用一个日志级别的话，
// 真正需要有人看的那条会淹没在噪音里。
func TestRefreshReuseIsAuditedAtFailureLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		consumeErr  error
		wantFailure bool
	}{
		{name: "reuse is alerted", consumeErr: ErrRefreshReused, wantFailure: true},
		{name: "ordinary rejection is not", consumeErr: ErrRefreshRejected},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.refresh.consumeErr = tt.consumeErr

			_, err := h.svc.Refresh(context.Background(), "token")
			// 两种情况对客户端同义：都该重新登录。
			if !errors.Is(err, ErrRefreshRejected) {
				t.Fatalf("Refresh = %v, want ErrRefreshRejected", err)
			}

			if got := h.auditor.failureCount() > 0; got != tt.wantFailure {
				t.Errorf("failure-level audit = %v, want %v", got, tt.wantFailure)
			}
		})
	}
}

func TestLogoutRevokesBoth(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	expiry := time.Now().Add(10 * time.Minute)

	if err := h.svc.Logout(context.Background(), "jti-1", expiry, "refresh-token"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, ok := h.revoke.revoked["jti-1"]; !ok {
		t.Error("the access token was not revoked")
	}
	if len(h.refresh.revoked) != 1 {
		t.Error("the refresh family was not revoked")
	}
}

// TestLogoutWithoutRefreshTokenStillRevokesAccess 覆盖只带访问令牌的登出。
func TestLogoutWithoutRefreshTokenStillRevokesAccess(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	if err := h.svc.Logout(context.Background(), "jti-1", time.Now().Add(time.Minute), ""); err != nil {
		t.Fatalf("Logout without a refresh token: %v", err)
	}
	if _, ok := h.revoke.revoked["jti-1"]; !ok {
		t.Error("the access token was not revoked")
	}
}

// TestPartialLogoutIsAnError 钉住"登出一半"要报错。
//
// 访问令牌撤销了但刷新链还活着，意味着对方仍能换取新的访问令牌——
// 登出实际上没有完成。返回成功会让用户以为自己已经登出。
//
// 顺序契约（local-authentication spec）：先撤刷新族、后撤访问令牌。
// 族撤销失败时访问令牌必须仍然有效——否则同一个 access token 已进不了
// 登出端点（401），而刷新链还活着，重试路径被自己撤掉的令牌挡死。
func TestPartialLogoutIsAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.refresh.revokeErr = errors.New("redis is down")

	err := h.svc.Logout(context.Background(), "jti-1", time.Now().Add(time.Minute), "refresh")
	if err == nil {
		t.Fatal("logout reported success while the refresh chain stayed live: the user " +
			"would believe they are logged out")
	}
	if _, revoked := h.revoke.revoked["jti-1"]; revoked {
		t.Fatal("the access token was revoked before the refresh family: " +
			"a retry with the same token is now impossible while the family stays live")
	}
}

// TestLogoutAccessRevokeFailureLeavesBoundedResidue 钉住失败残留的边界：
// 刷新族已死、访问令牌撤销失败时报错——残留只是一个短 TTL 的访问令牌，
// 无法续期，且重试登出自然可行（令牌仍有效）。
func TestLogoutAccessRevokeFailureLeavesBoundedResidue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.revoke.err = errors.New("redis is down")

	err := h.svc.Logout(context.Background(), "jti-1", time.Now().Add(time.Minute), "refresh")
	if err == nil {
		t.Fatal("logout reported success while the access token stayed live")
	}
	if len(h.refresh.revoked) != 1 {
		t.Fatal("the refresh family was not revoked first")
	}
}

// denyAllAccounts 是恒拒绝的账号维度限流替身。
type denyAllAccounts struct{}

func (denyAllAccounts) Allow(context.Context, string) bool { return false }

// TestAccountRateLimitGatesCredentialAttempts 钉住账号维度限流（rate-limiting spec）：
// 换源攻击仍被账号维度拦截；拦截发生在凭据查找与 bcrypt 计算**之前**——
// 被限的尝试不该再消耗昂贵资源，也不该获得时间信道信息。
func TestAccountRateLimitGatesCredentialAttempts(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	svc, err := NewService(Options{
		Credentials:    h.creds,
		Hasher:         h.hasher,
		AccessIssuer:   h.issuer,
		RefreshStore:   h.refresh,
		RevocationList: h.revoke,
		Auditor:        h.auditor,
		AccessTTL:      15 * time.Minute,
		Accounts:       denyAllAccounts{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// 毒化下游：门禁一旦泄漏，错误会是毒化值而不是限流哨兵，
	// errors.Is 断言自身就能指认泄漏点。
	h.creds.findErr = errors.New("credential lookup must not be reached behind the gate")
	h.hasher.hashErr = errors.New("bcrypt must not be reached behind the gate")

	if _, err := svc.Login(context.Background(), "13800000000", "pw"); !errors.Is(err, ErrAccountRateLimited) {
		t.Fatalf("Login = %v, want ErrAccountRateLimited", err)
	}
	if _, err := svc.Register(context.Background(), "13800000000", "pw", "nick"); !errors.Is(err, ErrAccountRateLimited) {
		t.Fatalf("Register = %v, want ErrAccountRateLimited", err)
	}
	if got := h.hasher.dummyCallCount(); got != 0 {
		t.Fatalf("dummy compare ran %d time(s) behind the gate, want 0", got)
	}
}

// TestAccountLimiterNilMeansDimensionOff 钉住门控语义：未注入即维度关闭，
// 行为与既有部署完全一致。
func TestAccountLimiterNilMeansDimensionOff(t *testing.T) {
	t.Parallel()

	h := newHarness(t) // 默认不注入 Accounts
	if _, err := h.svc.Login(context.Background(), "13800000000", "pw"); errors.Is(err, ErrAccountRateLimited) {
		t.Fatal("a nil account limiter must not rate limit anything")
	}
}

// TestRefreshDoesNotConsumeWhenIssuanceFails 钉住刷新的提交次序（#9 回归）。
//
// 先消费再签发时，签发故障会留下一枚已作废的刷新令牌：客户端手里那枚不再
// 可用，而它唯一合理的动作（重试）超过重放窗后被判成重用，整族被撤销——
// 一次令牌签发的抖动升级成一次强制登出。
func TestRefreshDoesNotConsumeWhenIssuanceFails(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.refresh.consumeUID = 42
	h.issuer.err = errors.New("signing key unavailable")

	_, err := h.svc.Refresh(context.Background(), "refresh-token")
	if err == nil {
		t.Fatal("Refresh succeeded although access token issuance failed")
	}
	if errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("签发故障被表达成令牌失效：%v（客户端会被迫重新登录）", err)
	}
	if h.refresh.consumeCalls != 0 {
		t.Fatalf("consume 被调用 %d 次，want 0：签发失败时刷新令牌必须原封不动，重试才安全",
			h.refresh.consumeCalls)
	}
	if h.refresh.peekCalls != 1 {
		t.Fatalf("peek 调用 %d 次，want 1", h.refresh.peekCalls)
	}
}

// TestRefreshReuseStillRevokesFamily 钉住只读判定不削弱重用处置。
//
// Peek 是只读的，撤销必须仍然真的发生——否则重用只是被记了一笔而没有任何处置。
func TestRefreshReuseStillRevokesFamily(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.refresh.peekErr = ErrRefreshReused
	h.refresh.consumeErr = ErrRefreshReused

	_, err := h.svc.Refresh(context.Background(), "leaked-token")
	if !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("Refresh = %v, want ErrRefreshRejected", err)
	}
	// 撤销与检测的原子绑定在 Consume 内：必须补这一次调用。
	if h.refresh.consumeCalls != 1 {
		t.Fatalf("consume 被调用 %d 次，want 1：重用检测后族撤销没有发生", h.refresh.consumeCalls)
	}
	if h.auditor.failureCount() == 0 {
		t.Fatal("重用未以告警级别留痕")
	}
}

// TestRefreshRotatesAfterSuccessfulIssuance 钉住正常路径仍然轮换。
func TestRefreshRotatesAfterSuccessfulIssuance(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.refresh.consumeUID = 7

	pair, err := h.svc.Refresh(context.Background(), "refresh-token")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken != "rotated-refresh" {
		t.Fatalf("Refresh 返回 %+v，want 新 access + 轮换后的 refresh", pair)
	}
	if h.refresh.consumeCalls != 1 {
		t.Fatalf("consume 被调用 %d 次，want 1", h.refresh.consumeCalls)
	}
}

// TestAccountNormalizationClosesTheRateLimitBypass 钉住账号规范化（#11 回归）。
//
// 数据库列是 utf8mb4_unicode_ci（PAD SPACE）：尾随空格与原值是同一行，
// 而按原始输入派生的 HMAC 限流键却是两个桶——每次多加一个空格就能对同一个
// 账号无限尝试，账号维度限流形同虚设。
func TestAccountNormalizationClosesTheRateLimitBypass(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &recordingLimiter{}
	svc, err := NewService(Options{
		Credentials: h.creds, Hasher: h.hasher, AccessIssuer: h.issuer,
		RefreshStore: h.refresh, RevocationList: h.revoke, Auditor: h.auditor,
		AccessTTL: 15 * time.Minute, Accounts: seen,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	ctx := context.Background()
	if _, err := svc.Register(ctx, "13800000000", "pw", "nick"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// PAD SPACE 下这是同一个数据库账号。
	_, _ = svc.Login(ctx, "13800000000 ", "pw")
	_, _ = svc.Login(ctx, "  13800000000", "pw")

	for _, account := range seen.accounts {
		if account != "13800000000" {
			t.Fatalf("限流看到未规范化的账号 %q：等价文本落进了不同的限流桶", account)
		}
	}
	// 写入路径同样用规范化后的值，否则同一个账号会有两种存在形式。
	if _, ok := h.creds.byPhone["13800000000"]; !ok {
		t.Fatalf("账号未以规范化形式存储：%v", h.creds.byPhone)
	}
}

// TestInvalidAccountIsRejectedBeforeAnyHashing 钉住非法账号在昂贵计算之前被拒。
func TestInvalidAccountIsRejectedBeforeAnyHashing(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, phone string }{
		{name: "空", phone: ""},
		{name: "全空白", phone: "   "},
		{name: "内部空格", phone: "138 0000 0000"},
		{name: "内部制表符", phone: "138\t00000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			if _, err := h.svc.Login(context.Background(), tt.phone, "pw"); !errors.Is(err, ErrInvalidAccount) {
				t.Fatalf("Login(%q) = %v, want ErrInvalidAccount", tt.phone, err)
			}
			if got := h.hasher.dummyCallCount(); got != 0 {
				t.Fatalf("非法账号仍执行了 %d 次哈希比对：形态判定应在任何昂贵计算之前", got)
			}
			if _, err := h.svc.Register(context.Background(), tt.phone, "pw", "n"); !errors.Is(err, ErrInvalidAccount) {
				t.Fatalf("Register(%q) = %v, want ErrInvalidAccount", tt.phone, err)
			}
		})
	}
}

// recordingLimiter 记录限流看到的账号取值。
type recordingLimiter struct{ accounts []string }

func (l *recordingLimiter) Allow(_ context.Context, account string) bool {
	l.accounts = append(l.accounts, account)
	return true
}

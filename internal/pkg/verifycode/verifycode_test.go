package verifycode

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/testutil"
)

// fakeSender 记录发送调用；用真实 miniredis 而不是替身 Redis：
// 本包核心是脚本原子性与 TTL 语义，替身验证不了这些。
type fakeSender struct {
	mu      sync.Mutex
	calls   int
	targets []string
	codes   []string
	err     error
}

func (f *fakeSender) Send(_ context.Context, target, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.targets = append(f.targets, target)
	f.codes = append(f.codes, code)
	return f.err
}

func (f *fakeSender) lastCode(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.codes) == 0 {
		t.Fatal("sender was never called")
	}
	return f.codes[len(f.codes)-1]
}

func newTestVerifier(t *testing.T, opts Options) (*Verifier, *fakeSender, *miniredis.Miniredis) {
	t.Helper()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	sender := &fakeSender{}
	v, err := New(t.Context(), client, sender, opts)
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	return v, sender, srv
}

// stubRedis 满足 RedisClient 但不可用：专供「构造失败发生在触碰存储之前」
// 的用例，让这些用例在禁 loopback 的受限环境也能执行。
type stubRedis struct{}

func (stubRedis) ScriptLoad(context.Context, string) *redis.StringCmd { return nil }
func (stubRedis) EvalSha(context.Context, string, []string, ...any) *redis.Cmd {
	return nil
}
func (stubRedis) SetNX(context.Context, string, any, time.Duration) *redis.BoolCmd { return nil }
func (stubRedis) Set(context.Context, string, any, time.Duration) *redis.StatusCmd {
	return nil
}
func (stubRedis) Del(context.Context, ...string) *redis.IntCmd { return nil }

func TestNewValidatesDependenciesAndOptions(t *testing.T) {
	t.Parallel()

	client := stubRedis{}
	sender := &fakeSender{}

	tests := []struct {
		name   string
		client RedisClient
		sender Sender
		opts   Options
	}{
		{name: "nil client", client: nil, sender: sender},
		{name: "nil sender", client: client, sender: nil},
		{name: "code length too short", client: client, sender: sender, opts: Options{CodeLength: 3}},
		{name: "code length too long", client: client, sender: sender, opts: Options{CodeLength: 11}},
		{name: "ttl below floor", client: client, sender: sender, opts: Options{TTL: time.Millisecond}},
		{name: "interval below floor", client: client, sender: sender, opts: Options{SendInterval: time.Millisecond}},
		{name: "max attempts too large", client: client, sender: sender, opts: Options{MaxAttempts: 101}},
		{name: "debug without matcher", client: client, sender: sender, opts: Options{Debug: &Debug{FixedCode: "123456"}}},
		{
			name: "debug fixed code wrong length", client: client, sender: sender,
			opts: Options{Debug: &Debug{Match: func(string) bool { return true }, FixedCode: "12"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(t.Context(), tt.client, tt.sender, tt.opts); err == nil {
				t.Fatal("New() = nil error, want validation failure")
			}
		})
	}
}

// TestSendVerifyLifecycle 钉住主链路：签发→命中→销毁（二次提交不通过）。
func TestSendVerifyLifecycle(t *testing.T) {
	v, sender, _ := newTestVerifier(t, Options{})
	const target = "+8613800138000"

	if err := v.Send(t.Context(), target); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	code := sender.lastCode(t)
	if len(code) != defaultCodeLength || strings.Trim(code, "0123456789") != "" {
		t.Fatalf("code = %q, want %d digits", code, defaultCodeLength)
	}

	if err := v.Verify(t.Context(), target, code); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	// 命中即毁：同一验证码不可能通过第二次校验。
	if err := v.Verify(t.Context(), target, code); !errors.Is(err, ErrCodeNotFound) {
		t.Fatalf("second Verify() = %v, want ErrCodeNotFound", err)
	}
}

// TestVerifyAttemptLimitDestroysCode 钉住防爆破：超限销毁，正确码也不再通过。
func TestVerifyAttemptLimitDestroysCode(t *testing.T) {
	v, sender, _ := newTestVerifier(t, Options{MaxAttempts: 2})
	const target = "user@example.com"

	if err := v.Send(t.Context(), target); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	code := sender.lastCode(t)
	wrong := "000000"
	if wrong == code {
		wrong = "000001"
	}

	if err := v.Verify(t.Context(), target, wrong); !errors.Is(err, ErrCodeMismatch) {
		t.Fatalf("Verify() #1 = %v, want ErrCodeMismatch", err)
	}
	if err := v.Verify(t.Context(), target, wrong); !errors.Is(err, ErrCodeMismatch) {
		t.Fatalf("Verify() #2 = %v, want ErrCodeMismatch", err)
	}
	if err := v.Verify(t.Context(), target, wrong); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("Verify() #3 = %v, want ErrTooManyAttempts", err)
	}
	// 超限后验证码已销毁：正确码也不通过。
	if err := v.Verify(t.Context(), target, code); !errors.Is(err, ErrCodeNotFound) {
		t.Fatalf("Verify(correct after limit) = %v, want ErrCodeNotFound", err)
	}
}

// TestSendThrottleBlocksResend 钉住节流：间隔内重复请求被拒且不触发发送。
func TestSendThrottleBlocksResend(t *testing.T) {
	v, sender, srv := newTestVerifier(t, Options{})
	const target = "+8613800138000"

	if err := v.Send(t.Context(), target); err != nil {
		t.Fatalf("Send() #1 = %v", err)
	}
	if err := v.Send(t.Context(), target); !errors.Is(err, ErrThrottled) {
		t.Fatalf("Send() #2 = %v, want ErrThrottled", err)
	}
	if sender.calls != 1 {
		t.Fatalf("sender calls = %d, want 1 (throttled request must not send)", sender.calls)
	}

	// 间隔过后可再次发送。
	srv.FastForward(defaultSendInterval + time.Second)
	if err := v.Send(t.Context(), target); err != nil {
		t.Fatalf("Send() after interval = %v, want nil", err)
	}
}

// TestVerifyExpiredCode 钉住 TTL：过期后提交正确码返回未找到。
func TestVerifyExpiredCode(t *testing.T) {
	v, sender, srv := newTestVerifier(t, Options{})
	const target = "user@example.com"

	if err := v.Send(t.Context(), target); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	code := sender.lastCode(t)
	srv.FastForward(defaultTTL + time.Second)

	if err := v.Verify(t.Context(), target, code); !errors.Is(err, ErrCodeNotFound) {
		t.Fatalf("Verify(expired) = %v, want ErrCodeNotFound", err)
	}
}

// TestDebugBypassStoresWithoutSending 钉住测试豁免契约：只存不发，校验路径一致；
// 未命中匹配器的目标仍走真实发送。
func TestDebugBypassStoresWithoutSending(t *testing.T) {
	const fixed = "888888"
	v, sender, _ := newTestVerifier(t, Options{
		Debug: &Debug{
			Match:     func(target string) bool { return strings.HasPrefix(target, "+86138000") },
			FixedCode: fixed,
		},
	})

	if err := v.Send(t.Context(), "+8613800012345"); err != nil {
		t.Fatalf("Send(debug target) = %v", err)
	}
	if sender.calls != 0 {
		t.Fatalf("sender calls = %d, want 0 for exempted target", sender.calls)
	}
	if err := v.Verify(t.Context(), "+8613800012345", fixed); err != nil {
		t.Fatalf("Verify(fixed code) = %v, want nil", err)
	}

	if err := v.Send(t.Context(), "+8613900000000"); err != nil {
		t.Fatalf("Send(real target) = %v", err)
	}
	if sender.calls != 1 {
		t.Fatalf("sender calls = %d, want 1 for non-exempted target", sender.calls)
	}
}

// TestKeysCarryNoPlaintextTarget 钉住 PII 契约：明文目标不出现在任何 Redis 键里。
func TestKeysCarryNoPlaintextTarget(t *testing.T) {
	v, _, srv := newTestVerifier(t, Options{})
	const target = "13800138000"

	if err := v.Send(t.Context(), target); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	keys := srv.Keys()
	if len(keys) == 0 {
		t.Fatal("no keys written")
	}
	for _, k := range keys {
		if strings.Contains(k, target) {
			t.Fatalf("key %q leaks plaintext target", k)
		}
	}
}

// TestSendReportsSenderFailure 钉住发送器故障透传：码已落库（用户重试受节流约束），
// 错误如实返回。
func TestSendReportsSenderFailure(t *testing.T) {
	v, sender, _ := newTestVerifier(t, Options{})
	sender.err = errors.New("sms gateway down")

	err := v.Send(t.Context(), "+8613800138000")
	if err == nil || !strings.Contains(err.Error(), "sms gateway down") {
		t.Fatalf("Send() = %v, want wrapped sender failure", err)
	}
}

// TestVerifyRejectsMalformedInput 钉住输入域边界：空目标与长度不符的码
// 直接判不匹配，不打存储往返。
func TestVerifyRejectsMalformedInput(t *testing.T) {
	v, _, _ := newTestVerifier(t, Options{})

	for _, tt := range []struct{ target, code string }{
		{"", "123456"},
		{"  ", "123456"},
		{"+8613800138000", ""},
		{"+8613800138000", "12345"},
		{"+8613800138000", "1234567"},
	} {
		if err := v.Verify(t.Context(), tt.target, tt.code); !errors.Is(err, ErrCodeMismatch) {
			t.Fatalf("Verify(%q, %q) = %v, want ErrCodeMismatch", tt.target, tt.code, err)
		}
	}
}

// TestRandomDigitsShape 钉住验证码形态：固定长度、纯数字（受限环境可跑）。
func TestRandomDigitsShape(t *testing.T) {
	t.Parallel()

	for _, length := range []int{minCodeLength, defaultCodeLength, maxCodeLength} {
		code, err := randomDigits(length)
		if err != nil {
			t.Fatalf("randomDigits(%d) = %v", length, err)
		}
		if len(code) != length || strings.Trim(code, "0123456789") != "" {
			t.Fatalf("randomDigits(%d) = %q, want %d digits", length, code, length)
		}
	}
}

// TestVerifyInputGuardNeedsNoStore 钉住输入守卫在存储之前：
// 长度不符/空目标的提交在触碰 Redis 前即被判不匹配（用不可用的 stub 证明）。
func TestVerifyInputGuardNeedsNoStore(t *testing.T) {
	t.Parallel()

	opts, err := normalizeOptions(Options{})
	if err != nil {
		t.Fatalf("normalizeOptions() = %v", err)
	}
	v := &Verifier{client: stubRedis{}, sender: &fakeSender{}, opts: opts}

	for _, tt := range []struct{ target, code string }{
		{"", "123456"},
		{"+8613800138000", "12345"},
		{"+8613800138000", ""},
	} {
		// stubRedis 的方法返回 nil cmd，一旦被触碰立即 panic——
		// 本用例以此证明守卫路径不打存储往返。
		if err := v.Verify(t.Context(), tt.target, tt.code); !errors.Is(err, ErrCodeMismatch) {
			t.Fatalf("Verify(%q, %q) = %v, want ErrCodeMismatch", tt.target, tt.code, err)
		}
	}
}

// TestHashedKeysCarryNoPlaintext 钉住键派生的 PII 契约（受限环境可跑）。
func TestHashedKeysCarryNoPlaintext(t *testing.T) {
	t.Parallel()

	opts, err := normalizeOptions(Options{})
	if err != nil {
		t.Fatalf("normalizeOptions() = %v", err)
	}
	v := &Verifier{opts: opts}
	const target = "13800138000"
	h := v.hash(target)
	for _, key := range []string{v.codeKey(h), v.throttleKey(h), v.attemptsKey(h)} {
		if strings.Contains(key, target) {
			t.Fatalf("key %q leaks plaintext target", key)
		}
		if !strings.HasPrefix(key, defaultKeyPrefix+":") {
			t.Fatalf("key %q missing prefix %q", key, defaultKeyPrefix)
		}
	}
	// 同一目标的散列必须稳定，否则发送与校验会各查各的键。
	if v.hash(target) != h {
		t.Fatal("hash is not deterministic for the same target")
	}
}

// TestSenderFuncAdapts 钉住闭包适配器。
func TestSenderFuncAdapts(t *testing.T) {
	t.Parallel()

	var gotTarget, gotCode string
	fn := SenderFunc(func(_ context.Context, target, code string) error {
		gotTarget, gotCode = target, code
		return nil
	})
	if err := fn.Send(t.Context(), "a@b.c", "123456"); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	if gotTarget != "a@b.c" || gotCode != "123456" {
		t.Fatalf("SenderFunc passed (%q, %q), want original arguments", gotTarget, gotCode)
	}
}

// TestVerifySurvivesScriptCacheFlush 钉住 NOSCRIPT 重载契约：
// Redis 重启（脚本缓存清空）后校验仍然可用。移除重载逻辑本测试必须失败。
func TestVerifySurvivesScriptCacheFlush(t *testing.T) {
	v, sender, srv := newTestVerifier(t, Options{})
	const target = "+8613800138000"

	if err := v.Send(t.Context(), target); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	code := sender.lastCode(t)

	// miniredis 没有 SCRIPT FLUSH 的公开入口，用一个错误 SHA 模拟缓存失效：
	// 把缓存的 SHA 改成不存在的值，EvalSha 必然 NOSCRIPT，走重载路径。
	bogus := strings.Repeat("0", 40)
	v.verifySHA.Store(&bogus)

	if err := v.Verify(t.Context(), target, code); err != nil {
		t.Fatalf("Verify() after script cache flush = %v, want nil", err)
	}
	_ = srv
}

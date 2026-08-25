//go:build integration

// 真实 Redis 集成测试：验证码的原子校验脚本。
//
// 覆盖的轴——**只有真实 Redis 能回答的那些**：
//   - Lua 5.1 的真实语义与返回值编排。包内把脚本结果按 Int64 取用，
//     并把 1/0/-1/-2 映射成四种结果；miniredis 跑的是 gopher-lua，
//     它的类型转换与真实 Redis 是两套实现，对不上时单测全绿而线上判错。
//   - 命中即毁、尝试超限即销毁这两条**原子性**在真实服务上成立。
//   - SCRIPT FLUSH 之后的自愈（opt-in，见 RequireScriptFlushPermission）。
//     包内那条 NOSCRIPT 分支此前只对着手写替身验过——替身返回的错误字符串
//     是测试自己编的，那证明不了真实 Redis 会那样回复。
//
// 安全设计：连专用 db（默认 15），键带本次运行独占前缀，结束只删自己的键；
// 不 FLUSHDB。Redis 连不上时 Skip（REQUIRE_DB_INTEGRATION=1 时变 Fatal）。
//
// 运行方式：
//
//	go test -tags integration -run TestIntegration ./internal/pkg/verifycode/
//	ALLOW_REDIS_SCRIPT_FLUSH=1 go test -tags integration ./internal/pkg/verifycode/
package verifycode_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/verifycode"
	"github.com/gtkit/gin-api/internal/testutil"
)

// newVerifier 造一个连真实 Redis 的验证器，并把签发的码经 sender 捕获。
//
// 用 sender 捕获而不是 Options.Debug 的固定码：Debug 是显式后门（固定码 + 只存不发），
// 走它就绕开了真实的生成与存储路径，而这条测试要验的正是那条路径。
func newVerifier(t *testing.T, maxAttempts int) (*verifycode.Verifier, *string) {
	t.Helper()

	client := testutil.RealRedisOrSkip(t)
	prefix := testutil.UniqueKeyPrefix(t, client)

	var sent string
	verifier, err := verifycode.New(context.Background(), client,
		verifycode.SenderFunc(func(_ context.Context, _, code string) error {
			sent = code
			return nil
		}),
		verifycode.Options{
			KeyPrefix:   prefix,
			TTL:         time.Minute,
			MaxAttempts: maxAttempts,
			// 取包允许的下限 1s：本用例只 Send 一次，节流不会挡住它；
			// 节流本身有独立用例。包对更小的值直接报错而不是静默修正。
			SendInterval: time.Second,
		})
	if err != nil {
		t.Fatalf("verifycode.New against real redis: %v", err)
	}
	return verifier, &sent
}

// TestIntegrationVerifyConsumesOnHit 钉住命中即毁。
//
// 同一个码只能用一次。若脚本在真实 Lua 下没有删键（比如返回值类型让 Go 侧
// 误判成功却没走删除分支），第二次校验会照样通过——那是验证码可重放。
func TestIntegrationVerifyConsumesOnHit(t *testing.T) {
	ctx := context.Background()
	verifier, sent := newVerifier(t, 3)
	const target = "13800000001"

	if err := verifier.Send(ctx, target); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if *sent == "" {
		t.Fatal("the sender did not receive a code")
	}

	if err := verifier.Verify(ctx, target, *sent); err != nil {
		t.Fatalf("first Verify with the correct code: %v", err)
	}
	err := verifier.Verify(ctx, target, *sent)
	if !errors.Is(err, verifycode.ErrCodeNotFound) {
		t.Fatalf("second Verify = %v, want ErrCodeNotFound (the code must be single-use)", err)
	}
}

// TestIntegrationVerifyDestroysAfterMaxAttempts 钉住尝试超限即销毁。
//
// 计数与销毁在同一段脚本里完成（`INCR` 之后判 `n > MaxAttempts`）。
// 若真实 Lua 下计数没生效，暴力猜码就没有上界。
//
// 契约按脚本读出来写，不按名字猜：`MaxAttempts` 是**允许的次数**，
// 因此上限内的错误只是不匹配，第 `MaxAttempts+1` 次才销毁。
func TestIntegrationVerifyDestroysAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	const maxAttempts = 2
	verifier, sent := newVerifier(t, maxAttempts)
	const target = "13800000002"

	if err := verifier.Send(ctx, target); err != nil {
		t.Fatalf("Send: %v", err)
	}
	wrong := wrongCodeOf(*sent)

	// 上限内的错误只是不匹配。
	for i := 1; i <= maxAttempts; i++ {
		if err := verifier.Verify(ctx, target, wrong); !errors.Is(err, verifycode.ErrCodeMismatch) {
			t.Fatalf("attempt %d of %d = %v, want ErrCodeMismatch", i, maxAttempts, err)
		}
	}
	// 超出上限的那一次触发销毁。
	if err := verifier.Verify(ctx, target, wrong); !errors.Is(err, verifycode.ErrTooManyAttempts) {
		t.Fatalf("attempt %d (one past the limit) = %v, want ErrTooManyAttempts", maxAttempts+1, err)
	}
	// 销毁之后连正确的码也不再有效——否则"超限"只是拒绝了一次，没有真的毁掉。
	if err := verifier.Verify(ctx, target, *sent); !errors.Is(err, verifycode.ErrCodeNotFound) {
		t.Fatalf("Verify with the correct code after destruction = %v, want ErrCodeNotFound", err)
	}
}

// TestIntegrationSendThrottleIsAtomic 钉住 SetNX 节流在真实 Redis 上生效。
func TestIntegrationSendThrottleIsAtomic(t *testing.T) {
	ctx := context.Background()

	client := testutil.RealRedisOrSkip(t)
	prefix := testutil.UniqueKeyPrefix(t, client)
	verifier, err := verifycode.New(ctx, client,
		verifycode.SenderFunc(func(context.Context, string, string) error { return nil }),
		verifycode.Options{
			KeyPrefix:    prefix,
			TTL:          time.Minute,
			SendInterval: time.Minute, // 足够长，第二次必然被挡
		})
	if err != nil {
		t.Fatalf("verifycode.New: %v", err)
	}

	const target = "13800000003"
	if err := verifier.Send(ctx, target); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if err := verifier.Send(ctx, target); !errors.Is(err, verifycode.ErrThrottled) {
		t.Fatalf("second Send = %v, want ErrThrottled", err)
	}
}

// TestIntegrationVerifySurvivesScriptFlush 钉住脚本缓存被清空后的自愈。
//
// 这是真实 Redis 独有的价值：包内的 NOSCRIPT 分支此前只对着手写替身验过，
// 而那个替身返回的错误字符串是测试自己写的——它证明不了真实 Redis 会那样回复，
// 也证明不了 ScriptLoad 之后的重试真的能跑通。主从切换与 SCRIPT FLUSH
// 之后缓存就是空的，那时这条分支是唯一的恢复通道。
//
// opt-in：SCRIPT FLUSH 是服务器级操作，会清掉同一实例上别人的脚本缓存。
func TestIntegrationVerifySurvivesScriptFlush(t *testing.T) {
	testutil.RequireScriptFlushPermission(t)

	ctx := context.Background()
	client := testutil.RealRedisOrSkip(t)
	prefix := testutil.UniqueKeyPrefix(t, client)

	var sent string
	verifier, err := verifycode.New(ctx, client,
		verifycode.SenderFunc(func(_ context.Context, _, code string) error {
			sent = code
			return nil
		}),
		verifycode.Options{KeyPrefix: prefix, TTL: time.Minute, SendInterval: time.Second})
	if err != nil {
		t.Fatalf("verifycode.New: %v", err)
	}

	const target = "13800000004"
	if err := verifier.Send(ctx, target); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 清空服务端脚本缓存：此后包内缓存的 sha 必然 NOSCRIPT。
	if err := client.ScriptFlush(ctx).Err(); err != nil {
		t.Fatalf("SCRIPT FLUSH: %v", err)
	}

	if err := verifier.Verify(ctx, target, sent); err != nil {
		t.Fatalf("Verify after SCRIPT FLUSH: %v (the NOSCRIPT reload path did not recover)", err)
	}
}

// wrongCodeOf 返回一个与 code 等长但必然不同的码。
//
// 等长是必要的：长度不符的提交在进 Redis 之前就被判不匹配，压根不计入尝试，
// 那样就测不到脚本里的计数分支了。
func wrongCodeOf(code string) string {
	wrong := []byte(code)
	for i := range wrong {
		if wrong[i] == '0' {
			wrong[i] = '1'
		} else {
			wrong[i] = '0'
		}
	}
	return string(wrong)
}

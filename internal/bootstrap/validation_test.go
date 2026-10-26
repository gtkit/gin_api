package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/go-playground/validator/v10"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/goerr"
	"github.com/gtkit/verify"
)

func newProviders(ctx context.Context, runtime *Runtime, shutdown *ShutdownCoordinator) (*providerSet, error) {
	budget := &rollbackBudget{force: shutdown.ForcedCh()}
	defer budget.Close()
	return newProvidersWithBudget(ctx, runtime, shutdown, budget)
}

// TestInitValidationFailureBlocksProcessRuntime verifies the single startup entry.
func TestInitValidationFailureBlocksProcessRuntime(t *testing.T) {
	sentinel := errors.New("forced init failure")
	orig := verifyInit
	verifyInit = func(_ ...verify.Option) error { return sentinel }
	t.Cleanup(func() { verifyInit = orig })

	err := initProcessRuntime(&config.Config{})
	if err == nil {
		t.Fatal("validator failure did not block process initialization")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("process initialization error = %v, want wrapping %v", err, sentinel)
	}
}

// TestValidationOptionsWiredIntoInit 反证「注册位的选项确实进入初始化装配」：
// 把 initValidation 里的 validationOptions()... 传参删掉，本测试必须失败。
func TestValidationOptionsWiredIntoInit(t *testing.T) {
	const (
		customTag = "zhtestrule"
		customMsg = "测试规则不通过"
	)
	orig := validationOptions
	originalVerify := verifyInit
	calls := 0
	verifyInit = func(options ...verify.Option) error {
		calls++
		return originalVerify(options...)
	}
	t.Cleanup(func() { verifyInit = originalVerify })
	validationOptions = func() []verify.Option {
		return []verify.Option{
			verify.WithTranslation(customTag, customMsg, func(fl validator.FieldLevel) bool {
				return fl.Field().String() == "pass"
			}),
		}
	}
	t.Cleanup(func() { validationOptions = orig })

	if err := initProcessRuntime(&config.Config{}); err != nil {
		t.Fatalf("initProcessRuntime() = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("validator initialized %d times, want 1", calls)
	}

	type payload struct {
		Name string `binding:"zhtestrule" json:"name"`
	}
	verr := verify.Struct(payload{Name: "fail"})
	if verr == nil {
		t.Fatal("verify.Struct() = nil, want custom rule to reject")
	}
	translated := verify.StructErr(verr)
	item, ok := errors.AsType[goerr.Error](translated)
	if !ok {
		t.Fatalf("StructErr() = %T(%v), want goerr.Error", translated, translated)
	}
	if !strings.Contains(item.Message(), customMsg) {
		t.Fatalf("Message() = %q, want containing %q", item.Message(), customMsg)
	}
}

// TestInitValidationSucceedsWithBaselineOptions 锁定母版基线：注册位的默认内容
// 能成功初始化，且中文翻译器可用（渲染路径在就绪标志置位后直接使用它，不再判空重试）。
func TestInitValidationSucceedsWithBaselineOptions(t *testing.T) {
	if err := initValidation(); err != nil {
		t.Fatalf("initValidation() = %v, want nil", err)
	}
	if verify.Trans() == nil {
		t.Fatal("verify.Trans() = nil after successful init, want non-nil translator")
	}
}

// TestHTTPURLTagHasChineseMessage 反证「引入翻译包未收录的 tag 必须补中文消息」。
//
// validator 的中文翻译包收录了 `url` 与 `max`，**没有收录 `http_url`**。
// 而 UserUpdateRequest.Avatar 用的是后者（`url` 不限 scheme，会放行
// javascript: / data: 这类可渲染成脚本的值）。
//
// 缺了这条注册，该字段的校验依然生效，只是错误消息退回英文——一次静默的、
// 按字段的降级，只有真正触发该字段失败时才看得见，不会在任何门禁里自然暴露。
// 删掉注册项，本测试必须失败。
func TestHTTPURLTagHasChineseMessage(t *testing.T) {
	if err := initValidation(); err != nil {
		t.Fatalf("initValidation() = %v, want nil", err)
	}

	type payload struct {
		Link string `binding:"omitempty,http_url" json:"link"`
	}
	verr := verify.Struct(payload{Link: "javascript:alert(1)"})
	if verr == nil {
		t.Fatal("verify.Struct() = nil, want http_url to reject a javascript: scheme")
	}

	translated := verify.StructErr(verr)
	item, ok := errors.AsType[goerr.Error](translated)
	if !ok {
		t.Fatalf("StructErr() = %T(%v), want goerr.Error", translated, translated)
	}
	msg := item.Message()
	if !strings.ContainsFunc(msg, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		t.Fatalf("http_url 的对客消息不含中文: %q —— 翻译包未收录该 tag，注册位必须补上", msg)
	}
}

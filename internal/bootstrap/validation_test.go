package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/gtkit/goerr"
	"github.com/gtkit/verify"
)

// TestInitValidationFailureBlocksProviders 反证「初始化失败阻断启动」：
// 把 initValidation 从装配链上摘掉，本测试必须失败。
// runtime 传 nil 是安全的：校验翻译器是第 0 步，失败即返回，不触碰任何资源。
func TestInitValidationFailureBlocksProviders(t *testing.T) {
	sentinel := errors.New("forced init failure")
	orig := verifyInit
	verifyInit = func(_ ...verify.Option) error { return sentinel }
	t.Cleanup(func() { verifyInit = orig })

	_, err := newProviders(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("newProviders() = nil error, want validation init failure to block assembly")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("newProviders() error = %v, want wrapping %v", err, sentinel)
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
	validationOptions = func() []verify.Option {
		return []verify.Option{
			verify.WithTranslation(customTag, customMsg, func(fl validator.FieldLevel) bool {
				return fl.Field().String() == "pass"
			}),
		}
	}
	t.Cleanup(func() { validationOptions = orig })

	if err := initValidation(); err != nil {
		t.Fatalf("initValidation() = %v, want nil", err)
	}

	type payload struct {
		Name string `binding:"zhtestrule" json:"name"`
	}
	verr := verify.Struct(payload{Name: "fail"})
	if verr == nil {
		t.Fatal("verify.Struct() = nil, want custom rule to reject")
	}
	translated := verify.StructErr(verr)
	var item goerr.Error
	if !errors.As(translated, &item) {
		t.Fatalf("StructErr() = %T(%v), want goerr.Error", translated, translated)
	}
	if !strings.Contains(item.Message(), customMsg) {
		t.Fatalf("Message() = %q, want containing %q", item.Message(), customMsg)
	}
}

// TestInitValidationSucceedsWithEmptyOptions 锁定母版基线：注册位为空时初始化成功，
// 且中文翻译器可用（渲染路径在就绪标志置位后直接使用它，不再判空重试）。
func TestInitValidationSucceedsWithEmptyOptions(t *testing.T) {
	if err := initValidation(); err != nil {
		t.Fatalf("initValidation() = %v, want nil", err)
	}
	if verify.Trans() == nil {
		t.Fatal("verify.Trans() = nil after successful init, want non-nil translator")
	}
}

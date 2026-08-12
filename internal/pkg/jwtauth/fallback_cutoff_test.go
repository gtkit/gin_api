package jwtauth

import (
	"context"
	"errors"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"

	"github.com/gtkit/gojwt/claims"
)

// stubParser 是一个恒失败的主验证器，用于把控制权推给回退路径。
type stubParser struct{ err error }

func (s stubParser) ParseToken(
	context.Context, string, ...jwtv5.ParserOption,
) (*claims.Claims, error) {
	return nil, s.err
}

// TestFallbackStopsAtCutoff 钉住 Laravel 回退在截止时刻真的停止。
//
// 这条此前完全没有执行：截止时刻只在配置层被校验格式，运行期从未读取——
// 于是只要开关不关，存量令牌永久有效，而配置里那个日期给了运维一个
// **虚假的**下线承诺。
//
// 三个时点都测：只测截止前后而漏掉边界，会让"到点当刻是否还接受"这个
// 问题长期没有答案，而它恰恰是切换当天唯一有人关心的事。
func TestFallbackStopsAtCutoff(t *testing.T) {
	t.Parallel()

	cutoff := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	primaryErr := errors.New("primary rejects everything")

	verifier, err := NewLaravelVerifier("a-laravel-secret-long-enough-for-hs256", "hs256", "user", 0)
	if err != nil {
		t.Fatalf("NewLaravelVerifier: %v", err)
	}

	tests := []struct {
		name         string
		now          time.Time
		wantFellBack bool
	}{
		{name: "well before the cutoff", now: cutoff.Add(-24 * time.Hour), wantFellBack: true},
		{name: "one second before the cutoff", now: cutoff.Add(-time.Second), wantFellBack: true},
		{
			// 到点当刻即停止：Before 是严格小于，因此 cutoff 本身已在窗口之外。
			name: "exactly at the cutoff", now: cutoff, wantFellBack: false,
		},
		{name: "after the cutoff", now: cutoff.Add(time.Second), wantFellBack: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parser, err := NewFallbackParser(stubParser{err: primaryErr}, verifier, cutoff)
			if err != nil {
				t.Fatalf("NewFallbackParser: %v", err)
			}
			parser.now = func() time.Time { return tt.now }

			_, parseErr := parser.ParseToken(context.Background(), "not-a-valid-token")
			if parseErr == nil {
				t.Fatal("an invalid token must not parse")
			}

			// 回退跑过时，错误里会同时含主验证与 Laravel 两路的原因；
			// 未回退时只有主验证那一条。这是判断回退是否发生的可靠信号。
			fellBack := !errors.Is(parseErr, primaryErr) || parseErr.Error() != primaryErr.Error()
			if fellBack != tt.wantFellBack {
				t.Fatalf("fell back = %v, want %v (err = %v)", fellBack, tt.wantFellBack, parseErr)
			}
		})
	}
}

// TestFallbackRequiresCutoff 钉住启用回退必须给出终点。
//
// 没有终点的过渡就是永久的，而没有任何东西会提醒你去关掉它。
func TestFallbackRequiresCutoff(t *testing.T) {
	t.Parallel()

	verifier, err := NewLaravelVerifier("a-laravel-secret-long-enough-for-hs256", "hs256", "user", 0)
	if err != nil {
		t.Fatalf("NewLaravelVerifier: %v", err)
	}

	if _, err := NewFallbackParser(stubParser{}, verifier, time.Time{}); err == nil {
		t.Fatal("enabling the laravel fallback without a cutoff was accepted")
	}

	// 不启用回退时不要求截止时刻——那时它没有意义。
	if _, err := NewFallbackParser(stubParser{}, nil, time.Time{}); err != nil {
		t.Fatalf("a parser without fallback must not require a cutoff: %v", err)
	}
}

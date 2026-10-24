package captcha

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/testutil"
)

func newTestCaptcha(t *testing.T, opts Options) (*Captcha, *miniredis.Miniredis) {
	t.Helper()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("client.Close(): %v", err)
		}
	})

	c, err := New(client, opts)
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	return c, srv
}

// storedAnswer 直接从 miniredis 取出答案：测试需要“正确答案”而生成接口
// 刻意不返回它（答案不外泄正是被测契约）。
func storedAnswer(t *testing.T, srv *miniredis.Miniredis, c *Captcha, id string) string {
	t.Helper()
	answer, err := srv.Get(c.key(id))
	if err != nil {
		t.Fatalf("answer not stored for id %q: %v", id, err)
	}
	return answer
}

// stubRedis 满足 RedisClient 但不可用：构造校验发生在触碰存储之前，
// 这些用例在禁 loopback 的受限环境也要能执行。
type stubRedis struct{}

func (stubRedis) Set(context.Context, string, any, time.Duration) *redis.StatusCmd { return nil }
func (stubRedis) GetDel(context.Context, string) *redis.StringCmd                  { return nil }

func TestNewValidatesDependenciesAndOptions(t *testing.T) {
	t.Parallel()

	client := stubRedis{}

	tests := []struct {
		name   string
		client RedisClient
		opts   Options
	}{
		{name: "nil client", client: nil},
		{name: "ttl below floor", client: client, opts: Options{TTL: time.Millisecond}},
		{name: "length too short", client: client, opts: Options{Length: 3}},
		{name: "length too long", client: client, opts: Options{Length: 11}},
		{name: "negative width", client: client, opts: Options{Width: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.client, tt.opts); err == nil {
				t.Fatal("New() = nil error, want validation failure")
			}
		})
	}
}

// TestGenerateStoresAnswerAndReturnsImage 钉住生成契约：
// 返回 ID 与 base64 图片，答案只落库、不出现在返回值里。
func TestGenerateStoresAnswerAndReturnsImage(t *testing.T) {
	c, srv := newTestCaptcha(t, Options{})

	id, b64, err := c.Generate(t.Context())
	if err != nil {
		t.Fatalf("Generate() = %v, want nil", err)
	}
	if id == "" || b64 == "" {
		t.Fatalf("Generate() = (%q, %d bytes), want non-empty id and image", id, len(b64))
	}
	answer := storedAnswer(t, srv, c, id)
	if len(answer) != defaultLength || strings.Trim(answer, "0123456789") != "" {
		t.Fatalf("stored answer = %q, want %d digits", answer, defaultLength)
	}
	if b64 == answer {
		t.Fatal("image payload equals the answer: answer must not leak in the return values")
	}
}

// TestVerifyConsumesOnce 钉住一次性消费：命中后销毁，二次校验不通过。
func TestVerifyConsumesOnce(t *testing.T) {
	c, srv := newTestCaptcha(t, Options{})

	id, _, err := c.Generate(t.Context())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	answer := storedAnswer(t, srv, c, id)

	ok, err := c.Verify(t.Context(), id, answer)
	if err != nil || !ok {
		t.Fatalf("Verify() = (%t, %v), want (true, nil)", ok, err)
	}
	ok, err = c.Verify(t.Context(), id, answer)
	if err != nil || ok {
		t.Fatalf("second Verify() = (%t, %v), want (false, nil)", ok, err)
	}
}

// TestVerifyWrongAnswerDestroysCaptcha 钉住猜错即换图：错误答案同样销毁验证码。
func TestVerifyWrongAnswerDestroysCaptcha(t *testing.T) {
	c, srv := newTestCaptcha(t, Options{})

	id, _, err := c.Generate(t.Context())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	answer := storedAnswer(t, srv, c, id)
	wrong := "00000"
	if wrong == answer {
		wrong = "00001"
	}

	ok, err := c.Verify(t.Context(), id, wrong)
	if err != nil || ok {
		t.Fatalf("Verify(wrong) = (%t, %v), want (false, nil)", ok, err)
	}
	// 正确答案也已随之销毁。
	ok, err = c.Verify(t.Context(), id, answer)
	if err != nil || ok {
		t.Fatalf("Verify(correct after wrong) = (%t, %v), want (false, nil)", ok, err)
	}
}

// TestVerifyExpired 钉住 TTL：过期后校验不通过且不报错。
func TestVerifyExpired(t *testing.T) {
	c, srv := newTestCaptcha(t, Options{})

	id, _, err := c.Generate(t.Context())
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	answer := storedAnswer(t, srv, c, id)
	srv.FastForward(defaultTTL + time.Second)

	ok, err := c.Verify(t.Context(), id, answer)
	if err != nil || ok {
		t.Fatalf("Verify(expired) = (%t, %v), want (false, nil)", ok, err)
	}
}

// TestVerifyRejectsEmptyInput 钉住输入域边界：空 ID / 空答案直接不通过，
// 不打存储往返（stub 客户端被触碰即 panic，本用例以此为证；受限环境可跑）。
func TestVerifyRejectsEmptyInput(t *testing.T) {
	t.Parallel()

	c, err := New(stubRedis{}, Options{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	for _, tt := range []struct{ id, answer string }{
		{"", "12345"},
		{"someid", ""},
		{"  ", "  "},
	} {
		ok, err := c.Verify(t.Context(), tt.id, tt.answer)
		if err != nil || ok {
			t.Fatalf("Verify(%q, %q) = (%t, %v), want (false, nil)", tt.id, tt.answer, ok, err)
		}
	}
}

// TestGenerateWorksWithoutNetworkStore 钉住绘图与键派生的纯逻辑部分：
// Set 失败（stub 返回 nil cmd 会 panic——这里用返回错误的替身）时不发图。
type failingSetRedis struct{ stubRedis }

func (failingSetRedis) Set(ctx context.Context, _ string, _ any, _ time.Duration) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(ctx)
	cmd.SetErr(errors.New("store down"))
	return cmd
}

func TestGenerateFailsClosedWhenStoreRejects(t *testing.T) {
	t.Parallel()

	c, err := New(failingSetRedis{}, Options{})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	id, b64, err := c.Generate(t.Context())
	if err == nil {
		t.Fatal("Generate() = nil error, want store failure: an unverifiable image must not be issued")
	}
	if id != "" || b64 != "" {
		t.Fatalf("Generate() = (%q, %d bytes) despite store failure, want empty results", id, len(b64))
	}
}

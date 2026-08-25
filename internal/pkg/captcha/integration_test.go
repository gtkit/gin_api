//go:build integration

// 真实 Redis 集成测试：图形验证码的一次性消费。
//
// 覆盖的轴——**只有真实 Redis 能回答的那个**：`GETDEL` 是 Redis **6.2+** 才有的命令。
// miniredis 实现了它，因此内嵌测试在任何版本上都绿；而真实服务低于 6.2 时
// 这条路径直接报错，验证码从"用一次就废"退化成"根本用不了"。
// 本用例同时是那条版本下界的可执行声明。
//
// 一次性语义也在这里落到实处：`GETDEL` 的读与删是一个命令，
// 因此"无论对错同一 ID 只有一次机会"是原子的，不是两条命令拼出来的。
//
// 安全设计：连专用 db（默认 15），键带本次运行独占前缀，结束只删自己的键。
//
// 运行方式：
//
//	go test -tags integration -run TestIntegration ./internal/pkg/captcha/
package captcha_test

import (
	"context"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/captcha"
	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/redis/go-redis/v9"
)

// newCaptcha 造一个连真实 Redis 的验证码器，并返回读取答案用的键前缀。
func newCaptcha(t *testing.T) (*captcha.Captcha, *redis.Client, string) {
	t.Helper()

	client := testutil.RealRedisOrSkip(t)
	prefix := testutil.UniqueKeyPrefix(t, client)

	c, err := captcha.New(client, captcha.Options{KeyPrefix: prefix, TTL: time.Minute})
	if err != nil {
		t.Fatalf("captcha.New against real redis: %v", err)
	}
	return c, client, prefix
}

// TestIntegrationCaptchaCorrectAnswerIsAcceptedOnce 钉住正确答案只能用一次。
//
// 从 Redis 直接读答案是刻意的：本用例测的就是存储集成，
// 键格式（`<prefix>:<id>`）属于被测契约的一部分。
func TestIntegrationCaptchaCorrectAnswerIsAcceptedOnce(t *testing.T) {
	ctx := context.Background()
	c, client, prefix := newCaptcha(t)

	id, b64, err := c.Generate(ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if id == "" || b64 == "" {
		t.Fatalf("Generate returned id=%q, image len=%d", id, len(b64))
	}

	answer, err := client.Get(ctx, prefix+":"+id).Result()
	if err != nil {
		t.Fatalf("the answer was not stored under the documented key: %v", err)
	}

	ok, err := c.Verify(ctx, id, answer)
	if err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if !ok {
		t.Fatal("the correct answer was rejected")
	}

	// GETDEL 已经把它删了：同一个 ID 不能再用。
	ok, err = c.Verify(ctx, id, answer)
	if err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	if ok {
		t.Fatal("the same captcha id was accepted twice; it must be single-use")
	}
}

// TestIntegrationCaptchaWrongAnswerAlsoConsumes 钉住答错也消费掉这次机会。
//
// 这是防爆破的关键：若答错不消费，攻击者可以对同一张图无限次猜。
func TestIntegrationCaptchaWrongAnswerAlsoConsumes(t *testing.T) {
	ctx := context.Background()
	c, client, prefix := newCaptcha(t)

	id, _, err := c.Generate(ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	answer, err := client.Get(ctx, prefix+":"+id).Result()
	if err != nil {
		t.Fatalf("read stored answer: %v", err)
	}

	ok, err := c.Verify(ctx, id, answer+"x")
	if err != nil {
		t.Fatalf("Verify with a wrong answer: %v", err)
	}
	if ok {
		t.Fatal("a wrong answer was accepted")
	}

	// 机会已经用掉：即便这次给出正确答案也不该通过。
	ok, err = c.Verify(ctx, id, answer)
	if err != nil {
		t.Fatalf("Verify after a wrong attempt: %v", err)
	}
	if ok {
		t.Fatal("the correct answer still worked after a wrong attempt consumed the id")
	}

	// 键确实不在了——一次性不是"业务层假装看不见"，而是存储里真的删了。
	if err := client.Get(ctx, prefix+":"+id).Err(); err == nil {
		t.Fatal("the captcha key still exists after a verify attempt")
	}
}

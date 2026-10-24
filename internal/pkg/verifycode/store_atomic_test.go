package verifycode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

// Run another request immediately after the code becomes visible in Redis.
type interleavingClient struct {
	*redis.Client
	afterStore func(string)
}

func (c *interleavingClient) Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	cmd := c.Client.Set(ctx, key, value, ttl)
	if cmd.Err() == nil && c.afterStore != nil {
		c.afterStore(fmt.Sprint(value))
	}
	return cmd
}

func (c *interleavingClient) Process(ctx context.Context, cmd redis.Cmder) error {
	err := c.Client.Process(ctx, cmd)
	args := cmd.Args()
	if err == nil && len(args) == 7 && cmd.Name() == "evalsha" && strings.Contains(fmt.Sprint(args[3]), ":code:") && c.afterStore != nil {
		c.afterStore(fmt.Sprint(args[5]))
	}
	return err
}

func TestSendDoesNotResetConcurrentVerificationAttempts(t *testing.T) {
	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	client := &interleavingClient{Client: redis.NewClient(&redis.Options{Addr: srv.Addr()})}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	v, err := New(t.Context(), client, &fakeSender{}, Options{MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	const target = "atomic@example.com"
	var wrong string
	client.afterStore = func(code string) {
		wrong = "000000"
		if wrong == code {
			wrong = "111111"
		}
		if err := v.Verify(t.Context(), target, wrong); !errors.Is(err, ErrCodeMismatch) {
			t.Fatalf("verification during Send=%v, want ErrCodeMismatch", err)
		}
	}
	if err := v.Send(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if wrong == "" {
		t.Fatal("concurrent verification was not exercised")
	}
	if err := v.Verify(t.Context(), target, wrong); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("second verification=%v, want ErrTooManyAttempts after one earlier attempt", err)
	}
}

func TestSendSurvivesScriptCacheFlush(t *testing.T) {
	v, sender, _ := newTestVerifier(t, Options{})
	client, ok := v.client.(*redis.Client)
	if !ok {
		t.Fatalf("unexpected test client type %T", v.client)
	}
	if err := client.ScriptFlush(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	const target = "reload@example.com"
	if err := v.Send(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(t.Context(), target, sender.lastCode(t)); err != nil {
		t.Fatalf("verification after script cache flush=%v", err)
	}
}

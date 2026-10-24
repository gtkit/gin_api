package verifycode

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

type retryObserver struct{ flags []bool }

func (*retryObserver) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *retryObserver) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "evalsha" {
			h.flags = append(h.flags, cmd.NoRetry())
		}
		return next(ctx, cmd)
	}
}
func (*retryObserver) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestScriptsDoNotAutomaticallyReplayStateChanges(t *testing.T) {
	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	observer := &retryObserver{}
	client.AddHook(observer)
	sender := &fakeSender{}
	v, err := New(t.Context(), client, sender, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Send(t.Context(), "retry@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(t.Context(), "retry@example.com", sender.lastCode(t)); err != nil {
		t.Fatal(err)
	}
	if len(observer.flags) != 2 {
		t.Fatalf("observed %d state-changing script commands, want 2", len(observer.flags))
	}
	for _, noRetry := range observer.flags {
		if !noRetry {
			t.Error("state-changing script permits automatic replay after an uncertain response")
		}
	}
}

type processErrorClient struct {
	*redis.Client
	err error
}

func (c processErrorClient) Process(context.Context, redis.Cmder) error { return c.err }
func (processErrorClient) EvalSha(context.Context, string, []string, ...any) *redis.Cmd {
	return redis.NewCmdResult(int64(1), nil)
}

func TestVerifyPreservesProcessError(t *testing.T) {
	root := errors.New("command transport failed")
	opts, err := normalizeOptions(Options{})
	if err != nil {
		t.Fatal(err)
	}
	v := &Verifier{client: processErrorClient{err: root}, opts: opts}
	sha := "loaded"
	v.verifySHA.Store(&sha)
	if err := v.Verify(t.Context(), "user@example.com", "123456"); !errors.Is(err, root) {
		t.Fatalf("process error was lost: %v", err)
	}
}

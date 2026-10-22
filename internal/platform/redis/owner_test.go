package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestOpenCancellationPrecedesFactory(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p, err := Open(ctx, Options{Config: &config.Config{}})
	if p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("owner=%v err=%v", p, err)
	}
}
func TestUnavailableAccessorsAreSafe(t *testing.T) {
	p := new(Owner)
	if p.Client(0) != nil || p.DefaultClient() != nil || p.PoolStats() != nil || p.Configured() || p.HealthCheck(t.Context()) == nil {
		t.Fatal("unavailable owner published resources")
	}
	if client, ok := p.GetClient(0); client != nil || ok {
		t.Fatal("missing DB available")
	}
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

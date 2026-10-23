package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
)

func TestProbeRedis(t *testing.T) {
	testutil.RequireLoopback(t)
	for _, tc := range []struct {
		name, password string
		wantErr        bool
	}{
		{"success", "probe-test", false},
		{"authentication rejected", "wrong-test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			server.RequireAuth("probe-test")
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := ProbeRedis(ctx, config.RedisConfig{Addr: server.Addr(), Password: tc.password, DBs: []int{0}})
			if (err != nil) != tc.wantErr {
				t.Fatalf("ProbeRedis = %v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

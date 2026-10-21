package dao

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/redis/go-redis/v9"
)

func TestBudgetNormalize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		in              Budget
		wantTotal       time.Duration
		wantPerResource time.Duration
		wantErr         bool
	}{
		{
			name:            "zero budget falls back to defaults",
			in:              Budget{},
			wantTotal:       DefaultBudgetTotal,
			wantPerResource: DefaultBudgetPerResource,
		},
		{
			name:            "explicit budget preserved",
			in:              Budget{Total: 20 * time.Second, PerResource: 4 * time.Second},
			wantTotal:       20 * time.Second,
			wantPerResource: 4 * time.Second,
		},
		{
			name:            "negative values fall back to defaults",
			in:              Budget{Total: -time.Second, PerResource: -time.Second},
			wantTotal:       DefaultBudgetTotal,
			wantPerResource: DefaultBudgetPerResource,
		},
		{
			// 单资源上限超过总预算是自相矛盾的配置：它承诺了一个总预算根本兑现不了的时间。
			name:    "per-resource above total rejected",
			in:      Budget{Total: time.Second, PerResource: 2 * time.Second},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.in.normalize()
			if tc.wantErr {
				if err == nil {
					t.Fatal("normalize() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize() error = %v, want nil", err)
			}
			if got.Total != tc.wantTotal || got.PerResource != tc.wantPerResource {
				t.Fatalf("normalize() = %+v, want total=%s per=%s", got, tc.wantTotal, tc.wantPerResource)
			}
		})
	}
}

// TestBudgetResourceContextClampsToRemaining 钉住两级预算的关键性质：
// 单资源上限必须取 min(PerResource, 总预算剩余)。
//
// 只用 PerResource 的话，前一个资源耗掉大半总预算后，后一个资源仍会拿到
// 一个完整的 PerResource，从而冲破总预算——两级预算就形同虚设。
func TestBudgetResourceContextClampsToRemaining(t *testing.T) {
	t.Parallel()

	budget := Budget{Total: 10 * time.Second, PerResource: 8 * time.Second}

	t.Run("clamped by remaining total", func(t *testing.T) {
		t.Parallel()

		parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		ctx, cancelRes := budget.resourceContext(parent)
		defer cancelRes()

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("resource context has no deadline")
		}
		if remaining := time.Until(deadline); remaining > 2*time.Second {
			t.Fatalf("remaining = %s, want <= parent remaining (2s)", remaining)
		}
	})

	t.Run("clamped by per-resource limit", func(t *testing.T) {
		t.Parallel()

		parent, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()

		ctx, cancelRes := budget.resourceContext(parent)
		defer cancelRes()

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("resource context has no deadline")
		}
		if remaining := time.Until(deadline); remaining > budget.PerResource {
			t.Fatalf("remaining = %s, want <= per-resource limit %s", remaining, budget.PerResource)
		}
	})

	t.Run("exhausted budget yields an already expired context", func(t *testing.T) {
		t.Parallel()

		parent, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		time.Sleep(time.Millisecond)

		ctx, cancelRes := budget.resourceContext(parent)
		defer cancelRes()

		// 预算耗尽时必须立刻以超时失败，而不是给出一个看不出原因的连接错误。
		if err := ctx.Err(); err == nil {
			t.Fatal("resource context error = nil, want deadline exceeded on an exhausted budget")
		}
	})
}

// TestResourcesCloseReverseOrderAndIdempotent 同时钉住三条关闭契约：
// 逆序、幂等、单个失败不阻断其余。
//
// 逆序：后建立的资源可能依赖先建立的。
// 幂等：`defer Close()` 与显式关停路径同时存在是常态；重复关闭底层连接池会报错，
//
//	把一次正常关停染成失败。
//
// 不阻断：第一个关闭失败就返回，会让它之后的资源全部泄漏。
func TestResourcesCloseReverseOrderAndIdempotent(t *testing.T) {
	t.Parallel()

	var order []string
	firstErr := errors.New("close first failed")

	res := &Resources{
		closers: []namedCloser{
			{name: "first", close: func() error { order = append(order, "first"); return firstErr }},
			{name: "second", close: func() error { order = append(order, "second"); return nil }},
			{name: "third", close: func() error { order = append(order, "third"); return nil }},
		},
	}

	err := res.Close()
	if !errors.Is(err, firstErr) {
		t.Fatalf("Close() error = %v, want it to carry the failing closer's error", err)
	}
	if !strings.Contains(err.Error(), "first") {
		t.Fatalf("Close() error %q does not name the failing resource", err)
	}
	if want := []string{"third", "second", "first"}; !equalStrings(order, want) {
		t.Fatalf("close order = %v, want %v (reverse of construction)", order, want)
	}

	// 幂等：第二次调用不得重新执行任何 closer，且返回同一个结果。
	before := len(order)
	if second := res.Close(); !errors.Is(second, firstErr) {
		t.Fatalf("second Close() = %v, want the cached first result", second)
	}
	if len(order) != before {
		t.Fatalf("second Close() re-ran closers: order = %v", order)
	}
}

func TestResourcesCloseTolerantOfZeroValues(t *testing.T) {
	t.Parallel()

	var nilRes *Resources
	if err := nilRes.Close(); err != nil {
		t.Fatalf("Close() on nil Resources = %v, want nil", err)
	}

	// closer 为 nil 的条目必须跳过而不是 panic：它们来自"资源未建立"的分支。
	res := &Resources{closers: []namedCloser{{name: "missing"}}}
	if err := res.Close(); err != nil {
		t.Fatalf("Close() with a nil closer = %v, want nil", err)
	}
}

// TestResourcesAccessorsOnZeroValue 验证取值方法在资源缺席时返回 nil 而不是 panic。
func TestResourcesAccessorsOnZeroValue(t *testing.T) {
	t.Parallel()

	var res *Resources
	if got := res.Mdb(); got != nil {
		t.Fatalf("Mdb() on nil Resources = %v, want nil", got)
	}
	if got := res.Rdb(0); got != nil {
		t.Fatalf("Rdb(0) on nil Resources = %v, want nil", got)
	}
	if got := res.RedisAvailability(); got != nil {
		t.Fatalf("RedisAvailability() on nil Resources = %v, want nil", got)
	}
	if err := res.PingDB(context.Background()); err == nil {
		t.Fatal("PingDB() on nil Resources = nil, want error")
	}
	if err := res.PingRedis(context.Background()); err == nil {
		t.Fatal("PingRedis() on nil Resources = nil, want error")
	}
}

// TestNewResourcesRequiresConfig 验证缺配置时报错而不是 panic。
func TestNewResourcesRequiresConfig(t *testing.T) {
	t.Parallel()

	res, err := NewResources(
		context.Background(), nil, config.DBCredentials{}, Requirements{}, DefaultBudget(),
	)
	if err == nil {
		t.Fatal("NewResources(nil config) error = nil, want error")
	}
	if res != nil {
		t.Fatal("NewResources returned resources alongside an error")
	}
}

// TestNewResourcesRejectsInconsistentBudget 验证预算自相矛盾时在**建立任何连接之前**失败。
func TestNewResourcesRejectsInconsistentBudget(t *testing.T) {
	t.Parallel()

	res, err := NewResources(
		context.Background(), &config.Config{}, config.DBCredentials{}, Requirements{},
		Budget{Total: time.Second, PerResource: time.Minute},
	)
	if err == nil {
		t.Fatal("NewResources with an inconsistent budget = nil error, want error")
	}
	if res != nil {
		t.Fatal("NewResources returned resources alongside an error")
	}
}

func TestResourcesFromClientsDoesNotOwnPluginClient(t *testing.T) {
	t.Parallel()

	client, _ := newSQLMockOrmxClient(t)
	res, err := ResourcesFromClients(client, nil, false, nil)
	if err != nil {
		t.Fatalf("ResourcesFromClients: %v", err)
	}
	if res.Mdb() == nil {
		t.Fatal("ResourcesFromClients returned a view without the MySQL handle")
	}
	if err := res.Close(); err != nil {
		t.Fatalf("view Close: %v", err)
	}
	if client.DB() == nil {
		t.Fatal("view Close cleared the plugin-owned client")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("client.Close: %v", err)
	}
}

type fakeRedisHandle struct {
	clients   map[int]*redis.Client
	stats     map[int]*redis.PoolStats
	healthErr error
}

func (h fakeRedisHandle) GetClient(db int) (*redis.Client, bool) {
	client, ok := h.clients[db]
	return client, ok
}

func (h fakeRedisHandle) PoolStats() map[int]*redis.PoolStats { return h.stats }

func (h fakeRedisHandle) HealthCheck(context.Context) error { return h.healthErr }

func (h fakeRedisHandle) DB() *sql.DB { return nil }

func TestResourcesFromClientsBuildsRedisViewAndProbesIt(t *testing.T) {
	t.Parallel()

	mysql, _ := newSQLMockOrmxClient(t)
	redisClient := redis.NewClient(&redis.Options{Addr: "redis.invalid:6379"})
	redisView := fakeRedisHandle{
		clients:   map[int]*redis.Client{0: redisClient},
		stats:     map[int]*redis.PoolStats{0: redisClient.PoolStats()},
		healthErr: errors.New("redis unavailable"),
	}
	resources, err := ResourcesFromClients(mysql, redisView, true, nil)
	if err != nil {
		t.Fatalf("ResourcesFromClients: %v", err)
	}
	if !resources.RedisConfigured() || resources.RedisAvailability() == nil {
		t.Fatal("Redis view did not preserve configured state and availability tracker")
	}
	if resources.Rdb(0) != redisClient || resources.Rdb(1) != nil {
		t.Fatal("Rdb did not expose only initialized logical DBs")
	}
	if err := resources.PingRedis(t.Context()); !errors.Is(err, redisView.healthErr) {
		t.Fatalf("PingRedis error = %v, want health error", err)
	}
	if err := resources.PingDB(t.Context()); err == nil {
		t.Fatal("PingDB unexpectedly succeeded without a sqlmock expectation")
	}
	if err := resources.Close(); err != nil {
		t.Fatalf("view Close: %v", err)
	}
	if err := redisClient.Close(); err != nil {
		t.Fatalf("redis client Close: %v", err)
	}
	if err := mysql.Close(); err != nil {
		t.Fatalf("mysql client Close: %v", err)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

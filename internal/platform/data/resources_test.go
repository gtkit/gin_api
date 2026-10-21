package dao

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

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

func TestResourcesFromClientsDoesNotOwnPluginClient(t *testing.T) {
	t.Parallel()

	client, _ := newSQLMockOrmxClient(t)
	res, err := ResourcesFromClients(client, nil, false)
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

	mysql, mock := newSQLMockOrmxClient(t)
	redisClient := redis.NewClient(&redis.Options{Addr: "redis.invalid:6379"})
	redisView := fakeRedisHandle{
		clients:   map[int]*redis.Client{0: redisClient},
		stats:     map[int]*redis.PoolStats{0: redisClient.PoolStats()},
		healthErr: errors.New("redis unavailable"),
	}
	resources, err := ResourcesFromClients(mysql, redisView, true)
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
	mock.ExpectPing().WillReturnError(errors.New("database probe failed"))
	if err := resources.PingDB(t.Context()); err == nil {
		t.Fatal("PingDB unexpectedly succeeded without a sqlmock expectation")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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

// Availability belongs to the borrowed view, including permanent startup degradation.
func TestResourcesOwnsAvailabilityForMissingClients(t *testing.T) {
	t.Parallel()
	for _, configured := range []bool{false, true} {
		client, _ := newSQLMockOrmxClient(t)
		view, err := ResourcesFromClients(client, fakeRedisHandle{}, configured)
		if err != nil {
			t.Fatal(err)
		}
		if view.RedisConfigured() != configured || view.RedisAvailability().State() != AvailabilityDegraded {
			t.Fatal("startup distinction lost")
		}
		if err := view.PingRedis(t.Context()); err == nil {
			t.Fatal("missing client was probed")
		}
		view.RedisAvailability().Observe(nil)
		if view.RedisAvailability().State() != AvailabilityDegraded {
			t.Fatal("permanent degradation recovered without restart")
		}
	}
}

package plugin_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/plugin"
	"github.com/gtkit/gin-api/internal/router"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"gorm.io/gorm"
)

// Each alternative has its own lifecycle and capability; none imports builtin
// constructors or concrete gtkit SDKs. All resources in this test are offline.
type lifecycle struct {
	name        string
	events      *[]string
	initialized bool
}

func (p *lifecycle) Name() string { return p.name }
func (p *lifecycle) Init(context.Context) error {
	*p.events = append(*p.events, "init "+p.name)
	p.initialized = true
	return nil
}
func (p *lifecycle) Start(context.Context) error {
	*p.events = append(*p.events, "start "+p.name)
	return nil
}
func (p *lifecycle) Stop(context.Context) error {
	*p.events = append(*p.events, "stop "+p.name)
	return nil
}

type alternateConfiguration struct {
	lifecycle
	cfg *config.Config
}

func (p *alternateConfiguration) Value() (*config.Config, error) {
	if !p.initialized {
		return nil, plugin.ErrConfigurationUnavailable
	}
	return p.cfg, nil
}

type alternateLogging struct {
	lifecycle
	entries []applog.LogEntry
	release func()
}

func (p *alternateLogging) Init(ctx context.Context) error {
	if err := p.lifecycle.Init(ctx); err != nil {
		return err
	}
	release, err := applog.Install(p, nil)
	p.release = release
	return err
}
func (p *alternateLogging) Log(_ context.Context, entry applog.LogEntry) {
	entry.Fields = slices.Clone(entry.Fields)
	entry.Args = slices.Clone(entry.Args)
	p.entries = append(p.entries, entry)
}
func (p *alternateLogging) Stop(ctx context.Context) error {
	if p.release != nil {
		p.release()
	}
	return p.lifecycle.Stop(ctx)
}

type alternateDatabase struct {
	lifecycle
	db    *gorm.DB
	pings int
}

func (p *alternateDatabase) DB() *gorm.DB                      { return p.db }
func (*alternateDatabase) SQLDB() *sql.DB                      { return nil }
func (p *alternateDatabase) PingContext(context.Context) error { p.pings++; return nil }

type alternateRedis struct {
	lifecycle
	client *redis.Client
	pings  int
}

func (p *alternateRedis) Client(db int) *redis.Client {
	if db == 7 {
		return p.client
	}
	return nil
}
func (p *alternateRedis) GetClient(db int) (*redis.Client, bool) {
	client := p.Client(db)
	return client, client != nil
}
func (p *alternateRedis) DefaultClient() *redis.Client      { return p.client }
func (*alternateRedis) Configured() bool                    { return true }
func (p *alternateRedis) HealthCheck(context.Context) error { p.pings++; return nil }
func (p *alternateRedis) PoolStats() map[int]*redis.PoolStats {
	return map[int]*redis.PoolStats{7: p.client.PoolStats()}
}
func (p *alternateRedis) Stop(ctx context.Context) error {
	return errors.Join(p.lifecycle.Stop(ctx), p.client.Close())
}

type alternateCache struct {
	lifecycle
	requests int
}

func (*alternateCache) Enabled() bool { return true }
func (p *alternateCache) Middleware(time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) { p.requests++; c.Header("Selected-Cache", "alternative"); c.Next() }
}

type alternateHTTP struct {
	lifecycle
	deps  plugin.MiddlewareDeps
	built bool
}

func (p *alternateHTTP) Build(deps plugin.MiddlewareDeps) ([]gin.HandlerFunc, error) {
	if p.built {
		return nil, plugin.ErrMiddlewareAlreadyBuilt
	}
	p.built = true
	p.deps = deps
	return []gin.HandlerFunc{func(c *gin.Context) { c.Header("Selected-HTTP", "alternative"); c.Next() }}, nil
}

var (
	_ plugin.ConfigurationProvider = (*alternateConfiguration)(nil)
	_ plugin.Logger                = (*alternateLogging)(nil)
	_ plugin.DatabaseProvider      = (*alternateDatabase)(nil)
	_ plugin.RedisProvider         = (*alternateRedis)(nil)
	_ plugin.ResponseCache         = (*alternateCache)(nil)
	_ plugin.MiddlewareBuilder     = (*alternateHTTP)(nil)
)

func TestSixCapabilitiesCanBeReplacedWithoutCoreChanges(t *testing.T) {
	var events []string
	life := func(name string) lifecycle { return lifecycle{name: name, events: &events} }
	cfg := &alternateConfiguration{lifecycle: life("config-alternative"), cfg: &config.Config{}}
	logging := &alternateLogging{lifecycle: life("logging-alternative")}
	database := &alternateDatabase{lifecycle: life("database-alternative"), db: &gorm.DB{}}
	redisPlugin := &alternateRedis{lifecycle: life("redis-alternative"), client: redis.NewClient(&redis.Options{Addr: "offline.invalid:6379", DB: 7})}
	cache := &alternateCache{lifecycle: life("cache-alternative")}
	httpPlugin := &alternateHTTP{lifecycle: life("middleware-alternative")}
	registry := core.NewPluginRegistry()
	t.Cleanup(func() {
		if err := registry.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	for _, entry := range []struct {
		plugin       core.Plugin
		dependencies []string
	}{
		{cfg, nil}, {logging, []string{cfg.Name()}}, {database, []string{logging.Name()}}, {redisPlugin, []string{logging.Name()}}, {cache, []string{redisPlugin.Name()}}, {httpPlugin, []string{logging.Name(), cache.Name()}},
	} {
		if err := registry.Register(entry.plugin, entry.dependencies...); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 0 {
		t.Fatal("registration performed lifecycle work")
	}
	if _, err := cfg.Value(); !errors.Is(err, plugin.ErrConfigurationUnavailable) {
		t.Fatal("configuration was loaded during registration")
	}
	if err := registry.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := cfg.Value(); err != nil || got != cfg.cfg {
		t.Fatal("selected configuration unavailable")
	}
	applog.Infof("selected logger %s", "alternative")
	if len(logging.entries) != 1 || logging.entries[0].Text() != "selected logger alternative" {
		t.Fatal("facade bypassed selected logger")
	}
	view, err := data.ResourcesFromClients(database, redisPlugin, true)
	if err != nil {
		t.Fatal(err)
	}
	if view.Mdb() != database.db || view.Rdb(7) != redisPlugin.client || view.Rdb(0) != nil {
		t.Fatal("borrowed capabilities changed handles")
	}
	if err := view.PingDB(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := view.PingRedis(t.Context()); err != nil {
		t.Fatal(err)
	}
	if database.pings != 1 || redisPlugin.pings != 1 {
		t.Fatal("view bypassed selected health methods")
	}
	recorder, err := metrics.New(otel.Meter("replacement-contract"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := router.New("test", router.Options{Middleware: httpPlugin, Metrics: recorder, RegisterModules: func(v1 *gin.RouterGroup) {
		v1.GET("/cached", cache.Middleware(time.Second), func(c *gin.Context) { c.Status(http.StatusOK) })
		v1.GET("/plain", func(c *gin.Context) { c.Status(http.StatusOK) })
	}})
	if err != nil {
		t.Fatal(err)
	}
	if httpPlugin.deps.Metrics != recorder {
		t.Fatal("Build captured an earlier instrument")
	}
	for _, path := range []string{"/api/v1/cached", "/api/v1/plain"} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Header().Get("Selected-HTTP") != "alternative" {
			t.Fatal("selected HTTP middleware did not run")
		}
		if (response.Header().Get("Selected-Cache") != "") == strings.HasSuffix(path, "/plain") {
			t.Fatal("cache was not explicitly mounted")
		}
	}
	if cache.requests != 1 {
		t.Fatal("response cache bypassed selected implementation")
	}
	beforeClose := len(events)
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if len(events) != beforeClose {
		t.Fatal("borrower stopped owners")
	}
	if err := registry.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop middleware-alternative", "stop cache-alternative", "stop redis-alternative", "stop database-alternative", "stop logging-alternative", "stop config-alternative"}
	if !slices.Equal(events[len(events)-6:], want) {
		t.Fatalf("cleanup=%v", events)
	}
	count := len(events)
	if err := registry.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(events) != count {
		t.Fatal("registry stopped alternatives twice")
	}
}

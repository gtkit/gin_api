//go:build integration && !windows

package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-viper/mapstructure/v2"
	"github.com/gtkit/gin-api/internal/bootstrap"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/router"
	"github.com/gtkit/gin-api/internal/shared/env"
	"github.com/knadh/koanf/parsers/yaml"
	"go.opentelemetry.io/otel"
)

// Run by Make before any other integration package can connect.
func TestPluginIntegrationPrerequisites(t *testing.T) {
	if os.Getenv("PLUGIN_INTEGRATION") != "1" {
		if os.Getenv("REQUIRE_DB_INTEGRATION") == "1" {
			t.Fatal("required plugin integration cannot skip its prerequisites")
		}
		t.Skip("set PLUGIN_INTEGRATION=1 to validate the isolated integration root")
	}
	if _, err := isolatedPluginRoot(os.Getenv("APP_ROOT")); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated runtime config metadata validated; no dependency connection attempted")
}

func TestIsolatedPluginRoot(t *testing.T) {
	project, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", project, filepath.Join(project, "cmd"), filepath.Join(t.TempDir(), "missing")} {
		if _, err := isolatedPluginRoot(input); err == nil {
			t.Fatalf("accepted unsafe root %q", input)
		}
	}
	root := t.TempDir()
	configDir := filepath.Join(root, "config", "env")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(configDir, "env.yml")
	if err := os.WriteFile(file, []byte("# metadata-only fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := isolatedPluginRoot(root)
	want, pathErr := filepath.EvalSymlinks(root)
	if err != nil || pathErr != nil || got != want {
		t.Fatalf("valid root=%q err=%v pathErr=%v", got, err, pathErr)
	}
	outside := filepath.Join(t.TempDir(), "outside.yml")
	if err := os.WriteFile(outside, []byte("# no credentials\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file); err != nil {
		t.Fatal(err)
	}
	if _, err := isolatedPluginRoot(root); err == nil || !strings.Contains(err.Error(), "escapes APP_ROOT") {
		t.Fatalf("accepted config escape: %v", err)
	}
}

// This opt-in suite performs only startup/session checks and isolated cache writes
// on the existing local services. It never runs DDL, DML, FLUSH, or migrations.
func TestFixedRuntimeRealDependencies(t *testing.T) {
	if os.Getenv("PLUGIN_INTEGRATION") != "1" {
		if os.Getenv("REQUIRE_DB_INTEGRATION") == "1" {
			t.Fatal("REQUIRE_DB_INTEGRATION=1 requires PLUGIN_INTEGRATION=1; refusing a silent skip")
		}
		t.Skip("set PLUGIN_INTEGRATION=1 to use existing local MySQL/Redis")
	}
	base := localPluginConfiguration(t)
	t.Run("failed startup then recovery", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() {
			defer close(exited)
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				if err := conn.Close(); err != nil {
					t.Errorf("conn.Close(): %v", err)
				}
			}
		}()
		defer func() {
			if err := listener.Close(); err != nil {
				t.Errorf("listener.Close(): %v", err)
			}
			<-exited
		}()
		broken := base
		_, broken.Database.Port, err = net.SplitHostPort(listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		installPluginConfiguration(t, broken)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if runtime, err := bootstrap.NewRuntime(ctx, bootstrap.Options{}); runtime != nil || err == nil || !strings.Contains(err.Error(), "mysql") {
			t.Fatalf("expected MySQL handshake failure, got %v", err)
		}
	})
	for _, store := range []string{config.CacheStoreMemory, config.CacheStoreRedis} {
		t.Run(store, func(t *testing.T) { verifyRealPluginCacheAndClosure(t, base, store) })
	}
	t.Run("migration minimal readonly", func(t *testing.T) {
		installPluginConfiguration(t, base)
		runtime, err := bootstrap.NewMigrationRuntime(t.Context(), bootstrap.MigrationOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := runtime.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
		sqlDB, err := runtime.DB().DB()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := sqlDB.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runtime.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.PingContext(ctx); err == nil || err.Error() != "sql: database is closed" {
			t.Fatalf("migration SQL pool not closed: %v", err)
		}
	})
	t.Run("process signals", func(t *testing.T) { verifyRealPluginProcessSignals(t, base) })
}

func localPluginConfiguration(t *testing.T) config.Config {
	t.Helper()
	root, err := isolatedPluginRoot(os.Getenv("APP_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	// Remove every inherited application override before loading the explicit
	// isolated file. Host/port and synthetic key overrides below are owned here.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "APP_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	privatePath, publicPath := pluginIntegrationKeys(t)
	// Connection targets are forced before Load, so no configuration can redirect
	// this suite to a remote service. Passwords remain inside normal application IO.
	for key, value := range map[string]string{
		"APP_ROOT": root, "APP_ENV": "test", "APP_DATABASE__HOST": "127.0.0.1", "APP_DATABASE__PORT": "3306",
		"APP_REDIS__ADDR": "127.0.0.1:6379", "APP_AUTH__MODE": "external", "APP_CACHE__ENABLED": "false",
		"APP_JWT__ACTIVE__KID": "plugin-itest", "APP_JWT__ACTIVE__PRIVATE_KEY_PATH": privatePath, "APP_JWT__ACTIVE__PUBLIC_KEY_PATH": publicPath,
	} {
		t.Setenv(key, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Preserve resolved local credentials in memory, then remove APP_ overrides so
	// each subsequent runtime really consumes its own complete temporary file.
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(key, "APP_") {
			t.Setenv(key, value)
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg.Env = env.Test
	cfg.App.Name = fmt.Sprintf("plugin-itest-%d", time.Now().UnixNano())
	cfg.Redis.Prefix = cfg.App.Name
	cfg.App.Host = "127.0.0.1"
	cfg.App.EnableHTTPS = false
	cfg.App.DrainWaitSeconds = 1
	cfg.Database.Host, cfg.Database.Port = "127.0.0.1", "3306"
	cfg.Database.MaxOpenConn, cfg.Database.MaxIdleConn = 2, 1
	cfg.Redis.Addr = "127.0.0.1:6379"
	cfg.Redis.DBs = []int{0}
	cfg.Redis.PoolSize, cfg.Redis.MinIdleConns = 2, 1
	cfg.Redis.DialTimeoutMS, cfg.Redis.ReadTimeoutMS, cfg.Redis.WriteTimeoutMS = 500, 500, 500
	cfg.Auth = config.AuthConfig{Mode: config.AuthModeExternal}
	cfg.Modules = config.ModuleConfigs{}
	cfg.RateLimit = config.RateLimitConfig{}
	cfg.Outbound = config.OutboundConfig{}
	cfg.Tasks = config.TasksConfig{}
	cfg.Telemetry = config.TelemetryConfig{}
	cfg.Pprof = config.PprofConfig{}
	cfg.News = config.NewsConfig{}
	cfg.Email = config.EmailConfig{}
	cfg.SMS = config.SMSConfig{}
	cfg.Log = config.LogConfig{Path: filepath.Join(t.TempDir(), "app"), ConsoleStdout: 1, Level: "error"}
	cfg.Cache = config.CacheConfig{}
	cfg.JWT = config.JWTConfig{Active: config.JWTKeyConfig{KID: "plugin-itest", PrivateKeyPath: privatePath, PublicKeyPath: publicPath}, Issuer: "plugin-itest", Audiences: []string{"plugin-itest"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Log("real dependencies: MySQL=127.0.0.1:3306, Redis=127.0.0.1:6379; credentials omitted")
	return *cfg
}

// Validate only metadata before Load or any connection. Never fall back to the
// project's real runtime configuration, including through a config symlink.
func isolatedPluginRoot(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", errors.New("plugin integration requires an explicit isolated APP_ROOT")
	}
	root, err := filepath.Abs(input)
	if err != nil {
		return "", fmt.Errorf("resolve isolated APP_ROOT: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve isolated APP_ROOT: %w", err)
	}
	project, err := filepath.Abs("..")
	if err != nil {
		return "", err
	}
	project, err = filepath.EvalSymlinks(project)
	if err != nil {
		return "", err
	}
	projectRelative, err := filepath.Rel(project, root)
	if err != nil {
		return "", err
	}
	if projectRelative != ".." && !strings.HasPrefix(projectRelative, ".."+string(filepath.Separator)) {
		return "", errors.New("plugin integration refuses project APP_ROOT; prepare an isolated runtime file")
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, "config", "env", "env.yml"))
	if err != nil {
		return "", fmt.Errorf("inspect isolated config: %w", err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("isolated config escapes APP_ROOT")
	}
	projectRelative, err = filepath.Rel(project, path)
	if err != nil {
		return "", err
	}
	if projectRelative != ".." && !strings.HasPrefix(projectRelative, ".."+string(filepath.Separator)) {
		return "", errors.New("isolated config resolves inside the source project")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("isolated config must be a regular file")
	}
	return root, nil
}

func pluginIntegrationKeys(t *testing.T) (string, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	privatePath, publicPath := filepath.Join(dir, "private.pem"), filepath.Join(dir, "public.pem")
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return privatePath, publicPath
}

func installPluginConfiguration(t *testing.T, cfg config.Config) string {
	t.Helper()
	var raw map[string]any
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{Result: &raw, TagName: "mapstructure"})
	if err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(cfg); err != nil {
		t.Fatal(err)
	}
	// No business modules are enabled. Omit the section so nil tombstone maps
	// retain absence instead of becoming forbidden empty legacy objects.
	delete(raw, "modules")
	data, err := yaml.Parser().Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "config", "env")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env.yml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ROOT", root)
	return root
}

func verifyRealPluginCacheAndClosure(t *testing.T, base config.Config, store string) {
	t.Helper()
	cfg := base
	cfg.App.Name += "-" + store
	cfg.Redis.Prefix = cfg.App.Name
	cfg.Cache = config.CacheConfig{Enabled: true, Store: store, RedisDB: 0, TTL: 2 * time.Second, MaxBytes: 1 << 20, MaxBodyBytes: 4096}
	installPluginConfiguration(t, cfg)
	runtime, err := bootstrap.NewRuntime(t.Context(), bootstrap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	sqlDB, err := runtime.Resources().Mdb().DB()
	if err != nil {
		t.Fatal(err)
	}
	redisClient := runtime.Resources().Rdb(0)
	if sqlDB == nil || redisClient == nil || !runtime.Cache().Enabled() {
		t.Fatalf("real capability states: SQL=%t Redis=%t cache=%t configuredCache=%t", sqlDB != nil, redisClient != nil, runtime.Cache().Enabled(), runtime.Config().Cache.Enabled)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := runtime.Resources().PingDB(ctx); err != nil {
		t.Fatal(err)
	}
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	recorder, err := metrics.New(otel.Meter("plugin-integration"))
	if err != nil {
		t.Fatal(err)
	}
	var executions atomic.Int32
	engine, err := router.New(env.Test, router.Options{AllowedOrigins: cfg.CORS.AllowedOrigins, LogRequestBody: cfg.Log.AccessRequestBody, LogResponseBody: cfg.Log.AccessResponseBody, MaxBodyBytes: cfg.App.MaxBodyBytes, Metrics: recorder, RegisterModules: func(v1 *gin.RouterGroup) {
		v1.GET("/cache-probe", runtime.Cache().Middleware(0), func(c *gin.Context) { c.String(http.StatusOK, "response-%d", executions.Add(1)) })
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = time.Second
	first := pluginIntegrationGET(t, client, server.URL+"/api/v1/cache-probe", false)
	second := pluginIntegrationGET(t, client, server.URL+"/api/v1/cache-probe", false)
	if first != second || executions.Load() != 1 {
		t.Fatalf("cache failed: %q / %q executions=%d", first, second, executions.Load())
	}
	if third := pluginIntegrationGET(t, client, server.URL+"/api/v1/cache-probe", true); third == first || executions.Load() != 2 {
		t.Fatal("identity did not bypass public response cache")
	}
	if store == config.CacheStoreRedis {
		// Scan is bounded and restricted to this test's unique prefix. Only keys
		// returned by that prefix may be removed, never existing application data.
		var cursor uint64
		var ownKeys []string
		for range 128 {
			keys, next, err := redisClient.Scan(ctx, cursor, cfg.App.Name+":httpcache:*", 64).Result()
			if err != nil {
				t.Fatal(err)
			}
			ownKeys = append(ownKeys, keys...)
			cursor = next
			if cursor == 0 {
				break
			}
		}
		if cursor != 0 || len(ownKeys) == 0 {
			t.Fatalf("isolated cache scan incomplete or no keys (cursor=%d count=%d)", cursor, len(ownKeys))
		}
		for _, key := range ownKeys {
			ttl, err := redisClient.PTTL(ctx, key).Result()
			if err != nil || ttl <= 0 || ttl > 2*time.Second {
				t.Fatalf("cache TTL=%v error=%v", ttl, err)
			}
		}
		if err := redisClient.Del(ctx, ownKeys...).Err(); err != nil {
			t.Fatal(err)
		}
	}
	server.Close()
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.PingContext(ctx); err == nil || err.Error() != "sql: database is closed" {
		t.Fatalf("SQL pool stayed open: %v", err)
	}
	if err := redisClient.Ping(ctx).Err(); err == nil {
		t.Fatal("Redis client stayed open")
	}
	if sqlDB.Stats().OpenConnections != 0 || redisClient.PoolStats().TotalConns != 0 {
		t.Fatal("owned connections remain after Close")
	}
	t.Logf("%s: real HTTP cache hit, identity bypass, SQL/Redis pools closed", store)
}

func pluginIntegrationGET(t *testing.T, client *http.Client, url string, identity bool) string {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if identity {
		request.Header.Set("Authorization", "Bearer cache-bypass-probe")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status=%d", response.StatusCode)
	}
	return string(data)
}

func verifyRealPluginProcessSignals(t *testing.T, base config.Config) {
	t.Helper()
	projectRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "gin-api")
	build := exec.CommandContext(t.Context(), "go", "build", "-p=4", "-o", binary, ".")
	build.Dir = projectRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build child: %v\n%s", err, output)
	}
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprintf("forced=%t", forced), func(t *testing.T) {
			cfg := base
			cfg.Cache = config.CacheConfig{}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			_, cfg.App.Port, err = net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			root := installPluginConfiguration(t, cfg)
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "server")
			child.Dir = projectRoot
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if !strings.HasPrefix(key, "APP_") {
					child.Env = append(child.Env, entry)
				}
			}
			child.Env = append(child.Env, "APP_ROOT="+root)
			// Child output is discarded: this suite never copies configuration or
			// credentials into its report. Exit status and HTTP establish the contract.
			child.Stdout, child.Stderr = io.Discard, io.Discard
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			var waitErr error
			go func() { waitErr = child.Wait(); close(exited) }()
			defer func() {
				select {
				case <-exited:
				default:
					if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
						t.Error(err)
					}
					<-exited
				}
			}()
			url := "http://127.0.0.1:" + cfg.App.Port + "/ready"
			waitPluginProcessStatus(t, ctx, url, http.StatusOK, exited)
			if err := child.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if forced {
				waitPluginProcessStatus(t, ctx, url, http.StatusServiceUnavailable, exited)
				if err := child.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-exited:
			case <-ctx.Done():
				t.Fatal("child did not terminate within budget")
			}
			if forced == (waitErr == nil) {
				t.Fatalf("forced=%t exit=%v", forced, waitErr)
			}
			t.Logf("real process forced=%t: expected exit result observed", forced)
		})
	}
}

func waitPluginProcessStatus(t *testing.T, ctx context.Context, url string, want int, exited <-chan struct{}) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	for range 1500 {
		select {
		case <-exited:
			t.Fatal("child exited before readiness state")
		case <-ctx.Done():
			t.Fatal("child readiness deadline exceeded")
		default:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			closeErr := response.Body.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child did not report HTTP %d", want)
}

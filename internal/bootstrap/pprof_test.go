package bootstrap

import (
	"context"
	"io"
	"net"
	"net/http"
	"runtime"
	runtimepprof "runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// resetProfileRates 在用例前后清零采样率。
//
// 它们是**进程全局状态**，用例之间会互相污染；不清的话，一个开了采样的
// 用例会让后续全部用例都带着采样开销跑，且断言"已清零"的用例会假通过。
func resetProfileRates(t *testing.T) {
	t.Helper()
	runtime.SetBlockProfileRate(0)
	runtime.SetMutexProfileFraction(0)
	t.Cleanup(func() {
		runtime.SetBlockProfileRate(0)
		runtime.SetMutexProfileFraction(0)
	})
}

// TestPprofDisabledReturnsNoHandle 验证未启用时不建立任何监听。
func TestPprofDisabledReturnsNoHandle(t *testing.T) {
	resetProfileRates(t)

	h, err := startPprofServer(config.PprofConfig{Enabled: false})
	if err != nil {
		t.Fatalf("startPprofServer(disabled): %v", err)
	}
	if h != nil {
		if err := h.Shutdown(context.Background()); err != nil {
			t.Errorf("h.Shutdown(context.Background()): %v", err)
		}
		t.Fatal("a disabled pprof config produced a live handle")
	}
	// nil handle 上的方法必须安全：调用方不该被迫到处判空。
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on a nil handle = %v, want nil", err)
	}
	if h.Exited() {
		t.Fatal("Exited() on a nil handle = true")
	}
}

// TestPprofBindFailureIsAStartupError 钉住绑定失败即启动失败。
//
// 旧实现在后台 goroutine 里 ListenAndServe，绑定失败只打一条日志——
// 进程照常启动，配置里写着 enabled，而端点根本不存在。排障的人会
// 对着一个不存在的地址反复尝试。
func TestPprofBindFailureIsAStartupError(t *testing.T) {
	testutil.RequireLoopback(t)
	resetProfileRates(t)

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		if err := held.Close(); err != nil {
			t.Errorf("held.Close(): %v", err)
		}
	}()

	addr, ok := held.Addr().(*net.TCPAddr)

	if !ok {
		t.Fatalf("listener address has type %T", held.Addr())
	}

	port := strconv.Itoa(addr.Port)
	h, err := startPprofServer(config.PprofConfig{Enabled: true, Host: "127.0.0.1", Port: port})
	if err == nil {
		if err := h.Shutdown(context.Background()); err != nil {
			t.Errorf("h.Shutdown(context.Background()): %v", err)
		}
		t.Fatal("startPprofServer succeeded on an occupied port")
	}
	if !strings.Contains(err.Error(), port) {
		t.Fatalf("error %q does not name the conflicting address", err)
	}
}

// TestPprofBindFailureDoesNotEnableSampling 验证绑定失败时不开启采样。
//
// 端点根本不存在，却让整个进程持续承担 block/mutex 采样开销，
// 是纯粹的浪费——而它在监控上只表现为"莫名其妙的性能下降"。
func TestPprofBindFailureDoesNotEnableSampling(t *testing.T) {
	testutil.RequireLoopback(t)
	resetProfileRates(t)

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		if err := held.Close(); err != nil {
			t.Errorf("held.Close(): %v", err)
		}
	}()

	addr, ok := held.Addr().(*net.TCPAddr)

	if !ok {
		t.Fatalf("listener address has type %T", held.Addr())
	}

	port := strconv.Itoa(addr.Port)
	if _, err := startPprofServer(config.PprofConfig{
		Enabled: true, Host: "127.0.0.1", Port: port,
		BlockProfileRate: 10000, MutexProfileFraction: 100,
	}); err == nil {
		t.Fatal("startPprofServer succeeded on an occupied port")
	}

	if got := runtime.SetMutexProfileFraction(-1); got != 0 {
		t.Fatalf("mutex profile fraction = %d after a failed bind, want 0", got)
	}
}

// TestPprofServesAndShutsDownCleanly 验证端点真的可访问，且关停后端口释放。
func TestPprofServesAndShutsDownCleanly(t *testing.T) {
	testutil.RequireLoopback(t)
	resetProfileRates(t)

	h, err := startPprofServer(config.PprofConfig{Enabled: true, Host: "127.0.0.1", Port: "0"})
	if err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}

	addr := h.Addr()
	if addr == "" {
		t.Fatal("handle reports no address")
	}

	// 端点必须真的在服务，而不只是"绑上了端口"。
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Errorf("test response I/O: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("resp.Body.Close(): %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// 关停后端口必须可再绑：不释放的话，重启会因端口冲突失败。
	again, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("pprof port %s is still held after shutdown: %v", addr, err)
	}
	if err := again.Close(); err != nil {
		t.Errorf("again.Close(): %v", err)
	}
}

// TestPprofShutdownClearsProfileRates 钉住采样率在关停时被清零。
//
// 只关 server 不清零的话，一个已经不存在的端点会让进程持续付出采样开销。
func TestPprofShutdownClearsProfileRates(t *testing.T) {
	testutil.RequireLoopback(t)
	resetProfileRates(t)

	h, err := startPprofServer(config.PprofConfig{
		Enabled: true, Host: "127.0.0.1", Port: "0",
		BlockProfileRate: 10000, MutexProfileFraction: 100,
	})
	if err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}

	// SetMutexProfileFraction(-1) 只读取当前值，不修改。
	if got := runtime.SetMutexProfileFraction(-1); got != 100 {
		t.Fatalf("mutex profile fraction = %d after start, want 100", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if got := runtime.SetMutexProfileFraction(-1); got != 0 {
		t.Fatalf("mutex profile fraction = %d after shutdown, want 0", got)
	}
}

// TestPprofShutdownIsIdempotent 验证重复关停不报错、不重复清理。
//
// 关停触发源不止一个（正常关停、启动回滚），它们可能都调用 Shutdown。
func TestPprofShutdownIsIdempotent(t *testing.T) {
	testutil.RequireLoopback(t)
	resetProfileRates(t)

	h, err := startPprofServer(config.PprofConfig{Enabled: true, Host: "127.0.0.1", Port: "0"})
	if err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	// 第二次不得 panic；Serve 的结果已被取走，这里只验证不崩且不阻塞。
	second := make(chan error, 1)
	go func() { second <- h.Shutdown(ctx) }()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("second Shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second Shutdown blocked; it must not wait for a result that was already consumed")
	}
}

// TestPprofUsesItsOwnMux 验证不污染 DefaultServeMux。
//
// net/http/pprof 的 init 会把 handler 挂进全局 mux；业务 engine 若也用了它，
// pprof 就会暴露在业务端口上——那是一个无鉴权的运行时信息泄漏面。
func TestPprofUsesItsOwnMux(t *testing.T) {
	t.Parallel()

	mux := newPprofMux()
	if mux == nil {
		t.Fatal("newPprofMux returned nil")
	}
	if mux == http.DefaultServeMux {
		t.Fatal("pprof registered on the default mux; it would leak onto any server using it")
	}

	for _, path := range []string{
		"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/profile",
		"/debug/pprof/symbol", "/debug/pprof/trace",
	} {
		req := httptestRequest(path)
		if h, pattern := mux.Handler(req); h == nil || pattern == "" {
			t.Errorf("no handler registered for %s", path)
		}
	}
}

// TestPprofMuxServesEveryBuiltinProfile 钉住独立 mux 能服务 runtime/pprof 的
// **每一个**内置 profile，而不是一份手工维护的端点白名单。
//
// 为什么需要它：本仓刻意不用 net/http/pprof 的 init 副作用（那会把 handler 挂进全局
// DefaultServeMux，业务端口一旦也用它就连带暴露 pprof），改为手工注册。手工注册天然
// 诱导"把端点逐个列出来"——那看起来更明确，实际是把「Go 新增内置 profile」变成
// 「文档承诺的端点静默 404」。Go 1.27 的 goroutineleak 就是第一个撞上这条的：
// 它靠 pprof.Index 的按名分发自动可用，任何白名单式重构都会丢掉它且无人报错。
//
// 反证性质：把 newPprofMux 改成逐个显式注册并漏掉任一内置 profile，本测试即失败。
func TestPprofMuxServesEveryBuiltinProfile(t *testing.T) {
	t.Parallel()

	mux := newPprofMux()

	profiles := runtimepprof.Profiles()
	if len(profiles) == 0 {
		t.Fatal("runtime/pprof reports no builtin profiles; the check would be vacuous")
	}

	for _, p := range profiles {
		name := p.Name()
		h, pattern := mux.Handler(httptestRequest("/debug/pprof/" + name))
		if h == nil || pattern == "" {
			t.Errorf("no handler for builtin profile %q; a whitelist-style registration "+
				"would silently 404 the endpoint the pprof guide promises", name)
		}
	}
}

// TestPprofServesGoroutineLeakProfile 验证 goroutineleak 端点**真的能取到数据**，
// 而不只是"handler 已注册"。
//
// 手册承诺的是端点可用，所以断言落在响应上。注意该端点每次请求都会强制一次完整 GC
// （Go 1.27 的泄漏检测走 GC 可达性分析），因此它只出现在这一个用例里，不进任何循环。
func TestPprofServesGoroutineLeakProfile(t *testing.T) {
	testutil.RequireLoopback(t)
	resetProfileRates(t)

	h, err := startPprofServer(config.PprofConfig{Enabled: true, Host: "127.0.0.1", Port: "0"})
	if err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("http://" + h.Addr() + "/debug/pprof/goroutineleak?debug=1")
	if err != nil {
		t.Fatalf("GET /debug/pprof/goroutineleak: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Errorf("resp.Body.Close(): %v", err)
	}
	if readErr != nil {
		t.Fatalf("read body: %v", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", resp.StatusCode, body)
	}
	// debug=1 的文本形态以 profile 名开头；空泄漏集合（total 0）同样是有效结果——
	// 断言的是端点提供该 profile，不是本进程一定有泄漏。
	if !strings.Contains(string(body), "goroutineleak profile") {
		t.Fatalf("response is not a goroutineleak profile: %q", body)
	}
}

// httptestRequest 构造一个用于 mux 查询的最小请求。
func httptestRequest(path string) *http.Request {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1"+path, nil)
	if err != nil {
		panic(err)
	}
	return req
}

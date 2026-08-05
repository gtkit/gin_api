package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/logger/v2"
)

// requestIDPattern 是定长小写十六进制的判据，长度直接取自导出的契约常量——
// 常量改了而实现没跟上时，这条判据会立刻失败。
var requestIDPattern = regexp.MustCompile(
	fmt.Sprintf(`^[0-9a-f]{%d}$`, RequestIDLength))

func newRequestIDEngine(t *testing.T) (*gin.Engine, *[]string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	gen, err := NewRequestIDGenerator()
	if err != nil {
		t.Fatalf("NewRequestIDGenerator: %v", err)
	}
	mw := RequestID(gen)

	var (
		mu   sync.Mutex
		seen []string
	)
	engine := gin.New()
	engine.Use(mw)
	engine.GET("/probe", func(c *gin.Context) {
		mu.Lock()
		seen = append(seen, RequestIDFromContext(c))
		mu.Unlock()
		c.Status(http.StatusOK)
	})
	return engine, &seen
}

// TestRequestIDShapeAndUniqueness 钉住格式与唯一性。
//
// 定长小写十六进制是给下游用的：日志字段宽度稳定，正则匹配不必处理变长，
// 也不会出现大小写不一致导致的"同一个 ID 看起来是两个"。
func TestRequestIDShapeAndUniqueness(t *testing.T) {
	engine, seen := newRequestIDEngine(t)

	const requests = 32
	ids := make(map[string]struct{}, requests)
	for range requests {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

		id := w.Header().Get(RequestIDHeader)
		if !requestIDPattern.MatchString(id) {
			t.Fatalf("request id %q does not match the fixed 48-char lowercase hex shape", id)
		}
		if _, dup := ids[id]; dup {
			t.Fatalf("duplicate request id %q", id)
		}
		ids[id] = struct{}{}
	}

	if len(*seen) != requests {
		t.Fatalf("handler saw %d ids, want %d", len(*seen), requests)
	}
}

// TestRequestIDIgnoresClientSuppliedHeader 钉住"不信任客户端输入"。
//
// gin-contrib/requestid 会原样复用传入的 X-Request-ID。于是攻击者能把自己的
// 请求日志与他人的串在一起，也能传一个超长值制造巨大的日志字段。
// 内部 ID 必须只由本进程生成。
func TestRequestIDIgnoresClientSuppliedHeader(t *testing.T) {
	engine, _ := newRequestIDEngine(t)

	forged := "forged-" + strings.Repeat("a", 4096)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set(RequestIDHeader, forged)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	got := w.Header().Get(RequestIDHeader)
	if got == forged {
		t.Fatal("client-supplied request id was reused as the internal id")
	}
	if !requestIDPattern.MatchString(got) {
		t.Fatalf("request id %q is not a freshly generated internal id", got)
	}
}

// TestRequestIDReachesGoContext 验证 ID 同时进入 Go context。
//
// 业务错误日志与 SQL 日志都从 Go context 取它；只写 gin.Context 的话，
// 一条 SQL 慢查询日志将无法与产生它的请求关联。
func TestRequestIDReachesGoContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gen, err := NewRequestIDGenerator()
	if err != nil {
		t.Fatalf("NewRequestIDGenerator: %v", err)
	}
	mw := RequestID(gen)

	var fromGoCtx string
	engine := gin.New()
	engine.Use(mw)
	engine.GET("/probe", func(c *gin.Context) {
		fromGoCtx = logger.RequestIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if fromGoCtx == "" {
		t.Fatal("request id did not reach the Go context")
	}
	if fromGoCtx != w.Header().Get(RequestIDHeader) {
		t.Fatalf("Go context id %q differs from the response header %q",
			fromGoCtx, w.Header().Get(RequestIDHeader))
	}
}

// TestRequestIDGeneratorPrefixIsPerProcess 验证同一生成器的前缀稳定、
// 不同生成器的前缀不同。
//
// 前缀相同的两个实例会产出完全相同的 ID 序列，跨实例日志关联随之失效。
func TestRequestIDGeneratorPrefixIsPerProcess(t *testing.T) {
	t.Parallel()

	genA, err := NewRequestIDGenerator()
	if err != nil {
		t.Fatalf("NewRequestIDGenerator: %v", err)
	}
	genB, err := NewRequestIDGenerator()
	if err != nil {
		t.Fatalf("NewRequestIDGenerator: %v", err)
	}

	a1, _ := genA.Next()
	a2, _ := genA.Next()
	b1, _ := genB.Next()

	if a1[:32] != a2[:32] {
		t.Fatal("the process prefix changed between two ids from the same generator")
	}
	if a1[:32] == b1[:32] {
		t.Fatal("two generators produced the same process prefix")
	}
	if a1 == a2 {
		t.Fatal("the counter did not advance")
	}
}

// TestRequestIDCounterIsRaceFree 在并发下断言不产生重复 ID。
func TestRequestIDCounterIsRaceFree(t *testing.T) {
	t.Parallel()

	gen, err := NewRequestIDGenerator()
	if err != nil {
		t.Fatalf("NewRequestIDGenerator: %v", err)
	}

	const workers, perWorker = 8, 64
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		all = make(map[string]struct{}, workers*perWorker)
	)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range perWorker {
				id, err := gen.Next()
				if err != nil {
					t.Errorf("Next: %v", err)
					return
				}
				mu.Lock()
				all[id] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(all) != workers*perWorker {
		t.Fatalf("unique ids = %d, want %d", len(all), workers*perWorker)
	}
}

// TestRequestIDExhaustionFailsClosed 验证计数耗尽时不发重复号。
//
// 2^64 在真实进程里到不了，因此这是一道断言：真发生说明计数器被破坏了，
// 而重复 ID 会让日志关联给出**错误的结论**——那比没有 ID 更糟。
func TestRequestIDExhaustionFailsClosed(t *testing.T) {
	t.Parallel()

	gen, err := NewRequestIDGenerator()
	if err != nil {
		t.Fatalf("NewRequestIDGenerator: %v", err)
	}
	// 直接把计数推到回绕前一格（白盒）。
	gen.counter.Store(^uint64(0))

	if _, err := gen.Next(); err == nil {
		t.Fatal("Next() returned nil error on counter wrap-around; it must fail closed")
	}
}

// TestRequestIDMiddlewareRequiresGenerator 验证装配缺失时拒绝而不是静默放行。
func TestRequestIDMiddlewareRequiresGenerator(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("RequestID(nil) was accepted: a missing generator must fail assembly")
		}
	}()
	RequestID(nil)
}

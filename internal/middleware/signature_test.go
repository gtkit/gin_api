package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/pkg/apisign"
)

const testAppSecret = "test-app-secret-0123456789abcdef"

// fakeNonceStore 是内存版 nonce 去重，供测试注入，避免依赖真实 Redis。
type fakeNonceStore struct {
	seen map[string]bool
	err  error
}

func (s *fakeNonceStore) Record(_ context.Context, appID, nonce string, _ time.Duration) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	key := appID + ":" + nonce
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if s.seen[key] {
		return false, nil
	}
	s.seen[key] = true
	return true, nil
}

func newSignatureEngine(nonce apisign.NonceStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Signature(SignatureOptions{
		Secrets:     map[string]string{"svc_a": testAppSecret},
		Nonce:       nonce,
		MaxSkew:     time.Minute,
		ValidWindow: 5 * time.Minute,
		Now:         time.Now,
	}))
	engine.POST("/api/pay", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return engine
}

// signReq 按与中间件一致的规则签名，返回带签名头的请求。
func signReq(t *testing.T, method, target, body, appID, nonce string, ts int64) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(headerAppID, appID)
	req.Header.Set(headerTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(headerNonce, nonce)

	canonical := apisign.CanonicalString(
		method, req.URL.Path, req.URL.Query().Encode(), body, appID,
		strconv.FormatInt(ts, 10), nonce,
	)
	req.Header.Set(headerSign, apisign.Sign(testAppSecret, canonical))
	return req
}

func TestSignatureAcceptsValidRequest(t *testing.T) {
	engine := newSignatureEngine(&fakeNonceStore{})
	req := signReq(t, http.MethodPost, "/api/pay?order=x", `{"amount":100}`, "svc_a", "nonce-1", time.Now().Unix())

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusNoContent)
	}
}

func TestSignatureRejectsBadSignature(t *testing.T) {
	engine := newSignatureEngine(&fakeNonceStore{})
	req := signReq(t, http.MethodPost, "/api/pay", `{"amount":100}`, "svc_a", "nonce-1", time.Now().Unix())
	req.Header.Set(headerSign, "deadbeef")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSignatureRejectsBodyTampering(t *testing.T) {
	engine := newSignatureEngine(&fakeNonceStore{})
	req := signReq(t, http.MethodPost, "/api/pay", `{"amount":100}`, "svc_a", "nonce-1", time.Now().Unix())
	// 改 body 后签名失配。
	req.Body = io.NopCloser(strings.NewReader(`{"amount":999}`))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSignatureRejectsReplayedNonce(t *testing.T) {
	store := &fakeNonceStore{}
	engine := newSignatureEngine(store)

	first := signReq(t, http.MethodPost, "/api/pay", `{"amount":100}`, "svc_a", "nonce-1", time.Now().Unix())
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, first)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first request status=%d want %d", rec.Code, http.StatusNoContent)
	}

	replay := signReq(t, http.MethodPost, "/api/pay", `{"amount":100}`, "svc_a", "nonce-1", time.Now().Unix())
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, replay)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed request status=%d want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSignatureRejectsUnknownAppID(t *testing.T) {
	engine := newSignatureEngine(&fakeNonceStore{})
	req := signReq(t, http.MethodPost, "/api/pay", `{"amount":100}`, "svc_unknown", "nonce-1", time.Now().Unix())

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSignatureRejectsExpiredTimestamp(t *testing.T) {
	engine := newSignatureEngine(&fakeNonceStore{})
	// 时间戳距今 10 分钟，超过 5 分钟有效期 + 1 分钟钟差。
	req := signReq(t, http.MethodPost, "/api/pay", `{"amount":100}`, "svc_a", "nonce-1", time.Now().Add(-10*time.Minute).Unix())

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusUnauthorized)
	}
}

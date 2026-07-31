package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGetReturnsUnauthorizedWhenUserMissing(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/user/profile", nil)

	handler.Get(c)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":10010005") {
		t.Fatalf("expected auth error code, got %s", body)
	}
}

func TestUpdateRejectsOversizedNickname(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	longNick := strings.Repeat("x", 51) // 超过 nickname 列的 50 字符上限
	c.Request = httptest.NewRequest(
		http.MethodPut,
		"/v1/user",
		strings.NewReader(`{"nick_name":"`+longNick+`"}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	handler.Update(c)

	// 超长昵称必须在 binding 层被拒（不能出现成功码 0），避免超长值到库层才炸。
	body := recorder.Body.String()
	if strings.Contains(body, "\"code\":0") {
		t.Fatalf("oversized nickname was accepted, body=%s", body)
	}
}

func TestDeactivateRejectsMissingChannel(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/user/deactivate",
		strings.NewReader(`{"reason":"不再使用"}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	handler.Deactivate(c)

	if recorder.Code == http.StatusOK {
		body := recorder.Body.String()
		// 接口语义：缺必填 channel 必须返回错误码，不能 code:0。
		if strings.Contains(body, "\"code\":0") {
			t.Fatalf("missing channel was accepted, body=%s", body)
		}
	}
	body := recorder.Body.String()
	if !strings.Contains(strings.ToLower(body), "channel") {
		t.Fatalf("expected validation message to mention channel, got %s", body)
	}
}

func TestDeactivateReturnsUnauthorizedWhenUserMissing(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/user/deactivate",
		strings.NewReader(`{"channel":"app","reason":"不再使用"}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	handler.Deactivate(c)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":10010005") {
		t.Fatalf("expected auth error code, got %s", body)
	}
}

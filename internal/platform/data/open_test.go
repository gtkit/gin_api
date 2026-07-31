package dao

import (
	"errors"
	"strings"
	"testing"
)

// TestCloseAndWrapClosesExactlyOnce 守住最容易泄漏的那条路径：
// OpenDB 在 return 成功之前仍然拥有连接，post-open 校验失败时若直接 return，
// 调用方拿到的是 nil，没有可关闭的句柄，连接池就泄漏了。
func TestCloseAndWrapClosesExactlyOnce(t *testing.T) {
	t.Parallel()

	cause := errors.New("version check failed")
	calls := 0
	err := closeAndWrap(func() error { calls++; return nil }, cause)

	if calls != 1 {
		t.Fatalf("closer called %d times, want exactly 1", calls)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("root cause must remain matchable by errors.Is, got %v", err)
	}
}

// TestCloseAndWrapKeepsRootCauseWhenCloseFails 锁住主次关系：
// 关闭失败只是附加信息，不能盖掉真正的失败原因——
// 反过来会让调用方误以为问题出在关闭上。
func TestCloseAndWrapKeepsRootCauseWhenCloseFails(t *testing.T) {
	t.Parallel()

	cause := errors.New("session semantics mismatch")
	closeErr := errors.New("driver already shut down")
	err := closeAndWrap(func() error { return closeErr }, cause)

	if !errors.Is(err, cause) {
		t.Fatalf("root cause must still be matchable by errors.Is, got %v", err)
	}
	if !strings.Contains(err.Error(), closeErr.Error()) {
		t.Fatalf("close failure should be reported as additional information, got %v", err)
	}
}

func TestCloseAndWrapReturnsCauseUnchangedOnCleanClose(t *testing.T) {
	t.Parallel()

	cause := errors.New("boom")
	if got := closeAndWrap(func() error { return nil }, cause); got.Error() != cause.Error() {
		t.Fatalf("closeAndWrap() = %q, want the cause unchanged (%q)", got, cause)
	}
}

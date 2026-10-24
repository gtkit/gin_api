package dbtx

import (
	"context"
	"testing"

	"gorm.io/gorm"
)

// 本文件只覆盖 With / From —— 事务句柄在 context 里的传递。
//
// Manager.Do 的失败矩阵位于 transaction_test.go，复用项目已锁定的 sqlmock；
// 真实 MySQL 的嵌套原子性与池等待由 integration_test.go 验证。

// TestTransactionHandleRoundTrips 钉住事务句柄的存取。
//
// 这是整套事务传递的地基：句柄取不出来时，repository 会**静默地**用底座连接
// 而不是事务连接——写操作照常成功，但不在事务里，回滚时不会被撤销。
// 那种错误不报任何异常，只在事后对账时才暴露。
func TestTransactionHandleRoundTrips(t *testing.T) {
	t.Parallel()

	handle := &gorm.DB{}
	ctx := With(context.Background(), handle)

	got, ok := From(ctx)
	if !ok {
		t.Fatal("the handle stored by With is not visible to From")
	}
	if got != handle {
		t.Fatal("From returned a different handle than the one stored")
	}
}

// TestFromWithoutTransaction 验证不在事务内时报告 false。
//
// 返回 (nil, true) 的话，调用方会拿着 nil 句柄去查询并 panic；
// 返回 (nil, false) 才能让调用方回退到底座连接。
func TestFromWithoutTransaction(t *testing.T) {
	t.Parallel()

	got, ok := From(context.Background())
	if ok {
		t.Fatal("From reported a transaction on a plain context")
	}
	if got != nil {
		t.Fatal("From returned a non-nil handle while reporting no transaction")
	}
}

// TestFromIgnoresForeignValues 验证 context 里的无关值不会被误认成事务句柄。
//
// key 用的是包内私有的空结构体类型，因此外部无法伪造。但断言仍有必要：
// 若哪天 key 改成字符串常量，任何人往 context 里塞同名 key 就能让 repository
// 拿到一个不是事务句柄的东西——而那会在运行期以类型断言 panic 收场。
func TestFromIgnoresForeignValues(t *testing.T) {
	t.Parallel()

	type otherKey struct{}
	ctx := context.WithValue(context.Background(), otherKey{}, &gorm.DB{})

	if _, ok := From(ctx); ok {
		t.Fatal("From picked up a value stored under a different key")
	}
}

// TestNestedWithReplacesHandle 验证内层事务句柄覆盖外层。
//
// 嵌套调用时内层必须拿到自己那一层的句柄；仍取到外层的话，
// 内层写入会落到外层事务上，而两层的提交/回滚边界并不相同。
func TestNestedWithReplacesHandle(t *testing.T) {
	t.Parallel()

	outer, inner := &gorm.DB{}, &gorm.DB{}
	ctx := With(With(context.Background(), outer), inner)

	got, ok := From(ctx)
	if !ok {
		t.Fatal("no handle after nesting")
	}
	if got != inner {
		t.Fatal("the outer handle shadowed the inner one")
	}
}

// TestWithPreservesContextValues 验证注入事务不会丢掉既有 context 值。
//
// 事务注入发生在请求处理中途，此时 context 里已经有 request ID、超时、
// 认证主体等。若 With 换成一个新 context，这些会一并丢失——超时尤其致命：
// 事务内的查询会失去截止时间，一条慢查询能把连接占到底。
func TestWithPreservesContextValues(t *testing.T) {
	t.Parallel()

	type marker struct{}
	base := context.WithValue(context.Background(), marker{}, "request-42")

	ctx := With(base, &gorm.DB{})
	if got := ctx.Value(marker{}); got != "request-42" {
		t.Fatalf("pre-existing context value = %v, want it preserved", got)
	}

	deadlineCtx, cancel := context.WithCancel(base)
	cancel()
	if With(deadlineCtx, &gorm.DB{}).Err() == nil {
		t.Fatal("cancellation did not survive the transaction injection")
	}
}

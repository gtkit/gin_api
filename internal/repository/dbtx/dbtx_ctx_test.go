package dbtx

import (
	"context"
	"testing"

	"gorm.io/gorm"
)

// TestFromReportsWhetherATransactionIsInFlight 覆盖事务句柄的存取。
//
// ok=false 是本包最要紧的返回值：它决定仓储用事务句柄还是底座 db。
// 判错的方向都坏——误判有事务会用一个已回滚的句柄，误判没事务则让本该
// 在同一事务里的写入落到事务外，而后者在事务回滚时**不会被撤销**。
func TestFromReportsWhetherATransactionIsInFlight(t *testing.T) {
	t.Parallel()

	if _, ok := From(context.Background()); ok {
		t.Fatal("a plain context reported an in-flight transaction")
	}

	tx := &gorm.DB{}
	ctx := With(context.Background(), tx)

	got, ok := From(ctx)
	if !ok {
		t.Fatal("From did not find the transaction that With just injected")
	}
	if got != tx {
		t.Fatal("From returned a different handle than the one injected")
	}
}

// TestTransactionKeyIsPrivate 钉住事务键不会被外部意外命中或伪造。
//
// 用私有空结构体做键（而不是字符串），跨包碰撞在类型层面就不可能发生。
func TestTransactionKeyIsPrivate(t *testing.T) {
	t.Parallel()

	// 用一个字符串键放进去，From 必须看不见它。
	ctx := context.WithValue(context.Background(), "dbtx", &gorm.DB{}) //nolint:staticcheck // 刻意用字符串键
	if _, ok := From(ctx); ok {
		t.Fatal("a string-keyed value was mistaken for the transaction handle: " +
			"any package could then forge one")
	}
}

// TestNestedWithReplacesTheHandle 覆盖嵌套注入。
//
// 内层事务句柄必须覆盖外层：嵌套调用里仍拿到外层句柄的话，
// 内层的写入会落进一个它并不属于的事务。
func TestNestedWithReplacesTheHandle(t *testing.T) {
	t.Parallel()

	outer, inner := &gorm.DB{}, &gorm.DB{}
	ctx := With(With(context.Background(), outer), inner)

	got, ok := From(ctx)
	if !ok {
		t.Fatal("no transaction handle found")
	}
	if got != inner {
		t.Fatal("the outer handle shadowed the inner one: writes would join the wrong transaction")
	}
}

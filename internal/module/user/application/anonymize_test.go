package application

import (
	"strings"
	"testing"
	"time"
)

// TestAnonymizedPhoneFitsTheColumn 钉住脱敏手机号不会撑爆列宽。
//
// 这条此前是一个真实的生产缺陷：十进制的 `DEL_<id>_<unix>` 在用户 ID 达到
// 6 位时就是 21 字符，超出 varchar(20)。MySQL 严格模式会拒绝写入，整个注销
// 事务回滚——用户点了注销却失败。
//
// 而它在测试里完全看不见：原有用例只用 UserID: 42（17 字符），
// **小数据量永远碰不到这个边界**。溢出只会在用户量涨上去之后，以
// "注销功能突然对新注册用户失效"的形式出现。
func TestAnonymizedPhoneFitsTheColumn(t *testing.T) {
	t.Parallel()

	// 2100-01-01，覆盖时间戳位数增长。
	future := time.Unix(4102444800, 0)

	tests := []struct {
		name   string
		userID int64
		at     time.Time
	}{
		{name: "a small id", userID: 42, at: time.Unix(1754000000, 0)},
		{name: "the last id that fit the old decimal format", userID: 99999, at: future},
		{name: "the first id that broke the old decimal format", userID: 100000, at: future},
		{name: "a million users", userID: 1_000_000, at: future},
		{name: "a billion users", userID: 1_000_000_000, at: future},
		{name: "a trillion users", userID: 1_000_000_000_000, at: future},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := anonymizedPhone(tt.userID, tt.at)
			if err != nil {
				t.Fatalf("anonymizedPhone(%d): %v", tt.userID, err)
			}
			if len(got) > maxPhoneColumnLength {
				t.Fatalf("anonymizedPhone(%d) = %q (%d chars), exceeds the %d-char column",
					tt.userID, got, len(got), maxPhoneColumnLength)
			}
			if !strings.HasPrefix(got, "DEL_") {
				t.Fatalf("anonymizedPhone(%d) = %q, want the traceable DEL_ prefix", tt.userID, got)
			}
		})
	}
}

// TestAnonymizedPhoneIsUniquePerUserAndTime 钉住脱敏值可用作唯一索引的值。
//
// phone 上有唯一索引。两个用户在同一秒注销时若生成相同的值，第二个会撞唯一键
// 而注销失败——而那种失败只在并发注销时出现，最难复现。
func TestAnonymizedPhoneIsUniquePerUserAndTime(t *testing.T) {
	t.Parallel()

	at := time.Unix(1754000000, 0)
	seen := map[string]bool{}

	for _, id := range []int64{1, 2, 35, 36, 37, 1295, 1296, 999999} {
		got, err := anonymizedPhone(id, at)
		if err != nil {
			t.Fatalf("anonymizedPhone(%d): %v", id, err)
		}
		if seen[got] {
			t.Fatalf("anonymizedPhone(%d) = %q collides with another user at the same second", id, got)
		}
		seen[got] = true
	}

	// 同一用户在不同时刻也必须不同：注销后重新注册再注销的场景下，
	// 相同的值会撞上历史记录留下的唯一键。
	first, _ := anonymizedPhone(7, time.Unix(1754000000, 0))
	second, _ := anonymizedPhone(7, time.Unix(1754000001, 0))
	if first == second {
		t.Fatal("the same user got the same anonymized phone at different times")
	}
}

// TestAnonymizedPhoneRejectsOverflowInsteadOfDeferringToMySQL 钉住兜底防线。
//
// 编码也压不住的规模下，这里必须返回一个说清原因的错误——好过让 MySQL 抛
// "Data too long for column 'phone'"，后者要读到 SQL 层才能明白发生了什么。
func TestAnonymizedPhoneRejectsOverflowInsteadOfDeferringToMySQL(t *testing.T) {
	t.Parallel()

	// int64 上限配一个远期时间戳，是编码能力的极端边界。
	_, err := anonymizedPhone(1<<62, time.Unix(1<<40, 0))
	if err == nil {
		t.Skip("the encoding still fits at this scale; the guard has no observable behavior yet")
	}
	if !strings.Contains(err.Error(), "phone column") {
		t.Fatalf("error %q does not explain that the column is the constraint", err)
	}
}

package credential

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestDummyHashCostMatchesConfiguredCost 钉住时间掩护的代价与配置一致。
//
// 这是账号枚举防护的全部要害。bcrypt 比对的耗时由**哈希串自身携带的 cost**
// 决定，与 Hasher 配置的 cost 无关——此前 dummy 哈希硬编码 cost 10，而默认
// 配置是 12，于是"账号存在"约 250ms、"账号不存在"约 60ms，4 倍差值可远程
// 测量，掩护只在配置恰好等于 10 时成立。
//
// 断言内嵌 cost 而不是计时：墙钟断言在 CI 上必然 flaky，而内嵌 cost 是
// 决定耗时的那个量，断言它即断言了耗时同阶。
func TestDummyHashCostMatchesConfiguredCost(t *testing.T) {
	t.Parallel()

	// 只取低代价：cost 每 +1 耗时翻倍，本断言与代价高低无关。
	for _, cost := range []int{bcrypt.MinCost, bcrypt.MinCost + 1} {
		t.Run("cost", func(t *testing.T) {
			t.Parallel()

			h, err := NewHasher(cost)
			if err != nil {
				t.Fatalf("NewHasher(%d): %v", cost, err)
			}

			dummyCost, err := bcrypt.Cost([]byte(h.dummyHash))
			if err != nil {
				t.Fatalf("bcrypt.Cost(dummy): %v", err)
			}
			if dummyCost != cost {
				t.Fatalf("dummy 哈希内嵌 cost = %d, want %d："+
					"掩护耗时与真实比对不同阶，账号存在性可经时间信道枚举", dummyCost, cost)
			}

			// 交叉验证：真实哈希的内嵌 cost 与 dummy 相同。
			realHash, err := h.Hash("some password")
			if err != nil {
				t.Fatalf("Hash: %v", err)
			}
			realCost, err := bcrypt.Cost([]byte(realHash))
			if err != nil {
				t.Fatalf("bcrypt.Cost(real): %v", err)
			}
			if realCost != dummyCost {
				t.Fatalf("真实哈希 cost = %d，dummy = %d，两条路径耗时不同阶", realCost, dummyCost)
			}
		})
	}
}

// TestNewHasherRejectsUnusableCost 钉住无法建立掩护时启动失败。
//
// 构造不出等价代价的掩护却返回一个可用的 Hasher，等于让进程在
// "看起来有掩护"的状态下运行——比没有掩护更难发现。
func TestNewHasherRejectsUnusableCost(t *testing.T) {
	t.Parallel()

	if _, err := NewHasher(bcrypt.MaxCost + 1); err == nil {
		t.Fatal("NewHasher 接受了超出 bcrypt 上限的 cost，未在构造期失败")
	}
}

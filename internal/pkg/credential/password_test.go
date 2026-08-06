package credential

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// testCost 是测试用的最低代价。
//
// 用 MinCost 而不是生产的 12：每个 bcrypt 比对在 cost=12 下要几百毫秒，
// 一个表驱动测试会因此跑上十几秒。代价高低不改变本包的逻辑分支。
const testCost = bcrypt.MinCost

func TestHashAndCompare(t *testing.T) {
	t.Parallel()

	h := mustHasher(t, testCost)

	tests := []struct {
		name     string
		password string
		compare  string
		wantErr  error
	}{
		{name: "matching password", password: "correct horse battery", compare: "correct horse battery"},
		{name: "wrong password", password: "s3cret", compare: "s3cret ", wantErr: ErrMismatch},
		{name: "empty password matches itself", password: "", compare: ""},
		{name: "empty against non-empty", password: "x", compare: "", wantErr: ErrMismatch},
		{
			// 72 字节是 bcrypt 的上限，边界值本身必须可用。
			name:     "at the 72-byte boundary",
			password: strings.Repeat("a", 72),
			compare:  strings.Repeat("a", 72),
		},
		{
			// 只差最后一个字节：若实现退回到截断语义，这两个会互相通过。
			name:     "differs only past the boundary",
			password: strings.Repeat("a", 71) + "b",
			compare:  strings.Repeat("a", 71) + "c",
			wantErr:  ErrMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hashed, err := h.Hash(tt.password)
			if err != nil {
				t.Fatalf("Hash: %v", err)
			}

			err = h.Compare(hashed, tt.compare)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Compare = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestHashDoesNotContainThePlaintext 钉住哈希里没有明文。
//
// 听起来显然，但一个"先明文存着回头再改"的实现会让所有比对测试照样通过。
//
// **明文必须足够长且独特**：bcrypt 哈希是随机盐加 base64 字符集，短明文
// （尤其是单字符）会以相当高的概率被随机撞中，断言因此按盐的随机性
// 时灵时不灵——那是一个自造的 flaky 测试，比没有更糟。
func TestHashDoesNotContainThePlaintext(t *testing.T) {
	t.Parallel()

	const plaintext = "zq7-distinctive-plaintext-marker-9wx"

	h := mustHasher(t, testCost)
	hashed, err := h.Hash(plaintext)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if strings.Contains(hashed, plaintext) {
		t.Fatal("the hash contains the plaintext password")
	}
}

// TestHashIsSalted 钉住同一口令两次哈希不同。
//
// 没有盐时，相同口令产生相同哈希，泄漏的库里一眼就能看出哪些人用了同一个
// 口令——而那是彩虹表攻击的入口。
func TestHashIsSalted(t *testing.T) {
	t.Parallel()

	h := mustHasher(t, testCost)
	first, err := h.Hash("same password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	second, err := h.Hash("same password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	if first == second {
		t.Fatal("hashing the same password twice produced identical digests: the hash is unsalted")
	}
	// 两个哈希都必须能校验通过——盐是随机的，但比对要能自洽。
	for _, hashed := range []string{first, second} {
		if err := h.Compare(hashed, "same password"); err != nil {
			t.Fatalf("Compare against a valid hash: %v", err)
		}
	}
}

// TestTooLongPasswordIsRejectedOnHash 钉住超长口令在注册路径被拒。
//
// 拒绝而不是截断：截断会让两个前 72 字节相同的口令互相通过校验。
func TestTooLongPasswordIsRejectedOnHash(t *testing.T) {
	t.Parallel()

	h := mustHasher(t, testCost)
	_, err := h.Hash(strings.Repeat("a", 73))
	if !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("Hash of an over-long password = %v, want ErrPasswordTooLong", err)
	}
}

// TestTooLongPasswordLooksLikeAMismatchOnLogin 钉住登录路径不因长度而分叉。
//
// 超长输入若得到一个与众不同的响应，那本身就是一个信道——而它出现在
// 恰恰最需要"所有失败长得一样"的端点上。
func TestTooLongPasswordLooksLikeAMismatchOnLogin(t *testing.T) {
	t.Parallel()

	h := mustHasher(t, testCost)
	hashed, err := h.Hash("real password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	err = h.Compare(hashed, strings.Repeat("a", 200))
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("Compare with an over-long password = %v, want ErrMismatch "+
			"(the login response must not vary with password length)", err)
	}
}

// TestCorruptHashIsNotAMismatch 钉住坏数据与密码错误可区分。
//
// 把损坏的哈希当成"密码错误"，会让一整批坏掉的凭据表现为"所有人都记错了
// 密码"——排查方向被引向用户，而问题在数据。
func TestCorruptHashIsNotAMismatch(t *testing.T) {
	t.Parallel()

	h := mustHasher(t, testCost)

	for _, hashed := range []string{"", "not-a-hash", "$2a$", "$9z$10$" + strings.Repeat("x", 53)} {
		err := h.Compare(hashed, "whatever")
		if err == nil {
			t.Fatalf("Compare accepted a corrupt hash %q", hashed)
		}
		if errors.Is(err, ErrMismatch) {
			t.Errorf("corrupt hash %q reported as ErrMismatch: broken credential data would "+
				"look like every user forgot their password", hashed)
		}
	}
}

// TestDummyHashIsRealAndDoesTheWork 钉住时间掩护不是空壳。
//
// 这是本包最容易做假的一处：CompareWithDummy 只要返回 ErrMismatch 就能让
// 所有行为测试通过，而**完全不做任何计算**——时间信道原封不动。
// 因此这里直接验证那个常量是一个真实的、可被 bcrypt 解析的哈希。
func TestDummyHashIsRealAndDoesTheWork(t *testing.T) {
	t.Parallel()

	h := mustHasher(t, testCost)

	// bcrypt.Cost 只在输入是合法哈希时成功：它证明掩护用的不是随手编的字符串。
	if _, err := bcrypt.Cost([]byte(h.dummyHash)); err != nil {
		t.Fatalf("the dummy hash is not a valid bcrypt hash, so comparing against it "+
			"costs nothing and the timing cover is fake: %v", err)
	}
	// 代价是否与配置一致由 TestDummyHashCostMatchesConfiguredCost 断言——
	// 那才是掩护成立的条件，固定下限（如 >= DefaultCost）会在低代价配置下误报。
	if err := h.CompareWithDummy("anything"); !errors.Is(err, ErrMismatch) {
		t.Fatalf("CompareWithDummy = %v, want ErrMismatch (it must be indistinguishable "+
			"from a wrong password)", err)
	}
}

// mustHasher 构造 Hasher，构造失败即测试失败。
//
// NewHasher 会返回错误：它在构造期按配置代价生成时间掩护用的哈希，
// 生成不出来就不该产出一个静默失去掩护的 Hasher。
func mustHasher(t *testing.T, cost int) *Hasher {
	t.Helper()

	h, err := NewHasher(cost)
	if err != nil {
		t.Fatalf("NewHasher(%d): %v", cost, err)
	}
	return h
}

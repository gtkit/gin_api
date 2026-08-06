// Package credential 提供密码哈希与比对。
//
// 独立成包而不是塞进 application：哈希是纯计算，没有任何依赖，
// 放在这里让它可以被任意模块复用，也让它的测试不必搭起一个服务。
package credential

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// ErrMismatch 表示密码不匹配。
//
// 单独定义而不是直接透出 bcrypt 的错误：调用方要判断的是"密码对不对"，
// 不该被迫 import bcrypt 才能写这个判断。
var ErrMismatch = errors.New("credential: password does not match")

// ErrPasswordTooLong 表示口令超出 bcrypt 能处理的长度。
//
// 单独定义是为了让它可以被判成**用户输入错误**（422）而不是服务端故障（500）。
// bcrypt 只处理前 72 字节，当前版本对超长输入直接报错而不是截断——
// 那是对的：截断会让两个前 72 字节相同的口令互相通过校验，
// 而用户完全无从知道自己的后半段没有生效。
var ErrPasswordTooLong = errors.New("credential: password is too long")

// Hasher 按固定代价生成与校验密码哈希。
type Hasher struct {
	cost int
	// dummyHash 是账号不存在时用来消耗时间的哈希，按 cost 现场生成。
	//
	// 必须与 cost 同代价：bcrypt 比对的耗时由**哈希串自身携带的 cost**
	// 决定，与本结构体的 cost 字段无关。此前这里是一个硬编码的 cost 10
	// 常量，而默认配置是 12，于是"账号存在"与"账号不存在"的耗时差约 4 倍、
	// 稳定可远程测量——掩护只在配置恰好等于 10 时成立。
	//
	// 值本身不是秘密：口令是随机的，且永远不会有任何账号使用它。
	dummyHash string
}

// NewHasher 构造 Hasher。
//
// cost 由配置校验保证在合法区间，这里不再重复校验区间——但仍会把 bcrypt 自身
// 的拒绝如实返回：两处校验的边界万一漂移，静默接受比报错更糟。
//
// 构造时按 cost 生成一份用于时间掩护的哈希，生成失败即构造失败：一个无法提供
// 等价耗时掩护的 Hasher 不该被造出来，让它在启动期失败远好过在运行期静默
// 泄漏账号存在性。代价是启动时多一次 bcrypt 运算（cost 12 约 250ms），只付一次。
func NewHasher(cost int) (*Hasher, error) {
	h := &Hasher{cost: cost}

	// 口令内容无关紧要，只要不为空且不会被任何账号使用；随机性由 bcrypt 的 salt 提供。
	dummy, err := bcrypt.GenerateFromPassword([]byte("dummy password for timing cover"), cost)
	if err != nil {
		return nil, fmt.Errorf("credential: build timing-cover hash at cost %d: %w", cost, err)
	}
	h.dummyHash = string(dummy)

	return h, nil
}

// Hash 生成密码哈希。
//
// 超长口令返回 ErrPasswordTooLong（bcrypt 的长度上限是 72 字节），
// 让调用方能把它判成用户输入问题而不是服务端故障。
func (h *Hasher) Hash(password string) (string, error) {
	digest, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	switch {
	case err == nil:
		return string(digest), nil
	case errors.Is(err, bcrypt.ErrPasswordTooLong):
		return "", ErrPasswordTooLong
	default:
		return "", fmt.Errorf("credential: hash password: %w", err)
	}
}

// Compare 比对密码与哈希，不匹配时返回 ErrMismatch。
func (h *Hasher) Compare(hashed, password string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword),
		// 超长口令在**比对**路径上等价于密码错误：任何已存的哈希都不可能是它的
		// 哈希。回 ErrMismatch 而不是长度错误，登录响应因此不随口令长度而变——
		// 否则超长输入会得到一个与众不同的响应，那本身就是一个信道。
		errors.Is(err, bcrypt.ErrPasswordTooLong):
		return ErrMismatch
	default:
		// 哈希本身损坏（截断、被改写、算法不认识）不是"密码错误"：
		// 把它当成 ErrMismatch 会让一整批坏掉的凭据表现为"所有人都记错了密码"，
		// 排查方向被引向用户而不是数据。
		return fmt.Errorf("credential: compare password: %w", err)
	}
}

// CompareWithDummy 在账号不存在时执行一次等价的比对。
//
// **必须调用它，而不是直接返回错误**：bcrypt 的比对要几百毫秒，直接返回
// 只需几微秒。这个差值稳定、可测量，攻击者据此可以枚举出哪些手机号已注册——
// 一个不需要任何口令就能拿到的用户名单。
//
// 返回值恒为 ErrMismatch：调用方应当把它与"密码错误"作完全相同的处理，
// 包括响应内容与状态码。响应上区分两者，等于把时间信道上刚堵住的信息
// 直接写进报文。
func (h *Hasher) CompareWithDummy(password string) error {
	// 忽略返回值：这次比对的目的是消耗与真实比对相同的时间，结果不影响结论。
	_ = bcrypt.CompareHashAndPassword([]byte(h.dummyHash), []byte(password))
	return ErrMismatch
}

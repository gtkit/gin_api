package jwtauth

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

const (
	// maxPreviousKeys 是 previous 公钥数量的硬上限。
	//
	// 存在上限的理由是**无 kid 存量 token 的验签成本**：那类 token 只能按
	// active → previous 顺序逐个尝试，没有上限就是 O(N) 且 N 由配置决定。
	// 8 已经远超任何合理的轮换重叠窗口（每次轮换保留一个旧公钥的话，
	// 相当于同时容忍 8 代未过期的 token）。
	maxPreviousKeys = 8

	// maxClockSkew 是允许的时钟偏移上限。
	//
	// 固定上界而不是完全由配置决定：偏移窗口直接放宽 exp/nbf 判定，
	// 一个写成 24h 的配置等于把过期检查关掉，而它在配置里看起来
	// 只是"宽容一点"。
	maxClockSkew = 2 * time.Minute
)

// kidPattern 约束 kid 的字符集。
//
// 不允许路径分隔符是硬要求：kid 会参与构造密钥文件名，
// 允许 `/` 或 `..` 等于让配置能写到目录之外。
var kidPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidateKID 校验 kid 格式。
func ValidateKID(kid string) error {
	if !kidPattern.MatchString(kid) {
		return fmt.Errorf(
			"kid %q must match %s: it is used both as a JWT header value and as part of the key "+
				"file name, so path separators are not allowed", kid, kidPattern.String())
	}
	return nil
}

// SigningKey 是签发用的活跃密钥。
type SigningKey struct {
	KID     string
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
}

// VerificationKey 是验签用的公钥。
type VerificationKey struct {
	KID    string
	Public ed25519.PublicKey
}

// KeySet 是进程生命周期内**不可变**的密钥集合。
//
// 不可变是有意的：热重载需要原子替换整个 parser 与其缓存，否则会出现
// "缓存里的结果由一个已经被移除的密钥验出"这种无法解释的状态。
// 轮换通过重启加载新配置完成，新 parser 自然从空缓存开始。
type KeySet struct {
	active   SigningKey
	previous []VerificationKey
	// byKID 是 kid → 公钥的精确索引，覆盖 active 与全部 previous。
	byKID map[string]ed25519.PublicKey
	// ordered 是无 kid 存量 token 的尝试顺序：active 优先。
	ordered []VerificationKey
}

// NewKeySet 校验并构造密钥集。
//
// 校验项及其理由：
//   - kid 格式：见 ValidateKID；
//   - active 公私钥匹配：不匹配意味着签出的 token 谁也验不了，而这个错误
//     只有在第一个真实请求上才会暴露；
//   - kid 唯一：重复 kid 让"按 kid 精确选键"失去确定性——同一个 kid
//     对应两把不同的公钥时，命中哪一把取决于遍历顺序；
//   - previous 数量上限：见 maxPreviousKeys。
func NewKeySet(active SigningKey, previous []VerificationKey) (*KeySet, error) {
	var errs []error

	if err := ValidateKID(active.KID); err != nil {
		errs = append(errs, fmt.Errorf("active key: %w", err))
	}
	if len(active.Private) != ed25519.PrivateKeySize {
		errs = append(errs, errors.New("active key: private key is missing or has the wrong size"))
	}
	if len(active.Public) != ed25519.PublicKeySize {
		errs = append(errs, errors.New("active key: public key is missing or has the wrong size"))
	}

	// 公私钥匹配必须实测，不能只看长度：长度对但不配对的一组密钥
	// 会让签发成功、验签全败，而那在启动期完全看不出来。
	if len(active.Private) == ed25519.PrivateKeySize && len(active.Public) == ed25519.PublicKeySize {
		derived, ok := active.Private.Public().(ed25519.PublicKey)
		if !ok || !derived.Equal(active.Public) {
			errs = append(errs, errors.New(
				"active key: the configured public key does not match the private key; "+
					"every token this process issues would fail verification"))
		}
	}

	if len(previous) > maxPreviousKeys {
		errs = append(errs, fmt.Errorf(
			"at most %d previous keys are allowed, got %d: tokens without a kid are verified by "+
				"trying keys in order, so an unbounded list makes that cost unbounded too",
			maxPreviousKeys, len(previous)))
	}

	byKID := map[string]ed25519.PublicKey{}
	if active.KID != "" {
		byKID[active.KID] = active.Public
	}

	for i, prev := range previous {
		if err := ValidateKID(prev.KID); err != nil {
			errs = append(errs, fmt.Errorf("previous key %d: %w", i, err))
			continue
		}
		if len(prev.Public) != ed25519.PublicKeySize {
			errs = append(errs, fmt.Errorf("previous key %q: public key has the wrong size", prev.KID))
			continue
		}
		if _, dup := byKID[prev.KID]; dup {
			errs = append(errs, fmt.Errorf(
				"duplicate kid %q: kid must identify exactly one key, otherwise which key verifies "+
					"a token depends on iteration order", prev.KID))
			continue
		}
		byKID[prev.KID] = prev.Public
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	ordered := make([]VerificationKey, 0, len(previous)+1)
	ordered = append(ordered, VerificationKey{KID: active.KID, Public: active.Public})
	ordered = append(ordered, previous...)

	return &KeySet{active: active, previous: previous, byKID: byKID, ordered: ordered}, nil
}

// ActiveKID 返回签发用的 kid。
func (s *KeySet) ActiveKID() string { return s.active.KID }

// SigningKey 返回签发私钥。
func (s *KeySet) SigningKey() ed25519.PrivateKey { return s.active.Private }

// PublicKeyFor 按 kid 精确取公钥。
//
// **不做遍历兜底**：找不到就是找不到。回退遍历会让 kid 形同虚设——
// 攻击者只要把 header 里的 kid 改成任意值，验签就退回"逐个试"，
// 而那正是引入 kid 要消灭的行为。
func (s *KeySet) PublicKeyFor(kid string) (ed25519.PublicKey, bool) {
	key, ok := s.byKID[kid]
	return key, ok
}

// OrderedKeys 返回无 kid 存量 token 的尝试顺序（active 优先）。
func (s *KeySet) OrderedKeys() []VerificationKey { return s.ordered }

// LoadEd25519PrivateKey 从 PEM 文件读取私钥并校验文件形态与权限。
func LoadEd25519PrivateKey(path string, strictPerm bool) (ed25519.PrivateKey, error) {
	data, err := readKeyFile(path, strictPerm, false)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("private key %s is not valid PEM", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key %s: %w", path, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key %s is %T, want ed25519", path, parsed)
	}
	return key, nil
}

// LoadEd25519PublicKey 从 PEM 文件读取公钥。
func LoadEd25519PublicKey(path string) (ed25519.PublicKey, error) {
	data, err := readKeyFile(path, false, true)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("public key %s is not valid PEM", path)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key %s: %w", path, err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key %s is %T, want ed25519", path, parsed)
	}
	return key, nil
}

// readKeyFile 读取密钥文件并校验它确实是普通文件。
//
// strictPerm 为真时（生产环境的私钥）要求 group/other 无任何权限位。
// public为真时，Unix公钥还必须禁止group/other写入，允许0644等只读共享。
//
// 允许受控的 secret mount symlink：解析之后仍然检查目标类型与权限，
// 因此符号链接本身不构成绕过。禁止目录、设备文件等非普通文件——
// 那类路径要么读出垃圾，要么让进程挂在一个特殊文件上。
func readKeyFile(path string, strictPerm, public bool) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve key path %s: %w", path, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat key file %s: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("key path %s is not a regular file (mode %s)", resolved, info.Mode())
	}
	if strictPerm && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf(
			"private key %s is readable or writable by group/other (mode %04o); use 0600",
			resolved, info.Mode().Perm())
	}
	if public && runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("public key %s is writable by group/other (mode %04o); remove group/other write access", resolved, info.Mode().Perm())
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read key file %s: %w", resolved, err)
	}
	return data, nil
}

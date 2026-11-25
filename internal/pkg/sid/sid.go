package sid

import (
	"errors"
	"sync/atomic"

	"github.com/sony/sonyflake"
)

type Sid struct {
	sf *sonyflake.Sonyflake
}

const (
	base62     = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	CODE_LENTH = 31
)

var sid atomic.Pointer[Sid]

// NewSid 生成分布式ID
func NewSid() {
	if sid.Load() != nil {
		return
	}
	sf := sonyflake.NewSonyflake(sonyflake.Settings{})
	if sf == nil {
		panic("sonyflake not created")
	}
	sid.CompareAndSwap(nil, &Sid{sf})
}
func Client() *Sid {
	return sid.Load()
}
func (s Sid) GenString() (string, error) {
	// 生成分布式ID
	id, err := s.GenUint64()
	if err != nil {
		return "", err
	}
	// 将ID转换为字符串
	return encodeID(id), nil
}
func (s Sid) GenUint64() (uint64, error) {
	// 生成分布式ID
	if s.sf == nil {
		return 0, errors.New("sid not initialized")
	}
	return s.sf.NextID()
}

func IntToBase62(n int) string {
	if n < 0 {
		return ""
	}
	return encodeID(uint64(n))
}

// encodeID retains the original quotient (31) and alphabet. Changing either
// would change existing string identifiers. A uint64 needs at most 13 digits.
func encodeID(n uint64) string {
	var buf [13]byte
	i := len(buf)
	for {
		i--
		buf[i] = base62[n%uint64(len(base62))]
		n /= CODE_LENTH
		if n == 0 {
			return string(buf[i:])
		}
	}
}

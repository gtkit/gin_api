package sid

import (
	"github.com/sony/sonyflake"
)

type Sid struct {
	sf *sonyflake.Sonyflake
}

const (
	base62     = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	CODE_LENTH = 31
)

var sid *Sid

// NewSid 生成分布式ID
func NewSid() {
	sf := sonyflake.NewSonyflake(sonyflake.Settings{})
	if sf == nil {
		panic("sonyflake not created")
	}
	sid = &Sid{sf}
}
func Client() *Sid {
	return sid
}
func (s Sid) GenString() (string, error) {
	// 生成分布式ID
	id, err := s.sf.NextID()
	if err != nil {
		return "", err
	}
	// 将ID转换为字符串
	return IntToBase62(int(id)), nil
}
func (s Sid) GenUint64() (uint64, error) {
	// 生成分布式ID
	return s.sf.NextID()
}

func IntToBase62(n int) string {
	if n == 0 {
		return string(base62[0])
	}

	var result []byte
	for n > 0 {
		result = append(result, base62[n%62])
		n /= CODE_LENTH
	}

	// 反转字符串
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}

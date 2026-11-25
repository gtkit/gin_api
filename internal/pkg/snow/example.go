// @Author xiaozhaofu 2023/7/13 18:42:00
package snow

import (
	"strconv"

	"github.com/gtkit/goerr"
)

// 采用雪花算法
func tradeno() (string, error) {
	s := NewWorkers(1, 2)
	snowid, err := s.SonwID()
	if err != nil {
		return "", goerr.Wrap(err, "获取雪花id:")
	}
	return strconv.Itoa(int(snowid)), nil
}

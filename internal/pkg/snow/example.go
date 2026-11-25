// @Author xiaozhaofu 2023/7/13 18:42:00
package snow

import (
	"strconv"

	"github.com/gtkit/goerr"
)

var tradeWorker = NewWorkers(1, 2)

// 采用雪花算法
func tradeno() (string, error) {
	snowid, err := tradeWorker.SonwID()
	if err != nil {
		return "", goerr.Wrap(err, "获取雪花id:")
	}
	return strconv.FormatUint(snowid, 10), nil
}

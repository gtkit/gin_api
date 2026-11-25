package sign

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// map 参数获取 sign
func MapSign(op map[string]any, appSecret string) string {

	var buf bytes.Buffer

	buf.WriteString(appSecret)

	buf.WriteString(sortMap(op, 0))

	buf.WriteString(appSecret)

	returnStr := buf.String()
	fmt.Println("------------return str : ", returnStr)

	s := NewMd5(returnStr)

	return strings.ToUpper(s)
}

func sortMap(op map[string]any, t int8) string {
	keys := make([]string, 0, len(op))

	for k, _ := range op {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer

	// 按照排序后的key遍历map
	for _, k := range keys {
		if op[k] == "" {
			continue
		}
		buf.WriteString(k)
		if t == 1 {
			buf.WriteString("=")
		}
		switch vv := op[k].(type) {
		case string:
			buf.WriteString(vv)
		case int:
			buf.WriteString(strconv.FormatInt(int64(vv), 10))
		case int8:
		case int16:
		case int32:
		case int64:
			buf.WriteString(strconv.FormatInt(vv, 10))
		case bool:
			buf.WriteString(strconv.FormatBool(vv))
		case map[string]any:
			// vvs := sortMap(vv, 1)
			// // fmt.Println("------string(jstr)", strings.TrimRight(vvs, ","))
			// buf.WriteString("{")
			// buf.WriteString(strings.TrimRight(vvs, ", "))
			// buf.WriteString("}")
			jstr, _ := json.Marshal(vv)
			// fmt.Println("------string(jstr)", string(jstr))
			buf.WriteString(string(jstr))

		default:
			continue
		}
		if t == 1 {
			buf.WriteString(", ")
		}
	}

	return buf.String()
}

func NewMd5(str string) string {
	md5ctx := md5.New()
	md5ctx.Write([]byte(str))

	return hex.EncodeToString(md5ctx.Sum(nil))
}

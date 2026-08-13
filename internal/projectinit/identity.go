//go:build scaffold

package projectinit

import (
	"regexp"
	"strings"
)

// setMakeVar 把 Make KV 文件中某个变量的值替换为 value。
//
// 只改赋值行的右侧，保留文件里的注释与其余内容——project.mk 的注释解释了取值约束与
// 「什么不属于身份」，那些说明对派生项目同样有价值，不能被整文件重写抹掉。
//
// 变量不存在时追加一行，保证结果一定含该赋值。
func setMakeVar(content, key, value string) string {
	re := regexp.MustCompile(`(?m)^([[:space:]]*` + regexp.QuoteMeta(key) + `[[:space:]]*=[[:space:]]*).*$`)
	if re.MatchString(content) {
		return re.ReplaceAllString(content, "${1}"+value)
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + key + " = " + value + "\n"
}

// displayNameFrom 由项目名派生展示名：下划线转空格，各单词首字母大写。
//
// 例：my_service -> My Service。
//
// 结果一定落在 APP_DISPLAY_NAME 的字符集 ^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$ 内——
// 输入已通过 projectNamePattern（只含小写字母、数字、下划线，首字符为字母，≤63 字符），
// 而本函数只做「下划线→空格」与大小写变换，既不引入新字符也不改变长度。
func displayNameFrom(name string) string {
	words := strings.Split(name, "_")
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w == "" {
			continue // 连续下划线不产生空词，避免出现双空格。
		}
		out = append(out, strings.ToUpper(w[:1])+w[1:])
	}
	return strings.Join(out, " ")
}

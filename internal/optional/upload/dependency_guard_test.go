package upload

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// loggerV1Require 匹配 go.mod 里 logger **v1** 主版本的 require 行。
//
// 两个版本的模块路径只差一个 `/v2` 后缀：
//
//	github.com/gtkit/logger v1.4.6        ← 要拦的
//	github.com/gtkit/logger/v2 v2.3.0     ← 本仓在用的
//
// 因此必须锚定 `logger` 之后紧跟空白，否则前缀匹配会把 v2 一起判为违规。
var loggerV1Require = regexp.MustCompile(`(?m)^\s*github\.com/gtkit/logger\s+v1\.`)

// TestLoggerV1RequirePatternDiscriminates 是上面那条正则自身的判据。
//
// 没有它，正则写错（比如漏掉 \s+ 锚点、或误写成永不匹配）时，依赖卫兵会
// 静默地永远通过——那比没有卫兵更糟，因为它看起来还在守着。
func TestLoggerV1RequirePatternDiscriminates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want bool
	}{
		{"v1 直接依赖", "\tgithub.com/gtkit/logger v1.4.6\n", true},
		{"v1 间接依赖", "\tgithub.com/gtkit/logger v1.4.6 // indirect\n", true},
		{"v1 其他小版本", "\tgithub.com/gtkit/logger v1.8.4\n", true},
		{"v2 不算违规", "\tgithub.com/gtkit/logger/v2 v2.3.0\n", false},
		{"v2 间接也不算", "\tgithub.com/gtkit/logger/v2 v2.3.0 // indirect\n", false},
		{"同前缀的其他包不算", "\tgithub.com/gtkit/logrotate v1.1.3 // indirect\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := loggerV1Require.MatchString(tt.line); got != tt.want {
				t.Fatalf("MatchString(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestGoModExcludesLoggerV1 守护「本仓只有一套日志栈」。
//
// 这条约束必须由门禁而非约定保证：logger v1 从来不是本仓 import 的，它是
// gouploader v1.7.0 经 internal/logx 带进来的传递依赖，所以任何基于搜索
// import 语句的检查都看不见它。它的代价也不是编译失败而是静默跑偏——v1 的
// 包级函数有 zap.NewDevelopment() 兜底，日志会以 Debug 全开写进 stderr，
// 绕开本仓配置的级别、轮转与字段规范。
func TestGoModExcludesLoggerV1(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	if match := loggerV1Require.FindString(string(content)); match != "" {
		t.Fatalf("go.mod 仍含 logger v1 依赖（%q）；"+
			"本仓日志栈唯一为 github.com/gtkit/logger/v2，"+
			"v1 只会经 gouploader 等传递依赖回归——请升级引入方而不是放行此项",
			match)
	}
}

// moduleRoot 从测试工作目录向上定位含 go.mod 的目录。
//
// 不用 `git rev-parse`：那样在无 git 的环境里只能 t.Skip，而一条会 skip 的
// 依赖卫兵等于静默放行。go.mod 的存在是模块能被测试的前提，因此这条路径
// 在任何能跑测试的环境里都成立。
func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("从 %s 向上未找到 go.mod", dir)
		}
		dir = parent
	}
}

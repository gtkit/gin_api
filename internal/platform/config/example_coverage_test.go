package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/knadh/koanf/parsers/yaml"
)

// shippedExamplePath is the schema coverage fixture, resolved from this package.
const shippedExamplePath = "../../../config/env.yml.example"

// exampleCoverageExemptions 是「结构体有、样例刻意不列」的豁免清单。
// 每项必须给理由；测试同时钉住豁免本身不腐化：
// 路径必须仍存在于结构体（防钉死无效豁免），且不得出现在样例里
// （样例补了键就必须删掉豁免）。
var exampleCoverageExemptions = map[string]string{
	"application.readtimeout":   "废弃键：非零即启动失败并给迁移文案，样例不得引导使用",
	"application.writertimeout": "废弃键：同上",
	"jwt.private_key_path":      "废弃键：单密钥形态已被 kid 密钥集取代，保留只为可读的迁移错误",
	"jwt.public_key_path":       "废弃键：同上",
	"modules.user":              "墓碑字段：v0.4.0 移除的 user 模块，保留只为给残留配置一条可读的迁移错误",
}

// TestExampleCoversConfigStruct 是「结构体 → 样例」方向的对齐门禁。
//
// 反方向（样例 → 结构体）由严格解码覆盖：样例里多出的键会让加载失败。
// 但反过来——结构体加了字段而样例没跟上——没有任何机制能发现：
// 字段静默取零值/内置默认，运维不知道这个旋钮存在，也不知道它的
// 影响面（gin-vue-admin 的 Media 段漂移让大文件上传实际无上限，
// 正是这一类事故）。本测试让这类漂移在 make check 就红。
func TestExampleCoversConfigStruct(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Clean(shippedExamplePath))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	parsed, err := yaml.Parser().Unmarshal(raw)
	if err != nil {
		t.Fatalf("parse example: %v", err)
	}
	examplePaths := map[string]bool{}
	collectYAMLPaths(parsed, "", examplePaths)

	structPaths := map[string]bool{}
	collectStructPaths(reflect.TypeFor[Config](), "", structPaths)

	var missing []string
	for path := range structPaths {
		if examplePaths[path] {
			continue
		}
		if _, exempt := exampleCoverageExemptions[path]; exempt {
			continue
		}
		missing = append(missing, path)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("config struct keys missing from %s (add them with comments, or add an exemption with a reason):\n  %s",
			shippedExamplePath, strings.Join(missing, "\n  "))
	}

	// 豁免清单自检：防止豁免比现实活得久。
	for path, reason := range exampleCoverageExemptions {
		if !structPaths[path] {
			t.Errorf("exemption %q (%s) no longer matches any struct key; delete it", path, reason)
		}
		if examplePaths[path] {
			t.Errorf("exemption %q (%s) is now present in the example; delete the exemption", path, reason)
		}
	}
}

// collectStructPaths 收集结构体的全部 mapstructure 键路径。
// map / slice 字段记录到字段本身为止（其下是动态键，样例给出示例或注释即可）；
// 嵌套 struct 递归；不含任何 mapstructure 字段的 struct（如 time.Time）当叶子。
func collectStructPaths(t reflect.Type, prefix string, out map[string]bool) {
	for field := range t.Fields() {
		tag, _, _ := strings.Cut(field.Tag.Get("mapstructure"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		ft := field.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && hasMapstructureFields(ft) {
			collectStructPaths(ft, path, out)
			continue
		}
		out[path] = true
	}
}

func hasMapstructureFields(t reflect.Type) bool {
	for field := range t.Fields() {
		if tag, _, _ := strings.Cut(field.Tag.Get("mapstructure"), ","); tag != "" && tag != "-" {
			return true
		}
	}
	return false
}

// collectYAMLPaths 收集样例里出现的全部键路径（含中间节点）。
func collectYAMLPaths(node any, prefix string, out map[string]bool) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	for key, value := range m {
		path := fmt.Sprintf("%v", key)
		if prefix != "" {
			path = prefix + "." + path
		}
		out[path] = true
		collectYAMLPaths(value, path, out)
	}
}

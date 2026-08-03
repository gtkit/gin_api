//go:build ignore

// openapi-diff 比较两份 OpenAPI 文档并报告破坏性变更。
//
// 用 Go 而不是 jq：`make check` 的契约是「不依赖网络、Docker 或额外工具」。
// 依赖 jq 会让门禁在没装它的机器上直接失败，而那类失败最终的处理方式
// 通常是把整条门禁跳过。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// doc 只解出比较需要的字段。
//
// 用宽松结构而不是完整 schema：这个工具的职责是**比较**，
// 完整建模会让它随文档结构演进而反复失效。
type doc struct {
	Paths map[string]map[string]operation `json:"paths"`
}

type operation struct {
	OperationID string              `json:"operationId"`
	Security    []map[string][]any  `json:"security"`
	Responses   map[string]struct{} `json:"responses"`
}

// finding 是一条破坏性变更。
type finding struct {
	Operation string // "METHOD /path"
	Rule      string
	Detail    string
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: openapi-diff <baseline.json> <current.json>")
		os.Exit(2)
	}

	baseline, err := load(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "load baseline: %v\n", err)
		os.Exit(2)
	}
	current, err := load(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "load current: %v\n", err)
		os.Exit(2)
	}

	findings := compare(baseline, current)
	if len(findings) == 0 {
		return
	}

	for _, f := range findings {
		fmt.Printf("%s|%s|%s\n", f.Operation, f.Rule, f.Detail)
	}
	os.Exit(1)
}

func load(path string) (*doc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d doc
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &d, nil
}

// compare 按**生产者/消费者方向**判断破坏性。
//
// 方向是关键：请求侧收窄让原本合法的客户端请求被拒；响应侧放宽或保证减弱
// 让原本能解析的客户端解析失败。两者都是破坏，但触发的判据不同——
// 写成一条"变严了就报警"会同时产生漏报与误报。
func compare(baseline, current *doc) []finding {
	var findings []finding

	for _, key := range sortedOperationKeys(baseline) {
		method, path := splitKey(key)

		curOps, pathExists := current.Paths[path]
		curOp, opExists := curOps[method]
		baseOp := baseline.Paths[path][method]

		// 1) 删除 endpoint / method：客户端调用直接 404 或 405。
		if !pathExists || !opExists {
			findings = append(findings, finding{
				Operation: key, Rule: "endpoint_removed",
				Detail: "the endpoint or method no longer exists",
			})
			continue
		}

		// 2) operationId 变更：它命名生成客户端里的方法，改名即客户端编译失败。
		if baseOp.OperationID != curOp.OperationID {
			findings = append(findings, finding{
				Operation: key, Rule: "operation_id_changed",
				Detail: fmt.Sprintf("%s -> %s", baseOp.OperationID, curOp.OperationID),
			})
		}

		// 3) security 变严：原本不带凭据就能调，现在会收到 401。
		if len(baseOp.Security) == 0 && len(curOp.Security) > 0 && !allowsAnonymous(curOp.Security) {
			findings = append(findings, finding{
				Operation: key, Rule: "security_tightened",
				Detail: "public -> credentials required",
			})
		}

		// 4) 删除既有响应状态码：客户端的分支判断会落空。
		for _, status := range sortedKeys(baseOp.Responses) {
			if _, ok := curOp.Responses[status]; !ok {
				findings = append(findings, finding{
					Operation: key, Rule: "response_status_removed",
					Detail: "status " + status + " is gone",
				})
			}
		}
	}

	return findings
}

// allowsAnonymous 报告 security 列表里是否含"无需凭据"的选项。
//
// OpenAPI 用一个空对象表示"这一项不要求任何凭据"，因此
// `[{}, {AppToken: []}]` 的语义是"可选认证"——那不是收紧。
func allowsAnonymous(security []map[string][]any) bool {
	for _, entry := range security {
		if len(entry) == 0 {
			return true
		}
	}
	return false
}

func sortedOperationKeys(d *doc) []string {
	var keys []string
	for path, ops := range d.Paths {
		for method := range ops {
			keys = append(keys, strings.ToUpper(method)+" "+path)
		}
	}
	slices.Sort(keys)
	return keys
}

func splitKey(key string) (method, path string) {
	parts := strings.SplitN(key, " ", 2)
	return strings.ToLower(parts[0]), parts[1]
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

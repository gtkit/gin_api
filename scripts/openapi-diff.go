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
	Paths      map[string]map[string]operation `json:"paths"`
	Components struct {
		Schemas map[string]*schema `json:"schemas"`
		// Responses 是可复用的响应定义。
		//
		// 必须支持：手写契约常把公共响应抽成 `#/components/responses/...`，
		// 解不出来的话它的 body 会变成"未知"，而未知与任何具体形状比较
		// 都会被判成变更——一整片误报，且掩盖真实的破坏。
		Responses map[string]response `json:"responses"`
	} `json:"components"`
}

// response 是一个响应定义，可能本身就是一个引用。
type response struct {
	Ref     string               `json:"$ref"`
	Content map[string]mediaType `json:"content"`
}

type operation struct {
	OperationID string              `json:"operationId"`
	Security    []map[string][]any  `json:"security"`
	Parameters  []parameter         `json:"parameters"`
	Responses   map[string]response `json:"responses"`
	RequestBody *struct {
		Content map[string]mediaType `json:"content"`
	} `json:"requestBody"`
}

// parameter 是路径 / 查询 / header 参数。
type parameter struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required"`
}

type mediaType struct {
	Schema *schema `json:"schema"`
}

// schema 只解出比较字段形状需要的部分。
type schema struct {
	Ref        string             `json:"$ref"`
	Type       any                `json:"type"`
	Format     string             `json:"format"`
	Properties map[string]*schema `json:"properties"`
	Required   []string           `json:"required"`
	Items      *schema            `json:"items"`
	Enum       []any              `json:"enum"`
	Minimum    *float64           `json:"minimum"`
	Maximum    *float64           `json:"maximum"`
	MinLength  *int               `json:"minLength"`
	MaxLength  *int               `json:"maxLength"`
	Pattern    string             `json:"pattern"`
	// AllOf 是组合式 schema。
	//
	// 必须展开：手写契约常用 allOf 把公共信封与具体 data 拼起来，
	// 不展开的话整个 body 解不出任何字段，与生成物比较时会整片误报。
	AllOf []*schema `json:"allOf"`
	// AdditionalProperties 可以是 bool 或一个 schema。
	AdditionalProperties any `json:"additionalProperties"`
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

		// 5) 参数、媒体类型与新增成功状态码。
		findings = append(findings, compareOperationShape(key, baseline, current, baseOp, curOp)...)

		// 6) body 的字段形状与约束。
		findings = append(findings, compareBodies(key, baseline, current, baseOp, curOp)...)
	}

	return findings
}

// field 是摊平后的一个字段及其全部可比较约束。
type field struct {
	// kind 是该字段可取的 JSON 类型集合，已排序去重。
	kind     []string
	required bool
	format   string
	// enum 是取值集合的字符串表示，已排序；nil 表示未约束。
	enum      []string
	minimum   *float64
	maximum   *float64
	minLength *int
	maxLength *int
	pattern   string
	// openMap 表示该 object 是否接受声明之外的属性；nil 表示未声明。
	openMap *bool
}

// compareBodies 比较请求体与响应体的字段形状。
//
// 两侧的判据**相反**，因为数据流向相反：
//
//	请求体由客户端产出——新增必填字段让老客户端的请求被拒。
//	响应体由服务端产出——删除字段或把保证从必填放宽，让老客户端解析落空。
//
// 写成一条"字段变了就报警"会同时误报（请求体新增可选字段完全安全）
// 与漏报（响应体去掉 required 是实打实的破坏）。
func compareBodies(key string, baseline, current *doc, baseOp, curOp operation) []finding {
	var findings []finding

	baseReq := flattenBody(baseline, requestSchema(baseOp))
	curReq := flattenBody(current, requestSchema(curOp))
	for _, name := range sortedKeys(curReq) {
		cur := curReq[name]
		base, existed := baseReq[name]
		switch {
		case cur.required && (!existed || !base.required):
			findings = append(findings, finding{
				Operation: key, Rule: "request_field_became_required",
				Detail: "field " + name + " is now required, so existing clients get rejected",
			})
		case existed && narrows(base.kind, cur.kind):
			// 只报**收窄**。放宽（例如原本只收 integer，现在也收 null）
			// 让老客户端原样继续工作，报出来就是误报——而一条会误报的门禁
			// 在第一次挡路时就会被关掉。
			findings = append(findings, finding{
				Operation: key, Rule: "request_field_type_narrowed",
				Detail: fmt.Sprintf("field %s: %s -> %s", name, base.kind, cur.kind),
			})
		case existed:
			findings = append(findings, constraintFindings(key, "request", name, base, cur)...)
		}
	}

	baseResp := flattenBody(baseline, responseSchema(baseline, baseOp))
	curResp := flattenBody(current, responseSchema(current, curOp))
	for _, name := range sortedKeys(baseResp) {
		base := baseResp[name]
		cur, exists := curResp[name]
		switch {
		case !exists:
			findings = append(findings, finding{
				Operation: key, Rule: "response_field_removed",
				Detail: "field " + name + " is gone, so clients reading it get nothing",
			})
		case base.required && !cur.required:
			findings = append(findings, finding{
				Operation: key, Rule: "response_field_no_longer_guaranteed",
				Detail: "field " + name + " is no longer required",
			})
		case narrows(cur.kind, base.kind):
			// 响应侧方向相反：现在**可能返回**老客户端不认识的类型才是破坏。
			// 收窄（原本可能返回 null，现在保证不返回）让老客户端原样工作。
			findings = append(findings, finding{
				Operation: key, Rule: "response_field_type_widened",
				Detail: fmt.Sprintf("field %s: %s -> %s", name, base.kind, cur.kind),
			})
		default:
			findings = append(findings, constraintFindings(key, "response", name, base, cur)...)
		}
	}

	return findings
}

// constraintFindings 比较类型之外的约束，方向由 side 决定。
//
// 请求侧的破坏是**收紧**（原本能发的现在发不了）；响应侧的破坏是**放宽**
// （现在可能返回老客户端处理不了的值）。同一条约束在两侧的判据因此相反。
func constraintFindings(key, side, name string, base, cur field) []finding {
	var findings []finding
	report := func(rule, detail string) {
		findings = append(findings, finding{
			Operation: key, Rule: side + "_field_" + rule,
			Detail: "field " + name + ": " + detail,
		})
	}
	request := side == "request"

	// format 双向都是破坏：它决定生成客户端里的字段类型（int32 vs int64），
	// 改动在两个方向上都让客户端编译失败或精度丢失。
	// 从无到有不算——那是补全精度信息，不改变已有值的合法性。
	if base.format != "" && cur.format != "" && base.format != cur.format {
		report("format_changed", base.format+" -> "+cur.format)
	}

	// enum：请求侧删值是收紧，响应侧加值是放宽。
	if base.enum != nil && cur.enum != nil {
		if request {
			if removed := missingFrom(base.enum, cur.enum); len(removed) > 0 {
				report("enum_narrowed", "no longer accepts "+strings.Join(removed, ", "))
			}
		} else if added := missingFrom(cur.enum, base.enum); len(added) > 0 {
			report("enum_widened", "may now return "+strings.Join(added, ", "))
		}
	}

	// 数值范围：请求侧下界上移 / 上界下移是收紧，响应侧反之。
	if request {
		if tightened(base.minimum, cur.minimum, true) {
			report("minimum_raised", describeFloat(base.minimum)+" -> "+describeFloat(cur.minimum))
		}
		if tightened(base.maximum, cur.maximum, false) {
			report("maximum_lowered", describeFloat(base.maximum)+" -> "+describeFloat(cur.maximum))
		}
	} else {
		if tightened(cur.minimum, base.minimum, true) {
			report("minimum_lowered", describeFloat(base.minimum)+" -> "+describeFloat(cur.minimum))
		}
		if tightened(cur.maximum, base.maximum, false) {
			report("maximum_raised", describeFloat(base.maximum)+" -> "+describeFloat(cur.maximum))
		}
	}

	// 长度范围同理。
	baseMin, curMin := intAsFloat(base.minLength), intAsFloat(cur.minLength)
	baseMax, curMax := intAsFloat(base.maxLength), intAsFloat(cur.maxLength)
	if request {
		if tightened(baseMin, curMin, true) {
			report("min_length_raised", describeFloat(baseMin)+" -> "+describeFloat(curMin))
		}
		if tightened(baseMax, curMax, false) {
			report("max_length_lowered", describeFloat(baseMax)+" -> "+describeFloat(curMax))
		}
	} else {
		if tightened(curMin, baseMin, true) {
			report("min_length_lowered", describeFloat(baseMin)+" -> "+describeFloat(curMin))
		}
		if tightened(curMax, baseMax, false) {
			report("max_length_raised", describeFloat(baseMax)+" -> "+describeFloat(curMax))
		}
	}

	// pattern 无法机械判断宽窄，因此任何变化都 fail-closed 报出来，
	// 由人判断并逐条 waiver。猜错的方向是放过一次真实破坏。
	if base.pattern != cur.pattern && base.pattern != "" {
		report("pattern_changed", base.pattern+" -> "+cur.pattern)
	}

	// additionalProperties：请求侧从开放变封闭，老客户端多发的字段会被拒。
	if request && base.openMap != nil && cur.openMap != nil && *base.openMap && !*cur.openMap {
		report("additional_properties_closed", "extra properties are no longer accepted")
	}

	return findings
}

// missingFrom 返回 want 中不在 have 里的元素。
func missingFrom(want, have []string) []string {
	var missing []string
	for _, v := range want {
		if !slices.Contains(have, v) {
			missing = append(missing, v)
		}
	}
	return missing
}

// tightened 报告边界是否收紧。
//
// lower 为 true 时比较下界（变大即收紧），否则比较上界（变小即收紧）。
// 从"无界"变成"有界"同样是收紧：原本任意值都合法，现在有一部分不合法了。
func tightened(base, cur *float64, lower bool) bool {
	if cur == nil {
		return false
	}
	if base == nil {
		return true
	}
	if lower {
		return *cur > *base
	}
	return *cur < *base
}

func intAsFloat(v *int) *float64 {
	if v == nil {
		return nil
	}
	f := float64(*v)
	return &f
}

func describeFloat(v *float64) string {
	if v == nil {
		return "unbounded"
	}
	return fmt.Sprintf("%g", *v)
}

// compareOperationShape 比较参数、媒体类型与新增成功状态码。
func compareOperationShape(key string, baseline, current *doc, baseOp, curOp operation) []finding {
	var findings []finding

	// 参数删除、重命名或从可选变必填。
	curParams := map[string]parameter{}
	for _, p := range curOp.Parameters {
		curParams[p.In+":"+p.Name] = p
	}
	for _, base := range baseOp.Parameters {
		cur, ok := curParams[base.In+":"+base.Name]
		switch {
		case !ok:
			findings = append(findings, finding{
				Operation: key, Rule: "parameter_removed",
				Detail: fmt.Sprintf("%s parameter %s is gone", base.In, base.Name),
			})
		case !base.Required && cur.Required:
			findings = append(findings, finding{
				Operation: key, Rule: "parameter_became_required",
				Detail: fmt.Sprintf("%s parameter %s is now required", base.In, base.Name),
			})
		}
	}
	// 新增必填参数同样让老客户端的请求被拒。
	baseParams := map[string]bool{}
	for _, p := range baseOp.Parameters {
		baseParams[p.In+":"+p.Name] = true
	}
	for _, cur := range curOp.Parameters {
		if cur.Required && !baseParams[cur.In+":"+cur.Name] {
			findings = append(findings, finding{
				Operation: key, Rule: "parameter_added_required",
				Detail: fmt.Sprintf("%s parameter %s is newly required", cur.In, cur.Name),
			})
		}
	}

	// 删除既有媒体类型：老客户端发的 Content-Type 不再被接受。
	if baseOp.RequestBody != nil && curOp.RequestBody != nil {
		for _, media := range sortedKeys(baseOp.RequestBody.Content) {
			if _, ok := curOp.RequestBody.Content[media]; !ok {
				findings = append(findings, finding{
					Operation: key, Rule: "request_media_type_removed",
					Detail: media + " is no longer accepted",
				})
			}
		}
	}
	for _, status := range sortedKeys(baseOp.Responses) {
		rawCur, ok := curOp.Responses[status]
		if !ok {
			continue // 已由 response_status_removed 覆盖
		}
		curResp := resolveResponse(current, rawCur, 0)
		for _, media := range sortedKeys(resolveResponse(baseline, baseOp.Responses[status], 0).Content) {
			if _, ok := curResp.Content[media]; !ok {
				findings = append(findings, finding{
					Operation: key, Rule: "response_media_type_removed",
					Detail: fmt.Sprintf("status %s no longer returns %s", status, media),
				})
			}
		}
	}

	// 新增**成功**状态码：客户端的成功分支只认旧的那个，多出来的会走进错误分支。
	// 新增错误码不报——客户端本就需要对未知错误码有兜底。
	for _, status := range sortedKeys(curOp.Responses) {
		if _, ok := baseOp.Responses[status]; ok {
			continue
		}
		if strings.HasPrefix(status, "2") {
			findings = append(findings, finding{
				Operation: key, Rule: "success_status_added",
				Detail: "status " + status + " is new, so existing success branches miss it",
			})
		}
	}

	return findings
}

func requestSchema(op operation) *schema {
	if op.RequestBody == nil {
		return nil
	}
	return op.RequestBody.Content[jsonMedia].Schema
}

func responseSchema(d *doc, op operation) *schema {
	return resolveResponse(d, op.Responses["200"], 0).Content[jsonMedia].Schema
}

// resolveResponse 解引用到实际的响应定义。
func resolveResponse(d *doc, r response, depth int) response {
	if r.Ref == "" || depth > maxFlattenDepth {
		return r
	}
	return resolveResponse(d, d.Components.Responses[strings.TrimPrefix(r.Ref, "#/components/responses/")], depth+1)
}

const jsonMedia = "application/json"

// rootFieldName 是 body 根节点在摊平结果里的键。
//
// 根节点必须自成一条：`additionalProperties` 之类的约束挂在对象本身而不是
// 它的某个属性上，只收集子字段的话，"从接受额外属性变成拒绝"完全不可见。
const rootFieldName = "(root)"

// flattenBody 把 schema 摊平成 "a.b.c" -> field。
func flattenBody(d *doc, s *schema) map[string]field {
	out := map[string]field{}
	if s != nil {
		out[rootFieldName] = fieldOf(d, s, true)
	}
	flatten(d, s, "", out, map[string]bool{}, 0)
	return out
}

// maxFlattenDepth 是摊平的深度上限。
//
// 自引用靠 seen 剪枝，但**互相引用**的一组类型（A→B→A）在不同路径下每次
// 都是"没见过的组合"，只有深度上限能保证终止。
const maxFlattenDepth = 12

func flatten(d *doc, s *schema, prefix string, out map[string]field, seen map[string]bool, depth int) {
	if s == nil || depth > maxFlattenDepth {
		return
	}

	// allOf 的各分支都贡献字段，逐个展开到同一层。
	for _, branch := range s.AllOf {
		flatten(d, branch, prefix, out, seen, depth+1)
	}

	if s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
		if seen[name] {
			return
		}
		seen[name] = true
		defer delete(seen, name)
		flatten(d, d.Components.Schemas[name], prefix, out, seen, depth+1)
		return
	}

	if s.Items != nil {
		flatten(d, s.Items, prefix+"[]", out, seen, depth+1)
		return
	}

	required := map[string]bool{}
	for _, name := range s.Required {
		required[name] = true
	}

	for _, name := range sortedKeys(s.Properties) {
		child := s.Properties[name]
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		out[path] = fieldOf(d, child, required[name])
		flatten(d, child, path, out, seen, depth+1)
	}
}

// fieldOf 收集一个字段的全部可比较约束。
//
// 约束一起收集而不是只看类型：一个把 `maxLength` 从 200 收到 50 的改动
// 不改变类型，却会让原本合法的请求被拒——只比类型的话它完全不可见。
func fieldOf(d *doc, s *schema, required bool) field {
	resolved := resolve(d, s, 0)
	f := field{kind: kindOf(d, s, 0), required: required}
	if resolved == nil {
		return f
	}

	// allOf 的约束取各分支的并集：组合出来的形状同时受每个分支约束。
	for _, branch := range resolved.AllOf {
		merged := fieldOf(d, branch, required)
		f = mergeField(f, merged)
	}

	f.format = resolved.Format
	f.minimum, f.maximum = resolved.Minimum, resolved.Maximum
	f.minLength, f.maxLength = resolved.MinLength, resolved.MaxLength
	f.pattern = resolved.Pattern

	if len(resolved.Enum) > 0 {
		f.enum = make([]string, 0, len(resolved.Enum))
		for _, v := range resolved.Enum {
			encoded, err := json.Marshal(v)
			if err != nil {
				continue
			}
			f.enum = append(f.enum, string(encoded))
		}
		slices.Sort(f.enum)
	}

	// additionalProperties 只有显式的 false 才是"封闭"；schema 值与 true 都是开放。
	if resolved.AdditionalProperties != nil {
		open := true
		if allowed, ok := resolved.AdditionalProperties.(bool); ok {
			open = allowed
		}
		f.openMap = &open
	}

	return f
}

// mergeField 把 allOf 分支的约束并进已有结果。
//
// 先到先得而不是后者覆盖：allOf 的语义是"同时满足全部分支"，
// 因此已经确定的约束不该被另一个分支抹掉。
func mergeField(base, branch field) field {
	if len(base.kind) == 0 || slices.Equal(base.kind, []string{"unknown"}) {
		base.kind = branch.kind
	}
	if base.format == "" {
		base.format = branch.format
	}
	if base.enum == nil {
		base.enum = branch.enum
	}
	if base.minimum == nil {
		base.minimum = branch.minimum
	}
	if base.maximum == nil {
		base.maximum = branch.maximum
	}
	if base.minLength == nil {
		base.minLength = branch.minLength
	}
	if base.maxLength == nil {
		base.maxLength = branch.maxLength
	}
	if base.pattern == "" {
		base.pattern = branch.pattern
	}
	if base.openMap == nil {
		base.openMap = branch.openMap
	}
	return base
}

// resolve 解引用到实际的 schema 定义。
func resolve(d *doc, s *schema, depth int) *schema {
	if s == nil || depth > maxFlattenDepth {
		return s
	}
	if s.Ref == "" {
		return s
	}
	return resolve(d, d.Components.Schemas[strings.TrimPrefix(s.Ref, "#/components/schemas/")], depth+1)
}

// kindOf 给出字段可取的 JSON 类型集合。
//
// 用集合而不是单个指纹：OpenAPI 3.1 的 type 可以是数组（`["integer","null"]`），
// 而"是否破坏"取决于集合的**包含方向**，不是取决于两者是否相等。
// 比相等会把放宽也报成破坏。
//
// format 不进集合：它表达的是同一 JSON 类型内部的精度，Go 侧由字段类型固定，
// 把它并进类型集合会让"补上 format"这种纯细化被报成破坏。
func kindOf(d *doc, s *schema, depth int) []string {
	if s == nil {
		return []string{"unknown"}
	}
	if s.Ref != "" {
		if depth > maxFlattenDepth {
			return []string{"ref:" + s.Ref}
		}
		// 解引用后比形状而不是比 ref 名：类型改名但形状不变不该报破坏，
		// 而 ref 名相同、字段全变才是真的破坏。
		return kindOf(d, d.Components.Schemas[strings.TrimPrefix(s.Ref, "#/components/schemas/")], depth+1)
	}

	// allOf 本身没有 type，取第一个能给出类型的分支。
	if s.Type == nil && len(s.AllOf) > 0 {
		for _, branch := range s.AllOf {
			if kinds := kindOf(d, branch, depth+1); !slices.Equal(kinds, []string{"unknown"}) {
				return kinds
			}
		}
	}

	var kinds []string
	switch t := s.Type.(type) {
	case string:
		kinds = []string{t}
	case []any:
		for _, item := range t {
			if name, ok := item.(string); ok {
				kinds = append(kinds, name)
			}
		}
	}
	if len(kinds) == 0 {
		// 没有 type 的 schema（例如只写了 properties）当作 object。
		if len(s.Properties) > 0 {
			return []string{"object"}
		}
		return []string{"unknown"}
	}
	slices.Sort(kinds)
	return slices.Compact(kinds)
}

// narrows 报告 from 是否收窄成了 to，即 from 里有 to 不接受的类型。
func narrows(from, to []string) bool {
	for _, kind := range from {
		if !slices.Contains(to, kind) {
			return true
		}
	}
	return false
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

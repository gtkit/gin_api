// Package openapi 从路由描述符生成 OpenAPI 3.1 契约。
//
// **单一来源**：契约由 RouteSpec 派生，不维护第二份手写文档。
// 手写文档与代码分开维护时，两者迟早分叉——而分叉的方向通常是
// 文档停留在某个旧版本，客户端照着它写，然后在联调时才发现对不上。
package openapi

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gtkit/gin-api/internal/router/routespec"
)

// SchemaVersion 是生成物的 OpenAPI 版本。
const SchemaVersion = "3.1.0"

// AppTokenScheme 是既有的安全方案名。
//
// **不能改名**：`api/openapi.json` 里它已经是稳定契约，客户端生成器用它
// 命名认证配置项。也不能声明成 HTTP bearer——那会让客户端发送
// `Authorization: Bearer`，而运行时只接受自定义的 `App-Token` header。
const AppTokenScheme = "AppToken"

// Document 是生成的 OpenAPI 文档。
//
// 字段顺序即 JSON 输出顺序（encoding/json 按结构体字段序输出），
// 因此结构体本身就是确定性排序的一部分。
type Document struct {
	OpenAPI    string              `json:"openapi"`
	Info       Info                `json:"info"`
	Tags       []Tag               `json:"tags,omitempty"`
	Paths      map[string]PathItem `json:"paths"`
	Components Components          `json:"components"`
}

// Info 是文档元信息。
//
// **不含生成时间**：带时间戳的生成物每次生成都不同，一致性门禁因此
// 永远失败，最终只能被关掉。
type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// Tag 是分组标签。
type Tag struct {
	Name string `json:"name"`
}

// PathItem 是一个路径下的各方法操作。
type PathItem map[string]Operation

// Operation 是一个操作。
type Operation struct {
	OperationID string                `json:"operationId"`
	Tags        []string              `json:"tags,omitempty"`
	Summary     string                `json:"summary,omitempty"`
	Deprecated  bool                  `json:"deprecated,omitempty"`
	Parameters  []Parameter           `json:"parameters,omitempty"`
	Security    []map[string][]string `json:"security,omitempty"`
	Responses   map[string]Response   `json:"responses"`
	// XAuthorizer 是受控的 vendor extension。
	//
	// 授权实现的具体 ID 用扩展字段而不是 security scheme：OpenAPI 的
	// security 只能表达"需要哪种凭据"，表达不了"需要通过哪条授权规则"。
	// 硬塞进 security 会让客户端以为那是一个它能满足的认证方式。
	XAuthorizer string `json:"x-authorizer,omitempty"`
}

// Parameter 是路径参数。
type Parameter struct {
	Name     string         `json:"name"`
	In       string         `json:"in"`
	Required bool           `json:"required"`
	Schema   map[string]any `json:"schema"`
}

// Response 是一个响应。
type Response struct {
	Description string `json:"description"`
}

// Components 承载安全方案。
type Components struct {
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes"`
}

// SecurityScheme 是一个安全方案。
type SecurityScheme struct {
	Type string `json:"type"`
	In   string `json:"in"`
	Name string `json:"name"`
}

// Options 是生成参数。
//
// 字段与 Info 同形，因此可直接转换——保留独立类型是为了让调用方
// 不必知道输出结构，未来 Info 增加纯生成侧的字段时也不会波及调用点。
type Options struct {
	Title       string
	Version     string
	Description string
}

// Generate 从编译后的路由生成文档。
//
// 输出**完全确定性**：路径、方法、标签、参数全部稳定排序，且不含任何
// 随生成时刻变化的内容。否则一致性门禁会因为无关差异而失败。
func Generate(routes []routespec.CompiledRoute, opts Options) Document {
	doc := Document{
		OpenAPI: SchemaVersion,
		Info:    Info(opts),
		Paths:   map[string]PathItem{},
		Components: Components{
			SecuritySchemes: map[string]SecurityScheme{
				AppTokenScheme: {Type: "apiKey", In: "header", Name: "App-Token"},
			},
		},
	}

	tagSet := map[string]struct{}{}

	for _, route := range routes {
		path := toOpenAPIPath(route.FullPath)
		item, ok := doc.Paths[path]
		if !ok {
			item = PathItem{}
			doc.Paths[path] = item
		}
		item[strings.ToLower(route.Spec.Method)] = toOperation(route)

		for _, tag := range route.Spec.Tags {
			tagSet[tag] = struct{}{}
		}
	}

	tags := make([]Tag, 0, len(tagSet))
	for name := range tagSet {
		tags = append(tags, Tag{Name: name})
	}
	slices.SortFunc(tags, func(a, b Tag) int { return cmp.Compare(a.Name, b.Name) })
	doc.Tags = tags

	return doc
}

// toOperation 把一条路由翻译成 OpenAPI 操作。
func toOperation(route routespec.CompiledRoute) Operation {
	op := Operation{
		OperationID: route.Spec.OperationID,
		Tags:        slices.Clone(route.Spec.Tags),
		Summary:     route.Spec.Summary,
		Deprecated:  route.Spec.Deprecated,
		Parameters:  pathParameters(route.FullPath),
		Responses:   mergeResponses(defaultResponses(route.Spec.AccessPolicy), route.Spec.Responses),
	}
	slices.Sort(op.Tags)

	op.Security = securityFor(route.Spec.AccessPolicy)
	if route.Spec.AccessPolicy.Mode == routespec.AccessAuthorized {
		op.XAuthorizer = route.Spec.AccessPolicy.AuthorizerID
	}

	return op
}

// securityFor 从访问策略**派生** security，不维护第二个字段。
//
// 维护两份的话，改了访问策略却忘了改文档，客户端会照着一份过期的
// 安全声明去实现——而那个错误只在真实调用时才暴露。
func securityFor(policy routespec.AccessPolicy) []map[string][]string {
	switch policy.Mode {
	case routespec.AccessPublic:
		return nil
	case routespec.AccessOptionalAuthenticated:
		// 空对象表示"也可以不带凭据"，与 AppToken 二选一。
		return []map[string][]string{{}, {AppTokenScheme: {}}}
	case routespec.AccessAuthenticated, routespec.AccessAuthorized:
		return []map[string][]string{{AppTokenScheme: {}}}
	default:
		// 未指定已在 compile 阶段拒绝；万一走到，按最严格的处理。
		return []map[string][]string{{AppTokenScheme: {}}}
	}
}

// defaultResponses 给出该访问模式下必然存在的响应码。
//
// 只声明由框架保证的那些：业务响应体的 schema 需要一套完整的类型映射
// 方案（validation tag、指针语义、自定义 Marshal、递归结构等），
// 那是独立的一步，不在这里凭空产出无约束的 `{}`——那样的 schema
// 看起来有契约，实际什么也没约束。
func defaultResponses(policy routespec.AccessPolicy) map[string]Response {
	responses := map[string]Response{
		"200": {Description: "Successful response"},
	}

	switch policy.Mode {
	case routespec.AccessAuthenticated, routespec.AccessOptionalAuthenticated:
		responses["401"] = Response{Description: "Missing or invalid credentials"}
	case routespec.AccessAuthorized:
		responses["401"] = Response{Description: "Missing or invalid credentials"}
		responses["403"] = Response{Description: "Authenticated but not permitted"}
		responses["503"] = Response{Description: "Authorization dependency unavailable"}
	case routespec.AccessPublic, routespec.AccessUnspecified:
	}

	return responses
}

// mergeResponses 把描述符声明的额外响应并入派生结果。
//
// 描述符优先：它是业务事实，派生规则只是兜底。
func mergeResponses(derived map[string]Response, extra map[string]string) map[string]Response {
	for status, description := range extra {
		derived[status] = Response{Description: description}
	}
	return derived
}

// pathParameters 从路径模式提取参数。
func pathParameters(fullPath string) []Parameter {
	var params []Parameter
	for _, segment := range strings.Split(fullPath, "/") {
		if !strings.HasPrefix(segment, ":") && !strings.HasPrefix(segment, "*") {
			continue
		}
		params = append(params, Parameter{
			Name:     segment[1:],
			In:       "path",
			Required: true,
			Schema:   map[string]any{"type": "string"},
		})
	}
	slices.SortFunc(params, func(a, b Parameter) int { return cmp.Compare(a.Name, b.Name) })
	return params
}

// toOpenAPIPath 把 Gin 的 `:id` / `*rest` 转成 OpenAPI 的 `{id}`。
func toOpenAPIPath(ginPath string) string {
	segments := strings.Split(ginPath, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*") {
			segments[i] = "{" + segment[1:] + "}"
		}
	}
	return strings.Join(segments, "/")
}

// Write 把文档写成确定性 JSON。
//
// encoding/json 对 map 的 key 自动排序，因此 paths / responses /
// securitySchemes 的顺序是稳定的；切片则由 Generate 显式排序。
func Write(w io.Writer, doc Document) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode openapi document: %w", err)
	}
	return nil
}

// Package openapi 从路由描述符生成 OpenAPI 3.1 契约。
//
// **单一来源**：契约由 RouteSpec 派生，不维护第二份手写文档。
// 手写文档与代码分开维护时，两者迟早分叉——而分叉的方向通常是
// 文档停留在某个旧版本，客户端照着它写，然后在联调时才发现对不上。
package openapi

import (
	"cmp"
	"encoding/json"
	"errors"
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
	RequestBody *RequestBody          `json:"requestBody,omitempty"`
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
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// Components 承载安全方案与具名 schema。
type Components struct {
	Schemas         map[string]*Schema        `json:"schemas,omitempty"`
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
	// Envelope 是统一响应信封的样本值（如 `response.ResResult{}`）。
	//
	// 由调用方传入而不是在本包写死：信封归 response 包所有，抄一份到这里
	// 就成了第二套来源，改信封时文档不会跟着变。
	//
	// 它必须**恰有一个** interface 字段——那就是业务数据槽。
	Envelope any
}

// info 取出文档元信息。
//
// Options 比 Info 多了生成侧才需要的字段，因此不能直接转换。
func (o Options) info() Info {
	return Info{Title: o.Title, Version: o.Version, Description: o.Description}
}

// Generate 从编译后的路由生成文档。
//
// 输出**完全确定性**：路径、方法、标签、参数全部稳定排序，且不含任何
// 随生成时刻变化的内容。否则一致性门禁会因为无关差异而失败。
//
// 任何一条路由的 body 无法产出有意义的 schema 时**整体失败**，不产出
// 一份"大部分正确"的契约：局部错误的契约比没有契约更危险，因为它看起来
// 是完整的，而客户端会照着错误的那部分去实现。
func Generate(routes []routespec.CompiledRoute, opts Options) (Document, error) {
	registry := NewSchemaRegistry()

	envelope, err := newEnvelopeBuilder(registry, opts.Envelope)
	if err != nil {
		return Document{}, err
	}

	doc := Document{
		OpenAPI: SchemaVersion,
		Info:    opts.info(),
		Paths:   map[string]PathItem{},
		Components: Components{
			SecuritySchemes: map[string]SecurityScheme{
				AppTokenScheme: {Type: "apiKey", In: "header", Name: "App-Token"},
			},
		},
	}

	tagSet := map[string]struct{}{}
	var errs []error

	for _, route := range routes {
		path := toOpenAPIPath(route.FullPath)
		item, ok := doc.Paths[path]
		if !ok {
			item = PathItem{}
			doc.Paths[path] = item
		}

		op, err := toOperation(route, registry, envelope)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", route.Spec.Method, route.FullPath, err))
			continue
		}
		item[strings.ToLower(route.Spec.Method)] = op

		for _, tag := range route.Spec.Tags {
			tagSet[tag] = struct{}{}
		}
	}

	if len(errs) > 0 {
		return Document{}, errors.Join(errs...)
	}

	tags := make([]Tag, 0, len(tagSet))
	for name := range tagSet {
		tags = append(tags, Tag{Name: name})
	}
	slices.SortFunc(tags, func(a, b Tag) int { return cmp.Compare(a.Name, b.Name) })
	doc.Tags = tags

	schemas, err := registry.Components()
	if err != nil {
		return Document{}, err
	}
	doc.Components.Schemas = schemas

	return doc, nil
}

// toOperation 把一条路由翻译成 OpenAPI 操作。
func toOperation(
	route routespec.CompiledRoute, registry *SchemaRegistry, envelope *envelopeBuilder,
) (Operation, error) {
	spec := route.Spec

	op := Operation{
		OperationID: spec.OperationID,
		Tags:        slices.Clone(spec.Tags),
		Summary:     spec.Summary,
		Deprecated:  spec.Deprecated,
		Parameters:  pathParameters(route.FullPath),
		Responses:   mergeResponses(defaultResponses(spec.AccessPolicy), spec.Responses),
	}
	slices.Sort(op.Tags)

	op.Security = securityFor(spec.AccessPolicy)
	if spec.AccessPolicy.Mode == routespec.AccessAuthorized {
		op.XAuthorizer = spec.AccessPolicy.AuthorizerID
	}

	if spec.RequestBody != nil {
		schema, err := registry.Of(spec.RequestBody, RoleRequest)
		if err != nil {
			return Operation{}, fmt.Errorf("request body: %w", err)
		}
		op.RequestBody = &RequestBody{
			Required: true,
			Content:  map[string]MediaType{jsonMediaType: {Schema: schema}},
		}
	}

	if err := attachResponseBodies(&op, spec, registry, envelope); err != nil {
		return Operation{}, fmt.Errorf("response body: %w", err)
	}

	return op, nil
}

// attachResponseBodies 给**每个**声明的状态码补上响应体。
//
// 不只给 200：错误响应同样有 body（统一信封里带业务错误码），只给成功码
// 声明 content 会让契约说"出错时没有响应体"，于是客户端在错误分支里
// 根本不去解析——而错误码正是它最需要读的东西。
func attachResponseBodies(
	op *Operation, spec routespec.RouteSpec, registry *SchemaRegistry, envelope *envelopeBuilder,
) error {
	success, err := responseSchema(spec, registry, envelope)
	if err != nil {
		return err
	}

	// 裸响应的错误分支也走同一个类型（探针无论 200 还是 503 都返回同一形状）；
	// 走信封的路由，错误分支是不带业务数据的信封。
	failure := success
	if spec.ResponseShape != routespec.ResponseShapeRaw {
		failure = envelope.wrap(registry, nil, "")
	}

	for status := range op.Responses {
		body := failure
		if strings.HasPrefix(status, "2") {
			body = success
		}
		response := op.Responses[status]
		response.Content = map[string]MediaType{jsonMediaType: {Schema: body}}
		op.Responses[status] = response
	}
	return nil
}

// responseSchema 按声明的形状产出 200 响应体的 schema。
//
// Empty 仍然产出信封而不是空 body：线上返回的是 `{code,msg,data:null}`，
// 声明成空 body 会让客户端跳过解析，连业务错误码都读不到。
func responseSchema(
	spec routespec.RouteSpec, registry *SchemaRegistry, envelope *envelopeBuilder,
) (*Schema, error) {
	switch spec.ResponseShape {
	case routespec.ResponseShapeEmpty:
		return envelope.wrap(registry, nil, ""), nil

	case routespec.ResponseShapeEnveloped:
		data, err := registry.Of(spec.ResponseBody, RoleResponse)
		if err != nil {
			return nil, err
		}
		return envelope.wrap(registry, data, bodySchemaName(spec.ResponseBody)), nil

	case routespec.ResponseShapeRaw:
		// 不加信封：探针的响应形状是编排器的契约，包进信封会让契约说谎。
		return registry.Of(spec.ResponseBody, RoleResponse)

	default:
		// 未声明已在 compile 阶段拒绝；万一走到，宁可整体失败也不产出一份猜出来的契约。
		return nil, fmt.Errorf("response shape %s cannot be rendered", spec.ResponseShape)
	}
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
// 只派生由框架保证的那些；业务特有的响应码归描述符声明。
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

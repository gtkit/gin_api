package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Schema 是 OpenAPI 3.1 的 schema 对象。
//
// 只建模本仓真正产出的字段。完整建模 JSON Schema 会让这个类型随规范演进
// 反复失效，而多出来的字段一个也用不上。
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 any                `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	MaxLength            *int               `json:"maxLength,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`
	Pattern              string             `json:"pattern,omitempty"`
}

// ErrUnspecifiableType 表示某个字段无法产出有意义的 schema。
//
// **拒绝生成而不是产出无约束的 `{}`**：空 schema 看起来有契约、实际什么也没约束，
// 比没有 schema 更糟——它让审阅者以为契约已经完整，也让生成的客户端
// 把该字段当成任意 JSON。作者必须显式声明它到底是什么。
var ErrUnspecifiableType = errors.New("openapi: field type cannot be expressed as a schema")

// SchemaEnum 让类型自述其枚举取值。
//
// Go 没有原生枚举，因此约定用接口而不是 tag：tag 是侵入式的（要改 dto 的
// 每个字段），而接口只需在类型上实现一次，且**编译期**就能发现漏实现。
type SchemaEnum interface {
	// SchemaEnumValues 返回该类型的全部合法取值。
	SchemaEnumValues() []any
}

// SchemaOverride 让自定义 MarshalJSON 的类型自述其线上形状。
//
// 反射看到的是 Go 结构，而 MarshalJSON 可以输出完全不同的东西——
// 例如一个 struct 序列化成字符串。没有这个接口，生成的 schema 会
// **与线上真实响应不符**，而那种不符只有客户端解析失败时才被发现。
type SchemaOverride interface {
	// OpenAPISchema 返回该类型在 JSON 上的真实形状。
	OpenAPISchema() *Schema
}

// Role 是 schema 的使用角色。
//
// 必须区分：OpenAPI 的 `required` 在请求体里是"客户端必须提供"，在响应体里是
// "服务端一定返回"。这是两件不同的事，判据也不同——前者看校验 tag，
// 后者看 `json:",omitempty"`。用同一套规则会让响应体的字段**全部变成可选**，
// 于是生成的客户端把每个字段都当成可能缺失，调用方要为不可能的情况写防御代码。
type Role uint8

const (
	// RoleRequest 是请求体：required 来自 `binding:"required"`。
	RoleRequest Role = iota
	// RoleResponse 是响应体：required 来自"没有 omitempty"。
	RoleResponse
)

func (r Role) String() string {
	if r == RoleResponse {
		return "response"
	}
	return "request"
}

// SchemaRegistry 按类型生成 schema，并把命名结构收敛为 `$ref`。
//
// 用 `$ref` 而不是就地展开：自引用结构（评论树、组织架构）展开会无限递归，
// 而共享结构重复展开会让文档体积随引用次数线性增长。
type SchemaRegistry struct {
	// components 是 `#/components/schemas` 下的定义，按 (角色, 类型名) 索引。
	components map[componentKey]*Schema
	// inProgress 记录正在生成的类型，用于打断自引用。
	inProgress map[componentKey]struct{}
}

// componentKey 让同一类型在两种角色下各有一份定义。
type componentKey struct {
	role Role
	name string
}

// NewSchemaRegistry 创建注册表。
func NewSchemaRegistry() *SchemaRegistry {
	return &SchemaRegistry{
		components: map[componentKey]*Schema{},
		inProgress: map[componentKey]struct{}{},
	}
}

// Components 返回已注册的命名 schema，供文档的 components 段使用。
//
// 同一类型同时用作请求体与响应体时**报错**而不是任选一份：两种角色的 required
// 不同，任选一份都会让另一半的契约是错的。正确的处置是为两端各定义一个类型，
// 因为"客户端要传什么"和"服务端会返回什么"本就不该被同一个结构绑死。
func (r *SchemaRegistry) Components() (map[string]*Schema, error) {
	out := make(map[string]*Schema, len(r.components))
	seen := make(map[string]Role, len(r.components))

	for key, schema := range r.components {
		if prev, dup := seen[key.name]; dup {
			return nil, fmt.Errorf(
				"%w: %s is used as both a %s and a %s body; define a separate type for each side, "+
					"because the two have different required fields",
				ErrUnspecifiableType, key.name, prev, key.role)
		}
		seen[key.name] = key.role
		out[key.name] = schema
	}
	return out, nil
}

// Of 返回给定值类型在该角色下的 schema。
func (r *SchemaRegistry) Of(v any, role Role) (*Schema, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: nil value has no type", ErrUnspecifiableType)
	}
	return r.schemaFor(reflect.TypeOf(v), role)
}

// timeType 是 time.Time 的反射类型。
var timeType = reflect.TypeOf(time.Time{})

// schemaFor 是类型 → schema 的主入口。
func (r *SchemaRegistry) schemaFor(t reflect.Type, role Role) (*Schema, error) {
	// 指针在这里只做解引用：**可空性由字段层判定**，不是类型层。
	// Go 的 *T 同时承载"可空"与"可选"两种语义，只有在字段上下文里
	// 才能区分——见 structSchema 的说明。
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// 自定义 MarshalJSON 优先：反射看到的 Go 结构与线上形状可能完全不同。
	if s, ok := overrideFor(t); ok {
		return s, nil
	}

	// 枚举优先于底层类型：一个 `type Status int8` 应当产出 enum 而不是裸 integer。
	if values, ok := enumValuesFor(t); ok {
		base, err := r.primitiveSchema(t)
		if err != nil {
			return nil, err
		}
		base.Enum = values
		return base, nil
	}

	// time.Time 与以它为底层类型的自定义时间类型都映射为 date-time 字符串。
	// 不这么做的话，反射会把 time.Time 展开成它的私有字段——那与线上形状毫无关系。
	if t == timeType || t.ConvertibleTo(timeType) && t.Kind() == reflect.Struct {
		return &Schema{Type: "string", Format: "date-time"}, nil
	}

	// 自定义序列化却没自述形状：**拒绝生成**。
	//
	// MarshalJSON 可以输出与 Go 结构完全不同的东西，此时反射产出的 schema
	// 与线上响应必然不符——而那种不符只有客户端解析失败时才被发现。
	// 实现 SchemaOverride 声明真实形状即可解除。
	if hasCustomMarshaler(t) {
		return nil, fmt.Errorf(
			"%w: %s has a custom MarshalJSON, so reflection cannot see its real JSON shape; "+
				"implement OpenAPISchema() to declare it", ErrUnspecifiableType, typeName(t))
	}

	switch t.Kind() {
	case reflect.Struct:
		return r.structRef(t, role)
	case reflect.Slice, reflect.Array:
		item, err := r.schemaFor(t.Elem(), role)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "array", Items: item}, nil
	case reflect.Map:
		// JSON 对象的 key 只能是字符串。
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%w: map key %s is not a string", ErrUnspecifiableType, t.Key())
		}
		value, err := r.schemaFor(t.Elem(), role)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "object", AdditionalProperties: value}, nil
	case reflect.Interface:
		// **拒绝生成**。这是本方案与通用库最重要的差别：
		// 通用库对 interface{} 一律输出 `{}`，那是一个看起来有契约、
		// 实际什么也没约束的 schema。作者必须显式声明它到底是什么。
		return nil, fmt.Errorf(
			"%w: %s is an interface; declare its concrete shape with OpenAPISchema() "+
				"instead of letting the contract say 'anything'", ErrUnspecifiableType, typeName(t))
	case reflect.Chan, reflect.Func, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128:
		return nil, fmt.Errorf("%w: %s cannot appear in JSON", ErrUnspecifiableType, t.Kind())
	default:
		return r.primitiveSchema(t)
	}
}

// primitiveSchema 映射基础类型。
func (r *SchemaRegistry) primitiveSchema(t reflect.Type) (*Schema, error) {
	switch t.Kind() {
	case reflect.String:
		return &Schema{Type: "string"}, nil
	case reflect.Bool:
		return &Schema{Type: "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		return &Schema{Type: "integer", Format: "int32"}, nil
	case reflect.Int64:
		return &Schema{Type: "integer", Format: "int64"}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer", Format: "int64", Minimum: floatPtr(0)}, nil
	case reflect.Float32:
		return &Schema{Type: "number", Format: "float"}, nil
	case reflect.Float64:
		return &Schema{Type: "number", Format: "double"}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported kind %s", ErrUnspecifiableType, t.Kind())
	}
}

// structRef 注册命名结构并返回一个 `$ref`。
//
// 匿名结构没有稳定名字，只能就地展开。
func (r *SchemaRegistry) structRef(t reflect.Type, role Role) (*Schema, error) {
	if t.Name() == "" {
		return r.structSchema(t, role)
	}

	key := componentKey{role: role, name: typeName(t)}
	ref := &Schema{Ref: "#/components/schemas/" + key.name}

	// 自引用：正在生成中就直接返回引用，打断递归。
	if _, building := r.inProgress[key]; building {
		return ref, nil
	}
	if _, done := r.components[key]; done {
		return ref, nil
	}

	r.inProgress[key] = struct{}{}
	schema, err := r.structSchema(t, role)
	delete(r.inProgress, key)
	if err != nil {
		return nil, err
	}

	r.components[key] = schema
	return ref, nil
}

// structSchema 生成结构体的 properties 与 required。
//
// **指针的两种语义在这里区分**：
//
//	Go 的 *T 同时承载"这个字段可以不传"与"这个值可以是 null"。
//	反射看不出作者想表达哪一种，因此判据只能来自另外两处：
//	  - `json:",omitempty"` 或校验 tag 里的 omitempty → 可选（不进 required）；
//	  - 指针本身                                      → 可空（type 为 [T, "null"]）。
//	两者独立：一个必填但可为 null 的字段是合法的，一个可选但非 null 的也是。
//	把它们混为一谈会让契约要么过严（拒绝合法请求）、要么过松（漏掉必填校验）。
func (r *SchemaRegistry) structSchema(t reflect.Type, role Role) (*Schema, error) {
	schema := &Schema{Type: "object", Properties: map[string]*Schema{}}
	var errs []error

	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		name, omitEmpty, skip := jsonFieldName(field)
		if skip {
			continue
		}

		// 内嵌结构体展开进父级（与 encoding/json 的行为一致）。
		if field.Anonymous && name == "" {
			embedded, err := r.structSchema(dereference(field.Type), role)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for k, v := range embedded.Properties {
				schema.Properties[k] = v
			}
			schema.Required = append(schema.Required, embedded.Required...)
			continue
		}
		if name == "" {
			name = field.Name
		}

		fieldSchema, err := r.schemaFor(field.Type, role)
		if err != nil {
			errs = append(errs, fmt.Errorf("field %s.%s: %w", typeName(t), field.Name, err))
			continue
		}

		binding := field.Tag.Get("binding")
		applyBindingConstraints(fieldSchema, binding)

		// 可空：指针类型允许 null。
		if field.Type.Kind() == reflect.Pointer && fieldSchema.Ref == "" {
			fieldSchema.Type = []any{fieldSchema.Type, "null"}
		}

		schema.Properties[name] = fieldSchema

		if isRequired(role, binding, omitEmpty) {
			schema.Required = append(schema.Required, name)
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	slices.Sort(schema.Required)
	return schema, nil
}

// isRequired 按角色判定字段是否必填。
//
// 请求体只认 `binding:"required"`：把"没有 omitempty"当成必填会让绝大多数字段
// 被误标为必填，契约因此拒绝掉大量合法请求。
//
// 响应体反过来只看 omitempty：响应 DTO 没有校验 tag，套用请求规则会让
// **每个字段都变成可选**，于是调用方必须为不可能缺失的字段写防御代码。
func isRequired(role Role, binding string, jsonOmitEmpty bool) bool {
	if jsonOmitEmpty {
		return false
	}
	if role == RoleResponse {
		return true
	}
	return slices.Contains(splitBinding(binding), "required")
}

// applyBindingConstraints 把校验 tag 翻译成 schema 约束。
//
// 只翻译**能无歧义映射**的那些。`min`/`max` 对字符串是长度、对数值是取值范围，
// 因此必须按 schema 的 type 分流——搞反的话，一个 `max=50` 的昵称会被
// 声明成"数值不超过 50"，客户端据此生成的校验完全不起作用。
func applyBindingConstraints(schema *Schema, binding string) {
	numeric := schema.Type == "integer" || schema.Type == "number"

	for _, rule := range splitBinding(binding) {
		key, value, hasValue := strings.Cut(rule, "=")
		switch {
		case key == "email":
			schema.Format = "email"
		case key == "url" || key == "uri":
			schema.Format = "uri"
		case key == "uuid":
			schema.Format = "uuid"
		case !hasValue:
			continue
		case key == "len":
			if n, err := strconv.Atoi(value); err == nil && !numeric {
				schema.MinLength, schema.MaxLength = &n, &n
			}
		case key == "min":
			applyBound(schema, value, numeric, true)
		case key == "max":
			applyBound(schema, value, numeric, false)
		}
	}
}

// applyBound 按 schema 类型把 min/max 落到长度或取值范围上。
func applyBound(schema *Schema, raw string, numeric, isMin bool) {
	if numeric {
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return
		}
		if isMin {
			schema.Minimum = &f
		} else {
			schema.Maximum = &f
		}
		return
	}

	n, err := strconv.Atoi(raw)
	if err != nil {
		return
	}
	if isMin {
		schema.MinLength = &n
	} else {
		schema.MaxLength = &n
	}
}

// splitBinding 拆分校验 tag。
func splitBinding(binding string) []string {
	if binding == "" {
		return nil
	}
	return strings.Split(binding, ",")
}

// jsonFieldName 解析 json tag。
func jsonFieldName(field reflect.StructField) (name string, omitEmpty, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	omitEmpty = slices.Contains(parts[1:], "omitempty") || slices.Contains(parts[1:], "omitzero")
	return name, omitEmpty, false
}

// overrideFor 查询类型是否自述了线上形状。
func overrideFor(t reflect.Type) (*Schema, bool) {
	overrideInterface := reflect.TypeOf((*SchemaOverride)(nil)).Elem()
	for _, candidate := range []reflect.Type{t, reflect.PointerTo(t)} {
		if candidate.Implements(overrideInterface) {
			v := reflect.New(t).Elem()
			if candidate.Kind() == reflect.Pointer {
				v = reflect.New(t)
			}
			impl, ok := v.Interface().(SchemaOverride)
			if ok {
				return impl.OpenAPISchema(), true
			}
		}
	}
	return nil, false
}

// enumValuesFor 查询类型是否自述了枚举取值。
func enumValuesFor(t reflect.Type) ([]any, bool) {
	enumInterface := reflect.TypeOf((*SchemaEnum)(nil)).Elem()
	if !t.Implements(enumInterface) {
		return nil, false
	}
	impl, ok := reflect.New(t).Elem().Interface().(SchemaEnum)
	if !ok {
		return nil, false
	}
	return impl.SchemaEnumValues(), true
}

// hasCustomMarshaler 报告类型是否实现了 json.Marshaler。
func hasCustomMarshaler(t reflect.Type) bool {
	marshaler := reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	return t.Implements(marshaler) || reflect.PointerTo(t).Implements(marshaler)
}

// dereference 剥掉指针。
func dereference(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// typeName 返回用于 `$ref` 的稳定类型名。
//
// 带包名前缀：两个包里同名的 Response 结构会互相覆盖，而那种覆盖
// 是静默的——生成的文档里只剩一个，另一个的字段全错。
func typeName(t reflect.Type) string {
	pkg := t.PkgPath()
	if pkg == "" {
		return t.Name()
	}
	segments := strings.Split(pkg, "/")
	return segments[len(segments)-1] + "." + t.Name()
}

func floatPtr(f float64) *float64 { return &f }

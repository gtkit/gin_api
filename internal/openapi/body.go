package openapi

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// jsonMediaType 是本仓唯一的 body 媒体类型。
const jsonMediaType = "application/json"

// RequestBody 是一个请求体。
type RequestBody struct {
	Required bool                 `json:"required"`
	Content  map[string]MediaType `json:"content"`
}

// MediaType 是某种媒体类型下的 schema。
type MediaType struct {
	Schema *Schema `json:"schema"`
}

// ErrEnvelopeShape 表示信封样本值不符合"恰有一个数据槽"的要求。
var ErrEnvelopeShape = errors.New("openapi: envelope sample must have exactly one interface field")

// envelopeBuilder 把业务数据 schema 包进统一响应信封。
//
// 信封形状**反射自 response 包的真实类型**而不是写死在这里：写死的话，
// 改了信封字段（比如 msg 改名）文档不会跟着变，而那种分叉只有客户端
// 解析失败时才被发现。
//
// 数据槽的定位方式是"找那个 interface 字段"——这正好利用了 schema 生成器
// 对 `any` 的拒绝：信封里那个无法生成 schema 的字段，就是要被替换的槽。
type envelopeBuilder struct {
	// structure 是信封除数据槽外的其余字段。
	structure *Schema
	// dataField 是数据槽的 JSON 字段名。
	dataField string
	// typeName 是信封类型名，用于命名生成的 component。
	typeName string
}

// newEnvelopeBuilder 从样本值构造。
func newEnvelopeBuilder(registry *SchemaRegistry, sample any) (*envelopeBuilder, error) {
	if sample == nil {
		return nil, fmt.Errorf("%w: got nil", ErrEnvelopeShape)
	}
	t := dereference(reflect.TypeOf(sample))
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: got %s", ErrEnvelopeShape, t.Kind())
	}

	shell := &Schema{Type: "object", Properties: map[string]*Schema{}}
	var slots []string

	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, skip := jsonFieldName(field)
		if skip {
			continue
		}
		if name == "" {
			name = field.Name
		}

		if dereference(field.Type).Kind() == reflect.Interface {
			slots = append(slots, name)
			continue
		}

		schema, err := registry.schemaFor(field.Type, RoleResponse)
		if err != nil {
			return nil, fmt.Errorf("envelope field %s: %w", field.Name, err)
		}
		shell.Properties[name] = schema
		shell.Required = append(shell.Required, name)
	}

	if len(slots) != 1 {
		return nil, fmt.Errorf(
			"%w: %s has %d interface fields (%s); the data slot would be ambiguous",
			ErrEnvelopeShape, typeName(t), len(slots), strings.Join(slots, ", "))
	}

	return &envelopeBuilder{structure: shell, dataField: slots[0], typeName: t.Name()}, nil
}

// wrap 把数据 schema 装进信封，注册为具名 component 并返回引用。
//
// data 为 nil 表示该操作没有业务数据，此时数据槽声明为 null。
func (b *envelopeBuilder) wrap(registry *SchemaRegistry, data *Schema, dataName string) *Schema {
	body := &Schema{
		Type:       b.structure.Type,
		Properties: make(map[string]*Schema, len(b.structure.Properties)+1),
		Required:   append([]string(nil), b.structure.Required...),
	}
	for k, v := range b.structure.Properties {
		body.Properties[k] = v
	}

	if data == nil {
		body.Properties[b.dataField] = &Schema{Type: "null"}
		dataName = "Empty"
	} else {
		body.Properties[b.dataField] = data
	}
	body.Required = append(body.Required, b.dataField)

	key := componentKey{role: RoleResponse, name: dataName + b.typeName}
	registry.components[key] = body
	return &Schema{Ref: "#/components/schemas/" + key.name}
}

// bodySchemaName 从样本值推导用于命名信封 component 的短名。
func bodySchemaName(sample any) string {
	t := dereference(reflect.TypeOf(sample))
	if t.Name() == "" {
		return "Anonymous"
	}
	return t.Name()
}

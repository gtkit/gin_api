// Package json is a JSON encoding and decoding.
package json

import (
	"encoding/json"
	"reflect"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/gtkit/gin-api/internal/pkg/encoding"
)

// Name is the name registered for the json codec.
const Name = "json"

var (
	// MarshalOptions is a configurable JSON format marshaller. Configure before serving requests.
	MarshalOptions = protojson.MarshalOptions{
		EmitUnpopulated: true, // 控制零值字段输出: false 减少数据传输量,true 输出所有字段,包括零值字段
		UseProtoNames:   true, // 字段命名策略，控制驼峰输出: false → camelCase（适合前端）,true → snake_case（适合内部服务
	}
	// UnmarshalOptions is a configurable JSON format parser. Configure before serving requests.
	UnmarshalOptions = protojson.UnmarshalOptions{
		DiscardUnknown: true,
	}
)

func init() {
	encoding.RegisterCodec(codec{})
}

// codec is a Codec implementation with json.
type codec struct{}

// Marshal object to data
func (codec) Marshal(v any) ([]byte, error) {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return json.Marshal(v)
	}
	switch m := v.(type) {
	case json.Marshaler:
		return m.MarshalJSON()
	case proto.Message:
		return MarshalOptions.Marshal(m)
	default:
		return json.Marshal(m)
	}
}

// Unmarshal data to bytes
func (codec) Unmarshal(data []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return encoding.ErrNotAPointer
	}
	if m, ok := v.(json.Unmarshaler); ok {
		return m.UnmarshalJSON(data)
	}
	// Only allocate pointer chains for protobuf messages. Ordinary pointers
	// retain encoding/json's null and custom unmarshaler semantics.
	target := rv.Type()
	for target.Kind() == reflect.Pointer {
		if target.Implements(reflect.TypeFor[proto.Message]()) {
			for rv.Type() != target {
				rv = rv.Elem()
				if rv.IsNil() {
					rv.Set(reflect.New(rv.Type().Elem()))
				}
			}
			return UnmarshalOptions.Unmarshal(data, rv.Interface().(proto.Message))
		}
		target = target.Elem()
	}
	return json.Unmarshal(data, v)
}

// Name get name
func (codec) Name() string {
	return Name
}

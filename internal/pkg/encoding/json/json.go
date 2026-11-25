// Package json is a JSON encoding and decoding.
package json

import (
	"encoding/json"
	"reflect"
	
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	
	"my_gin/internal/pkg/encoding"
)

// Name is the name registered for the json codec.
const Name = "json"

var (
	// MarshalOptions is a configurable JSON format marshaller.
	MarshalOptions = protojson.MarshalOptions{
		EmitUnpopulated: true, // 控制零值字段输出: false 减少数据传输量,true 输出所有字段,包括零值字段
		UseProtoNames:   true, // 字段命名策略，控制驼峰输出: false → camelCase（适合前端）,true → snake_case（适合内部服务
	}
	// UnmarshalOptions is a configurable JSON format parser.
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
	switch m := v.(type) {
	case json.Unmarshaler:
		return m.UnmarshalJSON(data)
	case proto.Message:
		return UnmarshalOptions.Unmarshal(data, m)
	default:
		rv := reflect.ValueOf(v)
		for rv := rv; rv.Kind() == reflect.Ptr; {
			if rv.IsNil() {
				rv.Set(reflect.New(rv.Type().Elem()))
			}
			rv = rv.Elem()
		}
		if m, ok := reflect.Indirect(rv).Interface().(proto.Message); ok {
			return UnmarshalOptions.Unmarshal(data, m)
		}
		return json.Unmarshal(data, m)
	}
}

// Name get name
func (codec) Name() string {
	return Name
}

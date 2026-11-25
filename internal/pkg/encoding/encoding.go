// Package encoding Provides encoding and decoding of json, protobuf and gob.
package encoding

import (
	"encoding"
	"errors"
	"reflect"
	"strings"
	"sync"
)

var (
	// ErrNotAPointer .
	ErrNotAPointer = errors.New("v argument must be a pointer")
)

// Codec defines the interface gRPC uses to encode and decode messages.  Note
// that implementations of this interface must be thread safe; a Codec's
// methods can be called from concurrent goroutines.
type Codec interface {
	// Marshal returns the wire format of v.
	Marshal(v any) ([]byte, error)
	// Unmarshal parses the wire format into v.
	Unmarshal(data []byte, v any) error
	// Name returns the name of the Codec implementation. The returned string
	// will be used as part of content type in transmission.  The result must be
	// static; the result cannot change between calls.
	Name() string
}

var registeredCodecs sync.Map // lowercase name -> Codec

// RegisterCodec registers the provided Codec for use with all transport clients and
// servers.
//
// The Codec will be stored and looked up by result of its Name() method, which
// should match the content-subtype of the encoding handled by the Codec.  This
// is case-insensitive, and is stored and looked up as lowercase.  If the
// result of calling Name() is an empty string, RegisterCodec will panic. See
// Content-Type on
// https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md#requests for
// more details.
//
// Re-registering a name atomically replaces its codec.
func RegisterCodec(codec Codec) {
	if codec == nil || isNilPointer(codec) {
		panic("cannot register a nil Codec")
	}
	contentSubtype := strings.ToLower(codec.Name())
	if contentSubtype == "" {
		panic("cannot register Codec with empty string result for Name()")
	}
	registeredCodecs.Store(contentSubtype, codec)
}

// GetCodec gets a registered Codec by content-subtype, or nil if no Codec is
// registered for the content-subtype.
//
// The lookup is case-insensitive.
func GetCodec(contentSubtype string) Codec {
	v, ok := registeredCodecs.Load(strings.ToLower(contentSubtype))
	if !ok {
		return nil
	}
	return v.(Codec)
}

// Encoding definition of coding interfaces
type Encoding interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

// Marshal encodes with the selected wire format. With no codec, the value
// must explicitly implement encoding.BinaryMarshaler.
func Marshal(e Encoding, v any) ([]byte, error) {
	if !isPointer(v) {
		return nil, ErrNotAPointer
	}
	if e != nil {
		if isNilPointer(e) {
			return nil, errors.New("nil encoding")
		}
		return e.Marshal(v)
	}
	if m, ok := v.(encoding.BinaryMarshaler); ok {
		return m.MarshalBinary()
	}
	return nil, errors.New("no codec or BinaryMarshaler")
}

// Unmarshal decodes only the selected wire format.
func Unmarshal(e Encoding, data []byte, v any) error {
	if !isPointer(v) {
		return ErrNotAPointer
	}
	if e != nil {
		if isNilPointer(e) {
			return errors.New("nil encoding")
		}
		return e.Unmarshal(data, v)
	}
	if m, ok := v.(encoding.BinaryUnmarshaler); ok {
		return m.UnmarshalBinary(data)
	}
	return errors.New("no codec or BinaryUnmarshaler")
}

func isPointer(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && !rv.IsNil()
}

func isNilPointer(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

package encoding

import "github.com/vmihailenco/msgpack"

// MsgPackEncoding msgpack format
type MsgPackEncoding struct{}

// Marshal msgpack encode
func (mp MsgPackEncoding) Marshal(v any) ([]byte, error) {
	buf, err := msgpack.Marshal(v)
	return buf, err
}

// Unmarshal msgpack decode
func (mp MsgPackEncoding) Unmarshal(data []byte, value any) error {
	err := msgpack.Unmarshal(data, value)
	if err != nil {
		return err
	}
	return nil
}

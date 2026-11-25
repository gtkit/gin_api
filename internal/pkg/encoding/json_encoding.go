package encoding

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"

	"github.com/klauspost/compress/snappy"
)

// JSONEncoding json format
type JSONEncoding struct{}

// Marshal json encode
func (j JSONEncoding) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal json decode
func (j JSONEncoding) Unmarshal(data []byte, value any) error {
	return json.Unmarshal(data, value)
}

// JSONGzipEncoding json and gzip
type JSONGzipEncoding struct{}

// Marshal json encode and gzip
func (jz JSONGzipEncoding) Marshal(v any) ([]byte, error) {
	buf, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return GzipEncode(buf)
}

// Unmarshal json encode and gzip
func (jz JSONGzipEncoding) Unmarshal(data []byte, value any) error {
	jsonData, err := GzipDecode(data)
	if err != nil {
		return err
	}

	return json.Unmarshal(jsonData, value)
}

// Compressed codecs accept up to 64 MiB of plaintext per message.
const maxPlainBytes = 64 << 20
const maxWireBytes = 80 << 20

var ErrPayloadTooLarge = errors.New("encoding: payload exceeds size limit")

// GzipEncode produces a standard gzip member.
func GzipEncode(in []byte) ([]byte, error) {
	if len(in) > maxPlainBytes {
		return nil, ErrPayloadTooLarge
	}
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(in); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GzipDecode validates all members and bounds their combined output.
func GzipDecode(in []byte) ([]byte, error) {
	return gzipDecode(in, maxPlainBytes)
}

func gzipDecode(in []byte, limit int64) ([]byte, error) {
	if len(in) > maxWireBytes {
		return nil, ErrPayloadTooLarge
	}
	r, err := gzip.NewReader(bytes.NewReader(in))
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(r, limit+1))
	closeErr := r.Close()
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, ErrPayloadTooLarge
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return out, nil
}

// JSONSnappyEncoding json format and snappy compression
type JSONSnappyEncoding struct{}

// Marshal serialization
func (s JSONSnappyEncoding) Marshal(v any) (data []byte, err error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > maxPlainBytes {
		return nil, ErrPayloadTooLarge
	}
	d := snappy.Encode(nil, b)
	return d, nil
}

// Unmarshal deserialization
func (s JSONSnappyEncoding) Unmarshal(data []byte, value any) error {
	if len(data) > maxWireBytes {
		return ErrPayloadTooLarge
	}
	size, err := snappy.DecodedLen(data)
	if err != nil {
		return err
	}
	if size > maxPlainBytes {
		return ErrPayloadTooLarge
	}
	b, err := snappy.Decode(nil, data)
	if err != nil {
		return err
	}

	return json.Unmarshal(b, value)
}

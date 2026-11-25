package encoding

import (
	"bytes"
	"encoding/gob"
)

// GobEncoding gob encode
type GobEncoding struct{}

// Marshal gob encode
func (g GobEncoding) Marshal(v any) ([]byte, error) {
	var (
		buffer bytes.Buffer
	)
	
	err := gob.NewEncoder(&buffer).Encode(v)
	return buffer.Bytes(), err
}

// Unmarshal gob encode
func (g GobEncoding) Unmarshal(data []byte, value any) error {
	err := gob.NewDecoder(bytes.NewReader(data)).Decode(value)
	if err != nil {
		return err
	}
	return nil
}

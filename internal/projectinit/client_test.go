//go:build scaffold

package projectinit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedClientMatchesContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := RenderClient(raw)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join("..", "pkg", "apiclient", "client.gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, generated) {
		t.Fatal("generated client differs from the contract")
	}
	for _, input := range [][]byte{[]byte(`{}`), bytes.Replace(raw, []byte(`"operationId": "login"`), []byte(`"operationId": "health"`), 1), bytes.ReplaceAll(raw, []byte(`"type": "string"`), []byte(`"type": "unknown"`))} {
		if _, err := RenderClient(input); err == nil {
			t.Fatal("invalid contract was accepted")
		}
	}
}

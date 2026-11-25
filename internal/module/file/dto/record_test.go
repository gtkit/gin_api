package dto

import (
	"reflect"
	"testing"

	"github.com/gtkit/gouploader"
)

func TestChunkDTOsPreservePublishedFields(t *testing.T) {
	t.Parallel()
	for _, pair := range []struct{ actual, previous any }{
		{ChunkInitRequest{}, gouploader.ChunkInitRequest{}},
		{ChunkInitResponse{}, gouploader.ChunkInitResponse{}},
		{ChunkUploadResponse{}, gouploader.ChunkUploadResponse{}},
		{ChunkStatusResponse{}, gouploader.ChunkStatusResponse{}},
		{ResumeRequest{}, gouploader.ResumeRequest{}},
		{ResumeResponse{}, gouploader.ResumeResponse{}},
	} {
		actual, previous := reflect.TypeOf(pair.actual), reflect.TypeOf(pair.previous)
		if actual.NumField() != previous.NumField() {
			t.Fatalf("%s fields changed from %d to %d", actual.Name(), previous.NumField(), actual.NumField())
		}
		for i := range actual.NumField() {
			a, b := actual.Field(i), previous.Field(i)
			if a.Name != b.Name || a.Type != b.Type || a.Tag.Get("json") != b.Tag.Get("json") || a.Tag.Get("binding") != b.Tag.Get("binding") {
				t.Errorf("%s.%s changed from %v to %v", actual.Name(), b.Name, b, a)
			}
		}
	}
}

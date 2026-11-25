package proto

import (
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/encoding"
	_ "github.com/gtkit/gin-api/internal/pkg/encoding/json"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

func TestProto(t *testing.T) {
	v := &pluginpb.CodeGeneratorRequest{FileToGenerate: []string{"one", "two"}, Parameter: new("test")}
	for _, name := range []string{"json", "proto"} {
		c := encoding.GetCodec(name)
		data, err := encoding.Marshal(c, v)
		if err != nil {
			t.Fatal(err)
		}
		var got pluginpb.CodeGeneratorRequest
		if err := encoding.Unmarshal(c, data, &got); err != nil || !proto.Equal(v, &got) {
			t.Fatalf("%s round trip failed: %v", name, err)
		}
	}
	var dst *pluginpb.CodeGeneratorRequest
	for _, bad := range []any{nil, dst, "text"} {
		if _, err := (codec{}).Marshal(bad); err == nil {
			t.Fatal("accepted invalid message")
		}
		if err := (codec{}).Unmarshal(nil, bad); err == nil {
			t.Fatal("accepted invalid target")
		}
	}
}

// Compare identical populated messages through the registered project codecs.
func BenchmarkMessageRoundTrip(b *testing.B) {
	v := &pluginpb.CodeGeneratorRequest{FileToGenerate: []string{"account.proto", "file.proto", "events.proto"}, Parameter: new("paths=source_relative"), CompilerVersion: &pluginpb.Version{Major: new(int32(5)), Minor: new(int32(1))}}
	b.Run("protojson", func(b *testing.B) { benchmarkRoundTrip(b, encoding.GetCodec("json"), v) })
	b.Run("protobuf", func(b *testing.B) { benchmarkRoundTrip(b, encoding.GetCodec("proto"), v) })
	type version struct {
		Major int32 `json:"major"`
		Minor int32 `json:"minor"`
	}
	type message struct {
		Files     []string `json:"file_to_generate"`
		Parameter string   `json:"parameter"`
		Compiler  version  `json:"compiler_version"`
	}
	dto := message{v.FileToGenerate, v.GetParameter(), version{v.CompilerVersion.GetMajor(), v.CompilerVersion.GetMinor()}}
	b.Run("json_dto", func(b *testing.B) { benchmarkRoundTrip(b, encoding.JSONEncoding{}, &dto) })
}

func benchmarkRoundTrip[T any](b *testing.B, c encoding.Encoding, v *T) {
	wire, err := encoding.Marshal(c, v)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		data, err := encoding.Marshal(c, v)
		if err != nil {
			b.Fatal(err)
		}
		var decoded T
		if err := encoding.Unmarshal(c, data, &decoded); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(wire)), "wire-B")
}

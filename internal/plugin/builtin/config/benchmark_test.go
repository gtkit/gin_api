package config

import (
	"context"
	"strings"
	"testing"

	schema "github.com/gtkit/gin-api/internal/platform/config"
)

func BenchmarkConfigurationValue(b *testing.B) {
	for _, parallel := range []bool{false, true} {
		name := "serial"
		if parallel {
			name = "parallel"
		}
		b.Run(name, func(b *testing.B) {
			cfg, err := loadFromReader(strings.NewReader(minimalConfigYAML()))
			if err != nil {
				b.Fatal(err)
			}
			p := New(func(context.Context) (*schema.Config, error) { return cfg, nil })
			if err := p.Init(b.Context()); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			if parallel {
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if _, err := p.Value(); err != nil {
							b.Error(err)
						}
					}
				})
				return
			}
			for b.Loop() {
				if _, err := p.Value(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

package core

import (
	"context"
	"strconv"
	"testing"
)

type benchmarkPlugin struct{ name string }

func (p benchmarkPlugin) Name() string              { return p.name }
func (benchmarkPlugin) Init(context.Context) error  { return nil }
func (benchmarkPlugin) Start(context.Context) error { return nil }
func (benchmarkPlugin) Stop(context.Context) error  { return nil }

// BenchmarkPluginRegistryLifecycle measures one registry's full startup and
// stop with no-I/O plugins. It is not an API throughput or component benchmark.
func BenchmarkPluginRegistryLifecycle(b *testing.B) {
	for _, shape := range []string{"independent", "chain"} {
		for _, size := range []int{6, 128, 1024} {
			b.Run(shape+"/"+strconv.Itoa(size), func(b *testing.B) {
				plugins := make([]benchmarkPlugin, size)
				deps := make([][]string, size)
				for i := range size {
					plugins[i].name = strconv.Itoa(i)
					if shape == "chain" && i > 0 {
						deps[i] = []string{plugins[i-1].name}
					}
				}
				b.ReportAllocs()
				for b.Loop() {
					r := NewPluginRegistry()
					for i, plugin := range plugins {
						if err := r.Register(plugin, deps[i]...); err != nil {
							b.Fatal(err)
						}
					}
					if err := r.Start(b.Context()); err != nil {
						b.Fatal(err)
					}
					if err := r.Stop(b.Context()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

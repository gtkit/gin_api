package bootstrap

import (
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/core"
)

func TestRuntimeRejectsMissingRegistrarOrCapabilitiesBeforeInit(t *testing.T) {
	if _, err := NewRuntime(t.Context(), Options{}); err == nil {
		t.Fatal("missing registrar accepted")
	}
	for _, mode := range []string{"nil", "typed nil", "drain", "registration error"} {
		t.Run(mode, func(t *testing.T) {
			f := newLifecycleFixture(t)
			sentinel := errors.New("registration failed")
			registrar := func(r *core.PluginRegistry) (PluginSet, error) {
				set, err := f.register(r)
				if err != nil {
					return set, err
				}
				switch mode {
				case "nil":
					set.Logging = nil
				case "typed nil":
					set.Cache = (*lifecycleComponent)(nil)
				case "drain":
					set.DrainLogs = nil
				case "registration error":
					return PluginSet{}, sentinel
				}
				return set, nil
			}
			if _, err := NewRuntime(t.Context(), Options{RegisterPlugins: registrar}); err == nil {
				t.Fatal("invalid registration accepted")
			}
			if len(f.events) != 0 {
				t.Fatalf("callbacks before registration validation: %v", f.events)
			}
		})
	}
}

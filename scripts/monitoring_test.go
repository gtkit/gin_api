//go:build ignore

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestMonitoringArtifacts(t *testing.T) {
	for _, file := range []string{"collector.yml", "prometheus-rules.yml"} {
		raw, err := os.ReadFile(filepath.Join("..", "monitoring", file))
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := yaml.Unmarshal(raw, &document); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if len(document) == 0 {
			t.Fatalf("empty %s", file)
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "monitoring", "dashboard.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Panels []struct {
			ID      int `json:"id"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, panel := range dashboard.Panels {
		if seen[panel.ID] || len(panel.Targets) == 0 || panel.Targets[0].Expr == "" {
			t.Fatal("invalid dashboard panel")
		}
		seen[panel.ID] = true
	}
	if len(seen) != 12 {
		t.Fatalf("expected twelve operational panels, got %d", len(seen))
	}
}

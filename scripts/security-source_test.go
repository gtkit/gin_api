//go:build ignore

package main

import (
	"strings"
	"testing"
)

func TestSourceFindingsMappedAndRedacted(t *testing.T) {
	raw := []byte(`[{"RuleID":"token","StartLine":6,"EndLine":6,"Secret":"must-not-survive","Match":"must-not-survive"}]`)
	got, err := mapFindings(raw, []sourcePart{{File: "example.go", First: 4, Last: 9}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].File != "example.go" || got[0].StartLine != 3 || strings.Contains(got[0].Fingerprint, "must-not") {
		t.Fatalf("invalid mapped report: %+v", got)
	}
	if _, err := mapFindings(raw, nil); err == nil {
		t.Fatal("unmapped finding accepted")
	}
	for _, raw := range []string{"null", "", "{", `[{"RuleID":"token","StartLine":2,"EndLine":1}]`} {
		if _, err := mapFindings([]byte(raw), nil); err == nil {
			t.Fatalf("accepted invalid scanner output %q", raw)
		}
	}
}

func TestVulnerabilityScannerFailureIsNotClean(t *testing.T) {
	for _, raw := range []string{"", "null", "{", `{"progress":{}}`, `{"config":null}`, `{"config":"invalid"}`} {
		if err := validVulnerabilityReport([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid report %q", raw)
		}
	}
	if err := validVulnerabilityReport([]byte("{\"config\":{}}\n{\"progress\":{}}")); err != nil {
		t.Fatal(err)
	}
}

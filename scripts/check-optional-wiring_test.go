//go:build ignore

package main

import (
	"slices"
	"strings"
	"testing"
)

func TestOptionalWiringProfiles(t *testing.T) {
	got, err := profiles("all")
	if err != nil || !slices.Equal(got, []string{"default", "optional"}) {
		t.Fatalf("profiles: %v %v", got, err)
	}
	if _, err := profiles("arbitrary-build-tag"); err == nil {
		t.Fatal("unrecognized profile accepted")
	}
}

func TestOptionalWiringEnvironment(t *testing.T) {
	got := wiringEnvironment([]string{"PATH=/toolchain", "GOFLAGS=-race", "GOWORK=/other/work", "GOTOOLCHAIN=auto", "GOPROXY=https://proxy.example.test"})
	for _, expected := range []string{"GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "PATH=/toolchain", "GOPROXY=https://proxy.example.test"} {
		if !slices.Contains(got, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	for _, value := range got {
		if strings.Contains(value, "-race") || strings.Contains(value, "/other/work") {
			t.Fatalf("unsafe build override retained: %s", value)
		}
	}
}

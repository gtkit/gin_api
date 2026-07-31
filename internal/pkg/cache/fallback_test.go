package cache

import (
	"testing"
	"time"
)

func TestNewFallbackCache(t *testing.T) {
	fc, err := NewFallbackCache("test", 5*time.Minute, 1)
	if err != nil {
		t.Fatalf("NewFallbackCache error: %v", err)
	}
	if fc.Name() != "test" {
		t.Fatalf("Name() = %q, want %q", fc.Name(), "test")
	}
}

func TestGetMiss(t *testing.T) {
	fc, _ := NewFallbackCache("test-miss", 5*time.Minute, 1)

	_, ok := fc.Get("nonexistent")
	if ok {
		t.Fatal("Get should return false for nonexistent key")
	}
}

func TestSetAndGet(t *testing.T) {
	fc, _ := NewFallbackCache("test-set-get", 5*time.Minute, 1)

	fc.Set("key1", []byte(`{"data":"hello"}`))

	data, ok := fc.Get("key1")
	if !ok {
		t.Fatal("Get should return true after Set")
	}
	if string(data) != `{"data":"hello"}` {
		t.Fatalf("data = %q, want %q", string(data), `{"data":"hello"}`)
	}
}

func TestSetOverwritesPreviousValue(t *testing.T) {
	fc, _ := NewFallbackCache("test-overwrite", 5*time.Minute, 1)

	fc.Set("key1", []byte("v1"))
	fc.Set("key1", []byte("v2"))

	data, ok := fc.Get("key1")
	if !ok {
		t.Fatal("Get should return true")
	}
	if string(data) != "v2" {
		t.Fatalf("data = %q, want %q", string(data), "v2")
	}
}

func TestDifferentKeysIsolated(t *testing.T) {
	fc, _ := NewFallbackCache("test-isolated", 5*time.Minute, 1)

	fc.Set("a", []byte("value-a"))
	fc.Set("b", []byte("value-b"))

	dataA, _ := fc.Get("a")
	dataB, _ := fc.Get("b")

	if string(dataA) != "value-a" {
		t.Fatalf("a = %q, want %q", string(dataA), "value-a")
	}
	if string(dataB) != "value-b" {
		t.Fatalf("b = %q, want %q", string(dataB), "value-b")
	}
}

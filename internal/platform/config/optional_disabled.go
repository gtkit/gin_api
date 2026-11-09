//go:build !optional

package config

// OptionalConfig is empty in the default service build.
// Unrecognized optional YAML keys remain visible through IgnoredKeys and doctor.
type OptionalConfig struct{}

func (*Config) TaskQueueEnabled() bool  { return false }
func (*Config) validateOptional() error { return nil }

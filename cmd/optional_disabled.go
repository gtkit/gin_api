//go:build !optional

package cmd

import "github.com/gtkit/gin-api/internal/platform/config"

func optionalDDLModels(*config.Config) []any { return nil }

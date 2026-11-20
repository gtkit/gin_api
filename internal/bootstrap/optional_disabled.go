//go:build !optional

package bootstrap

import "github.com/gin-gonic/gin"

type optionalInfra struct{} //nolint:unused // Keeps infra shared with optional builds; this build has no optional fields.

type optionalServices struct{} //nolint:unused // Keeps services shared with optional builds; this build has no optional modules.

func buildOptionalProviders(*infra, *providerCleanup) error                      { return nil }
func optionalWorkers(*infra) ([]Worker, error)                                   { return nil, nil }
func buildOptionalServices(*services, infra) error                               { return nil }
func registerOptionalModules(*gin.RouterGroup, services, gin.HandlerFunc, infra) {}

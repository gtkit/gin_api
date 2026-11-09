//go:build !optional

package bootstrap

type optionalInfra struct{} //nolint:unused // Keeps infra shared with optional builds; this build has no optional fields.

func buildOptionalProviders(*infra, *providerCleanup) error { return nil }
func optionalWorkers(*infra) ([]Worker, error)              { return nil, nil }

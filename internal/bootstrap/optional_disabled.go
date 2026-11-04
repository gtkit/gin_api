//go:build !optional

package bootstrap

type optionalInfra struct{}

func buildOptionalProviders(*infra, *providerCleanup) error { return nil }
func optionalWorkers(*infra) ([]Worker, error)              { return nil, nil }

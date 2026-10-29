package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type namedChecker struct {
	stubChecker
	named map[string]func(context.Context) error
}

func (c *namedChecker) NamedConnectionChecks() map[string]func(context.Context) error { return c.named }

func TestReadinessKeepsNamedConnectionsRequiredAndDefaultRedisOptional(t *testing.T) {
	for _, failNamed := range []bool{false, true} {
		t.Run(map[bool]string{false: "optional Redis only", true: "named Redis failed"}[failNamed], func(t *testing.T) {
			checker := &namedChecker{redisErr: errors.New("default Redis unavailable"),
				named: map[string]func(context.Context) error{
					"database/reports": func(context.Context) error { return nil },
					"redis/sessions": func(context.Context) error {
						if failNamed {
							return errors.New("private connection details")
						}
						return nil
					},
				}}
			engine := newTestEngine(t, checker)
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
			var body ReadinessResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantHTTP, wantNamed := http.StatusOK, statusOK
			if failNamed {
				wantHTTP, wantNamed = http.StatusServiceUnavailable, statusUnavailable
			}
			if response.Code != wantHTTP || body.Checks["redis"].Status != statusDegraded || body.Checks["redis/sessions"].Status != wantNamed || body.Checks["database/reports"].Status != statusOK {
				t.Fatalf("status=%d body=%+v", response.Code, body)
			}
			if failNamed && body.Checks["redis/sessions"].Error != dependencyFailureMessage {
				t.Fatal("raw dependency error leaked")
			}
		})
	}
}

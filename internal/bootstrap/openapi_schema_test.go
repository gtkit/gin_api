package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/module/auth/dto"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/router"
)

type contractSchema struct {
	Type       string                    `json:"type"`
	Ref        string                    `json:"$ref"`
	Properties map[string]contractSchema `json:"properties"`
	Required   []string                  `json:"required"`
	MinLength  int                       `json:"minLength"`
	MaxLength  int                       `json:"maxLength"`
}

func readFieldContract(t *testing.T) map[string]contractSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]contractSchema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Components.Schemas
}

func checkDTOFields(schema contractSchema, typ reflect.Type, request bool) error {
	fields := 0
	for field := range typ.Fields() {
		if !field.IsExported() {
			continue
		}
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		fields++
		property, ok := schema.Properties[name]
		if !ok {
			return fmt.Errorf("missing property %s", name)
		}
		if field.Type.Kind() == reflect.String && property.Type != "string" {
			return fmt.Errorf("%s must be string", name)
		}
		if field.Type.Kind() == reflect.Int64 && property.Type != "integer" {
			return fmt.Errorf("%s must be integer", name)
		}
		required := !strings.Contains(options, "omitempty")
		if request {
			rules := strings.Split(field.Tag.Get("binding"), ",")
			required = slices.Contains(rules, "required")
			minLength := 0
			if required {
				minLength = 1
			}
			maxLength := 0
			for _, rule := range rules {
				key, value, ok := strings.Cut(rule, "=")
				if !ok {
					continue
				}
				n, err := strconv.Atoi(value)
				if err != nil {
					return err
				}
				if key == "min" {
					minLength = n
				}
				if key == "max" {
					maxLength = n
				}
			}
			if property.MinLength != minLength || property.MaxLength != maxLength {
				return fmt.Errorf("%s length contract differs from binding", name)
			}
		}
		if slices.Contains(schema.Required, name) != required {
			return fmt.Errorf("%s required contract differs", name)
		}
	}
	if fields != len(schema.Properties) {
		return fmt.Errorf("schema has fields absent from DTO")
	}
	return nil
}

func TestOpenAPIFieldContracts(t *testing.T) {
	schemas := readFieldContract(t)
	for _, tc := range []struct {
		name    string
		value   any
		request bool
	}{
		{"RegisterRequest", dto.RegisterRequest{}, true}, {"LoginRequest", dto.LoginRequest{}, true},
		{"RefreshRequest", dto.RefreshRequest{}, true}, {"LogoutRequest", dto.LogoutRequest{}, true},
		{"Token", dto.TokenResponse{}, false}, {"Envelope", resp.ResResult{}, false},
		{"Liveness", router.LivenessResponse{}, false}, {"DependencyCheck", router.DependencyCheck{}, false},
		{"Readiness", router.ReadinessResponse{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkDTOFields(schemas[tc.name], reflect.TypeOf(tc.value), tc.request); err != nil {
				t.Fatal(err)
			}
		})
	}
	// A changed field bound must fail the same checker used above.
	changed := schemas["LoginRequest"]
	changed.Properties["phone"] = contractSchema{Type: "string", MinLength: 1, MaxLength: 21}
	if err := checkDTOFields(changed, reflect.TypeFor[dto.LoginRequest](), true); err == nil {
		t.Fatal("contract mutation went unnoticed")
	}
}

func TestOpenAPIHTTPErrorAndSecurityContracts(t *testing.T) {
	engine := openAPIEngine(t, config.AuthModeLocal)
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]json.RawMessage `json:"responses"`
			Security  []map[string][]string      `json:"security"`
			MaxBody   int                        `json:"x-max-body-bytes"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, body, content string
		status              int
	}{
		{"/api/v1/auth/register", `{}`, "application/json", 422},
		{"/api/v1/auth/login", `{`, "application/json", 422},
		{"/api/v1/auth/refresh", `{}`, "text/plain", 415},
		{"/api/v1/auth/login", strings.Repeat("x", 8193), "application/json", 413},
		{"/api/v1/auth/logout", `{}`, "application/json", 401},
	} {
		request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		request.Header.Set("Content-Type", tc.content)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("%s: HTTP %d, want %d", tc.path, response.Code, tc.status)
		}
		op := doc.Paths[tc.path]["post"]
		if _, ok := op.Responses[strconv.Itoa(response.Code)]; !ok {
			t.Fatalf("undocumented response: %s %d", tc.path, response.Code)
		}
		if op.MaxBody != 8192 {
			t.Fatal("request body limit differs")
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"code", "msg", "data", "request_id"} {
			if _, ok := envelope[key]; !ok {
				t.Fatalf("missing envelope %s", key)
			}
		}
		if tc.path == "/api/v1/auth/logout" && len(op.Security) == 0 {
			t.Fatal("logout security is undocumented")
		}
		if tc.path != "/api/v1/auth/logout" && len(op.Security) != 0 {
			t.Fatal("public auth operation marked private")
		}
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != 200 || strings.TrimSpace(response.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("liveness contract: %d %s", response.Code, response.Body)
	}
}

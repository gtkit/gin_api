//go:build scaffold

package projectinit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// RenderClient generates the project's supported OpenAPI profile, not arbitrary JSON Schema.
func RenderClient(raw []byte) ([]byte, error) {
	var spec struct {
		OpenAPI    string                                `json:"openapi"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	if spec.OpenAPI != "3.1.0" || len(spec.Paths) == 0 || len(spec.Components.Schemas) == 0 {
		return nil, errors.New("expected nonempty OpenAPI 3.1.0 contract")
	}
	runtime, err := moduleTemplates.ReadFile("templates/client/runtime.tmpl")
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(runtime)
	names := make([]string, 0, len(spec.Components.Schemas))
	for name := range spec.Components.Schemas {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := validateClientSchema(spec.Components.Schemas[name]); err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
		if name == "Envelope" || name == "TokenEnvelope" || name == "ErrorEnvelope" {
			continue
		}
		if !token.IsIdentifier(name) || token.Lookup(name).IsKeyword() {
			return nil, fmt.Errorf("invalid schema name %q", name)
		}
		var schema clientSchema
		if err := json.Unmarshal(spec.Components.Schemas[name], &schema); err != nil {
			return nil, err
		}
		if schema.Type != "object" || schema.Properties == nil {
			return nil, fmt.Errorf("schema %s: only named objects supported", name)
		}
		fmt.Fprintf(&out, "\ntype %s struct {\n", name)
		fields := make([]string, 0, len(schema.Properties))
		for field := range schema.Properties {
			fields = append(fields, field)
		}
		slices.Sort(fields)
		for _, field := range fields {
			typ, err := clientFieldType(schema.Properties[field], spec.Components.Schemas)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", name, field, err)
			}
			goName := strings.ReplaceAll(displayNameFrom(field), " ", "")
			if !token.IsIdentifier(goName) || goName == "" {
				return nil, fmt.Errorf("invalid field %q", field)
			}
			tag := field
			if !slices.Contains(schema.Required, field) {
				tag += ",omitempty"
			}
			fmt.Fprintf(&out, "%s %s `json:%s`\n", goName, typ, strconv.Quote(tag))
		}
		out.WriteString("}\n")
	}
	paths := make([]string, 0, len(spec.Paths))
	for path := range spec.Paths {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	seen := map[string]bool{}
	for _, path := range paths {
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "{}?#\r\n") {
			return nil, fmt.Errorf("unsupported path %q", path)
		}
		methods := make([]string, 0, len(spec.Paths[path]))
		for method := range spec.Paths[path] {
			methods = append(methods, method)
		}
		slices.Sort(methods)
		for _, method := range methods {
			if method != "get" && method != "post" {
				return nil, fmt.Errorf("unsupported operation %s %s", method, path)
			}
			var op struct {
				ID         string            `json:"operationId"`
				Parameters []json.RawMessage `json:"parameters"`
				Body       struct {
					Content map[string]struct {
						Schema clientSchema `json:"schema"`
					} `json:"content"`
				} `json:"requestBody"`
				Responses map[string]struct {
					Content map[string]struct {
						Schema clientSchema `json:"schema"`
					} `json:"content"`
				} `json:"responses"`
				Security []map[string][]string `json:"security"`
			}
			if err := json.Unmarshal(spec.Paths[path][method], &op); err != nil {
				return nil, err
			}
			name := strings.ReplaceAll(displayNameFrom(op.ID), " ", "")
			if name == "" || !token.IsIdentifier(name) || seen[name] || len(op.Parameters) > 0 {
				return nil, fmt.Errorf("unsupported or duplicate operation %q", op.ID)
			}
			seen[name] = true
			result := op.Responses["200"].Content["application/json"].Schema
			if len(result.OneOf) > 0 {
				if len(result.OneOf) != 2 || result.OneOf[0].Ref != "#/components/schemas/TokenEnvelope" || result.OneOf[1].Ref != "#/components/schemas/ErrorEnvelope" {
					return nil, errors.New("unsupported response alternatives")
				}
				result = result.OneOf[0]
			}
			resultType, err := clientFieldType(result, spec.Components.Schemas)
			if err != nil {
				return nil, err
			}
			wrapped := strings.HasPrefix(resultType, "Envelope[")
			params := "ctx context.Context"
			body := "nil"
			auth := "\"\""
			if len(op.Security) > 0 {
				params += ", accessToken string"
				auth = "accessToken"
			}
			if len(op.Body.Content) > 0 {
				input, err := clientFieldType(op.Body.Content["application/json"].Schema, spec.Components.Schemas)
				if err != nil {
					return nil, err
				}
				params += ", request " + input
				body = "request"
			}
			fmt.Fprintf(&out, "\nfunc (c *Client) %s(%s) (%s, error) {\nvar result %s\nerr := c.do(ctx, %q, %q, %s, %s, &result, %t)\nreturn result, err\n}\n", name, params, resultType, resultType, strings.ToUpper(method), path, body, auth, wrapped)
		}
	}
	return format.Source(out.Bytes())
}

func validateClientSchema(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("schema must be an object")
	}
	for key, value := range fields {
		switch key {
		case "$ref", "type", "required", "minLength", "maxLength", "format", "description", "writeOnly", "const", "enum", "maxItems":
		case "properties":
			var properties map[string]json.RawMessage
			if err := json.Unmarshal(value, &properties); err != nil {
				return err
			}
			for _, property := range properties {
				if err := validateClientSchema(property); err != nil {
					return err
				}
			}
		case "items", "additionalProperties", "not":
			if err := validateClientSchema(value); err != nil {
				return err
			}
		case "allOf", "oneOf":
			var alternatives []json.RawMessage
			if err := json.Unmarshal(value, &alternatives); err != nil {
				return err
			}
			for _, alternative := range alternatives {
				if err := validateClientSchema(alternative); err != nil {
					return err
				}
			}
		default:
			if !strings.HasPrefix(key, "x-") {
				return fmt.Errorf("unsupported schema keyword %q", key)
			}
		}
	}
	return nil
}

type clientSchema struct {
	Ref        string                  `json:"$ref"`
	Type       string                  `json:"type"`
	Properties map[string]clientSchema `json:"properties"`
	Required   []string                `json:"required"`
	Items      *clientSchema           `json:"items"`
	Additional *clientSchema           `json:"additionalProperties"`
	OneOf      []clientSchema          `json:"oneOf"`
}

func clientFieldType(s clientSchema, schemas map[string]json.RawMessage) (string, error) {
	if s.Ref != "" {
		const prefix = "#/components/schemas/"
		name := strings.TrimPrefix(s.Ref, prefix)
		if !strings.HasPrefix(s.Ref, prefix) || schemas[name] == nil {
			return "", fmt.Errorf("unresolved schema %q", s.Ref)
		}
		switch name {
		case "Envelope":
			return "Envelope[json.RawMessage]", nil
		case "TokenEnvelope":
			return "Envelope[Token]", nil
		case "ErrorEnvelope":
			return "Envelope[json.RawMessage]", nil
		}
		return name, nil
	}
	switch s.Type {
	case "string":
		return "string", nil
	case "integer":
		return "int64", nil
	case "boolean":
		return "bool", nil
	case "number":
		return "float64", nil
	case "array":
		if s.Items != nil {
			typ, err := clientFieldType(*s.Items, schemas)
			return "[]" + typ, err
		}
	case "object":
		if s.Additional != nil {
			typ, err := clientFieldType(*s.Additional, schemas)
			return "map[string]" + typ, err
		}
	}
	return "", fmt.Errorf("unsupported schema type %q", s.Type)
}

// GenerateClient writes a new client, or checks the existing generated file without changing it.
func GenerateClient(start string, check bool) (string, error) {
	root, err := findProjectRoot(start)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(root, "docs", "openapi.json"))
	if err != nil {
		return "", err
	}
	generated, err := RenderClient(raw)
	if err != nil {
		return "", err
	}
	const path = "internal/pkg/apiclient/client.gen.go"
	if check {
		existing, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return "", err
		}
		if !bytes.Equal(existing, generated) {
			return "", errors.New("generated client differs from OpenAPI; review and regenerate")
		}
		return path, nil
	}
	_, err = WriteGenerated(root, map[string][]byte{path: generated}, false)
	return path, err
}

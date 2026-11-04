//go:build scaffold

package projectinit

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
)

//go:embed templates/module/*
//go:embed templates/client/*
var moduleTemplates embed.FS

// RenderModule renders a complete unregistered module without filesystem writes.
func RenderModule(modulePath, name string) (map[string][]byte, error) {
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,29}$`).MatchString(name) || token.Lookup(name).IsKeyword() {
		return nil, errors.New("module name must be a non-keyword lowercase Go identifier, at most 30 characters")
	}
	if modulePath == "" || strings.ContainsAny(modulePath, "\"\n\r\t ") {
		return nil, errors.New("invalid Go module path")
	}
	data := struct{ Module, Name, Type string }{modulePath, name, displayNameFrom(name)}
	data.Type = strings.ReplaceAll(data.Type, " ", "")
	targets := map[string]string{
		"model":        "internal/models/" + name + ".go",
		"service":      "internal/module/" + name + "/application/service.go",
		"service_test": "internal/module/" + name + "/application/service_test.go",
		"dto":          "internal/module/" + name + "/dto/record.go",
		"handler":      "internal/module/" + name + "/transport/http/router.go",
		"handler_test": "internal/module/" + name + "/transport/http/router_test.go",
		"repository":   "internal/repository/" + name + "/repository.go",
		"runtime":      "internal/runtime/module/" + name + "/module.go",
		"runtime_test": "internal/runtime/module/" + name + "/module_test.go",
		"schema":       "database/ddl/create_" + name + "_records_table.sql",
	}
	files := make(map[string][]byte, len(targets))
	for source, target := range targets {
		raw, err := moduleTemplates.ReadFile("templates/module/" + source + ".tmpl")
		if err != nil {
			return nil, err
		}
		tmpl, err := template.New(source).Parse(string(raw))
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		if err := tmpl.Execute(&out, data); err != nil {
			return nil, err
		}
		body := out.Bytes()
		if strings.HasSuffix(target, ".go") {
			body, err = format.Source(body)
			if err != nil {
				return nil, fmt.Errorf("format %s: %w", target, err)
			}
		}
		files[target] = body
	}
	return files, nil
}

// GenerateModule creates a module in this project, refusing any existing target.
func GenerateModule(start, name string, dryRun bool) ([]string, error) {
	root, err := findProjectRoot(start)
	if err != nil {
		return nil, err
	}
	module, err := readModulePath(root)
	if err != nil {
		return nil, err
	}
	files, err := RenderModule(module, name)
	if err != nil {
		return nil, err
	}
	return WriteGenerated(root, files, dryRun)
}

// WriteGenerated validates every target before writing and rolls back owned files on failure.
// Paths are project-relative; os.Root prevents escaping through parent symlinks.
func WriteGenerated(root string, files map[string][]byte, dryRun bool) (paths []string, err error) {
	fs, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer fs.Close()
	for path := range files {
		if !filepath.IsLocal(path) || filepath.Clean(path) != path || path == "." {
			return nil, fmt.Errorf("target must be project-relative: %q", path)
		}
		for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
			info, statErr := fs.Lstat(parent)
			if statErr == nil && !info.IsDir() {
				return nil, fmt.Errorf("target parent is not a directory: %s", parent)
			}
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return nil, fmt.Errorf("inspect target parent: %w", statErr)
			}
		}
		if _, statErr := fs.Lstat(path); statErr == nil {
			return nil, fmt.Errorf("refusing existing target: %s", path)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect %s: %w", path, statErr)
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	if dryRun {
		return paths, nil
	}
	var created []string
	defer func() {
		if err == nil {
			return
		}
		for _, path := range created {
			err = errors.Join(err, fs.Remove(path))
		}
	}()
	for _, path := range paths {
		if err = fs.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, err
		}
		var file *os.File
		file, err = fs.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return nil, err
		}
		created = append(created, path)
		_, writeErr := io.Copy(file, bytes.NewReader(files[path]))
		err = errors.Join(writeErr, file.Close())
		if err != nil {
			return nil, err
		}
	}
	return paths, nil
}

package scaffold

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"

	"golang.org/x/tools/imports"
)

// importsMu 串行化对 imports.LocalPrefix（x/tools 包级全局变量）的写入与
// imports.Process 调用，避免并发生成（如并行测试）时对该全局的数据竞争。
var importsMu sync.Mutex

// formatWithImports 用 goimports 引擎格式化 Go 源码：按 local-prefix=模块路径
// 分组 import（FormatOnly 只重排、不增删），与 golangci-lint 的 goimports 一致。
func formatWithImports(path, modulePath string, src []byte) ([]byte, error) {
	importsMu.Lock()
	defer importsMu.Unlock()

	imports.LocalPrefix = modulePath
	return imports.Process(path, src, &imports.Options{
		Comments:   true,
		TabIndent:  true,
		TabWidth:   8,
		FormatOnly: true,
	})
}

var moduleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var fieldTypePattern = regexp.MustCompile(`^(string|bool|int|int8|int16|int32|int64|uint|uint8|uint16|uint32|uint64|float32|float64|time\.Time|\*time\.Time)$`)
var reservedFieldNames = map[string]struct{}{
	"id":         {},
	"created_at": {},
	"updated_at": {},
	"deleted_at": {},
}

type ModuleGenerator struct {
	root       string
	modulePath string
	now        func() time.Time
}

type GenerateOptions struct {
	Force     bool
	DryRun    bool
	Bootstrap bool
	Fields    []string
}

type GenerateResult struct {
	DryRun  bool
	Created []string
	Updated []string
	Skipped []string
}

type moduleField struct {
	ColumnName  string
	FieldName   string
	Type        string
	ModelTag    string
	ResponseTag string
}

type moduleTemplateData struct {
	ProjectModulePath string
	PackageName       string
	StructName        string
	RoutePath         string
	TableName         string
	MigrationName     string
	AppAlias          string
	HTTPAlias         string
	RepoAlias         string
	RuntimeAlias      string
	ServiceVar        string
	Fields            []moduleField
	NeedsModelTime    bool
}

type generatedFile struct {
	path           string
	template       string
	allowOverwrite bool
	formatGo       bool
}

func NewModuleGenerator(startDir string) (*ModuleGenerator, error) {
	if strings.TrimSpace(startDir) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
		startDir = wd
	}

	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	modulePath, err := readGoModulePath(root)
	if err != nil {
		return nil, err
	}

	return &ModuleGenerator{root: root, modulePath: modulePath, now: time.Now}, nil
}

func (g *ModuleGenerator) Generate(rawName string, force bool) (*GenerateResult, error) {
	return g.GenerateWithOptions(rawName, GenerateOptions{Force: force, Bootstrap: true})
}

func (g *ModuleGenerator) GenerateWithOptions(rawName string, opts GenerateOptions) (*GenerateResult, error) {
	if g == nil {
		return nil, fmt.Errorf("module generator is nil")
	}

	data, err := newModuleTemplateData(rawName, opts.Fields, g.modulePath)
	if err != nil {
		return nil, err
	}

	result := &GenerateResult{}
	result.DryRun = opts.DryRun
	files := g.generatedFiles(data)
	migrationFiles, err := g.generatedMigrationFiles(data)
	if err != nil {
		return nil, err
	}
	files = append(files, migrationFiles...)

	for _, file := range files {
		content, err := renderTemplate(file.template, data, file.formatGo)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", g.rel(file.path), err)
		}

		if err := g.writeManagedFile(file, content, opts.Force, opts.DryRun, result); err != nil {
			return nil, err
		}
	}

	if opts.Bootstrap {
		if err := g.updateProviders(data, opts.DryRun, result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (g *ModuleGenerator) generatedFiles(data moduleTemplateData) []generatedFile {
	moduleDir := filepath.Join(g.root, "internal", "module", data.PackageName)
	repoDir := filepath.Join(g.root, "internal", "repository", data.PackageName)

	return []generatedFile{
		{
			path:           filepath.Join(moduleDir, "application", "ports.go"),
			template:       applicationPortsTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(moduleDir, "application", "service.go"),
			template:       applicationServiceTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(moduleDir, "application", "service_test.go"),
			template:       applicationServiceTestTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(moduleDir, "transport", "http", "router.go"),
			template:       transportRouterTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(moduleDir, "transport", "http", "handler.go"),
			template:       transportHandlerTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(moduleDir, "transport", "http", "handler_test.go"),
			template:       transportHandlerTestTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(repoDir, "repository.go"),
			template:       repositoryTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(repoDir, "repository_util.go"),
			template:       repositoryUtilTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(moduleDir, "dto", data.PackageName+".go"),
			template:       dtoTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(g.root, "internal", "runtime", "module", data.PackageName, "module.go"),
			template:       runtimeModuleTemplate,
			allowOverwrite: true,
			formatGo:       true,
		},
		{
			path:           filepath.Join(g.root, "internal", "models", data.PackageName+".go"),
			template:       modelTemplate,
			allowOverwrite: false,
			formatGo:       true,
		},
	}
}

func (g *ModuleGenerator) generatedMigrationFiles(data moduleTemplateData) ([]generatedFile, error) {
	migrationsDir := filepath.Join(g.root, "database", "migrations")
	pattern := filepath.Join(migrationsDir, "*_create_"+data.TableName+"_table.up.sql")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob migration file: %w", err)
	}

	var basePath string
	if len(matches) > 0 {
		slices.Sort(matches)
		basePath = strings.TrimSuffix(matches[len(matches)-1], ".up.sql")
	} else {
		now := time.Now
		if g.now != nil {
			now = g.now
		}

		fileName := now().Format("2006_01_02_150405") + "_create_" + data.TableName + "_table"
		basePath = filepath.Join(migrationsDir, fileName)
	}

	return []generatedFile{
		{
			path:           basePath + ".up.sql",
			template:       migrationUpTemplate,
			allowOverwrite: true,
			formatGo:       false,
		},
		{
			path:           basePath + ".down.sql",
			template:       migrationDownTemplate,
			allowOverwrite: true,
			formatGo:       false,
		},
	}, nil
}

func (g *ModuleGenerator) updateProviders(data moduleTemplateData, dryRun bool, result *GenerateResult) error {
	providersPath := filepath.Join(g.root, "internal", "bootstrap", "providers.go")
	content, err := os.ReadFile(providersPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", g.rel(providersPath), err)
	}

	updated := string(content)
	changed := false

	// 校验型标记先于任何注入动作断言：缺失即中止，天然不会写出半改写的 providers.go。
	for _, marker := range providersValidateOnlyMarkers() {
		if !strings.Contains(updated, marker) {
			return fmt.Errorf("validate providers marker: anchor %q not found", strings.TrimSpace(marker))
		}
	}

	appImport := fmt.Sprintf("\t%s \"%s/internal/module/%s/application\"\n", data.AppAlias, data.ProjectModulePath, data.PackageName)
	runtimeImport := fmt.Sprintf("\t%s \"%s/internal/runtime/module/%s\"\n", data.RuntimeAlias, data.ProjectModulePath, data.PackageName)
	httpImport := fmt.Sprintf("\t%s \"%s/internal/module/%s/transport/http\"\n", data.HTTPAlias, data.ProjectModulePath, data.PackageName)
	fieldLine := fmt.Sprintf("\t%s *%s.Service\n", data.ServiceVar, data.AppAlias)
	serviceLine := fmt.Sprintf("\tsvc.%s = %s.NewService(inf.mysqlDB)\n", data.ServiceVar, data.RuntimeAlias)
	moduleLine := fmt.Sprintf("\t\t%s.NewModule(svc.%s),\n", data.HTTPAlias, data.ServiceVar)

	importsMarker, serviceFieldsMarker, servicesMarker, modulesMarker := providersMarkers()

	updated, changedImport, err := ensureLineBefore(updated, appImport, importsMarker)
	if err != nil {
		return fmt.Errorf("update providers application import: %w", err)
	}
	changed = changed || changedImport

	updated, changedImport, err = ensureLineBefore(updated, runtimeImport, importsMarker)
	if err != nil {
		return fmt.Errorf("update providers runtime import: %w", err)
	}
	changed = changed || changedImport

	updated, changedImport, err = ensureLineBefore(updated, httpImport, importsMarker)
	if err != nil {
		return fmt.Errorf("update providers http import: %w", err)
	}
	changed = changed || changedImport

	updated, changedField, err := ensureLineBefore(updated, fieldLine, serviceFieldsMarker)
	if err != nil {
		return fmt.Errorf("update providers service field: %w", err)
	}
	changed = changed || changedField

	updated, changedService, err := ensureLineBefore(updated, serviceLine, servicesMarker)
	if err != nil {
		return fmt.Errorf("update providers service init: %w", err)
	}
	changed = changed || changedService

	updated, changedModule, err := ensureLineBefore(updated, moduleLine, modulesMarker)
	if err != nil {
		return fmt.Errorf("update providers module list: %w", err)
	}
	changed = changed || changedModule

	if !changed {
		result.Skipped = append(result.Skipped, g.rel(providersPath))
		return nil
	}

	if dryRun {
		result.Updated = append(result.Updated, g.rel(providersPath))
		return nil
	}

	// 用 goimports 格式化注入结果：把新增的本项目 import 归入正确分组（local-prefix=模块路径），
	// 避免 marker 前插入 + gofmt 不跨组重排导致的「独立 import 组」不过 goimports。
	formatted, err := formatWithImports(providersPath, g.modulePath, []byte(updated))
	if err != nil {
		return fmt.Errorf("format %s: %w", g.rel(providersPath), err)
	}

	if err := os.WriteFile(providersPath, formatted, 0o644); err != nil { //nolint:gosec // generated Go source files are expected to be user-readable.
		return fmt.Errorf("write %s: %w", g.rel(providersPath), err)
	}

	result.Updated = append(result.Updated, g.rel(providersPath))
	return nil
}

// providersMarkers 返回 updateProviders 使用的「注入型」稳定标记——生成器在每个
// 标记前插入新模块的接线行。标记归脚手架所有，不依赖任何业务模块长期存在。
// 对应 providers.go 的分阶段结构：import 区、services struct 字段、
// buildServices 构造、buildEngine 模块切片四处注入点。
//
// 另有「校验型」标记见 providersValidateOnlyMarkers——两类标记处理方式不同，
// 不要把校验型标记接进本函数或 ensureLineBefore 的插入逻辑。
func providersMarkers() (imports, serviceFields, services, modules string) {
	return "\t// scaffold:module-imports\n",
		"\t// scaffold:module-service-fields\n",
		"\t// scaffold:module-services\n",
		"\t\t// scaffold:modules\n"
}

// providersValidateOnlyMarkers 返回「校验型」标记——生成器只断言其存在，不插入内容。
//
// scaffold:module-workers 位于 buildWorkers 的 worker 集合内，是后台任务的注册锚点。
// 它不参与注入：生成器不产出 worker 源码文件，且绝大多数模块不需要后台任务，
// 自动注入只会给每个新模块塞进一个无实现的注册行或空转 ticker。
// 但它仍须被生成器认领并校验，否则会退化成无人负责的裸注释——母版的五阶段结构
// 被破坏时不会有任何信号，违反 scaffold-mother-integrity 的标记完整性要求。
func providersValidateOnlyMarkers() []string {
	return []string{"\t// scaffold:module-workers\n"}
}

func (g *ModuleGenerator) writeManagedFile(file generatedFile, content []byte, force, dryRun bool, result *GenerateResult) error {
	relPath := g.rel(file.path)
	existing, err := os.ReadFile(file.path)
	if err == nil {
		if bytes.Equal(existing, content) {
			result.Skipped = append(result.Skipped, relPath)
			return nil
		}
		if !force || !file.allowOverwrite {
			result.Skipped = append(result.Skipped, relPath)
			return nil
		}
		if dryRun {
			result.Updated = append(result.Updated, relPath)
			return nil
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", relPath, err)
	} else if dryRun {
		result.Created = append(result.Created, relPath)
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(relPath), err)
	}
	if err := os.WriteFile(file.path, content, 0o644); err != nil { //nolint:gosec // generated project files are expected to be user-readable.
		return fmt.Errorf("write %s: %w", relPath, err)
	}

	if err == nil {
		result.Updated = append(result.Updated, relPath)
		return nil
	}

	result.Created = append(result.Created, relPath)
	return nil
}

func (g *ModuleGenerator) rel(path string) string {
	relPath, err := filepath.Rel(g.root, path)
	if err != nil {
		return filepath.Clean(path)
	}

	return filepath.ToSlash(relPath)
}

func readGoModulePath(root string) (string, error) {
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}

		modulePath := strings.TrimSpace(strings.TrimPrefix(line, "module "))
		if modulePath != "" {
			return modulePath, nil
		}
	}

	return "", fmt.Errorf("module path not found in go.mod")
}

func findProjectRoot(startDir string) (string, error) {
	current, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve project root from %q: %w", startDir, err)
	}

	for {
		if fileExists(filepath.Join(current, "go.mod")) &&
			fileExists(filepath.Join(current, "internal", "bootstrap", "providers.go")) {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("project root not found from %q", startDir)
		}

		current = parent
	}
}

func newModuleTemplateData(rawName string, fieldSpecs []string, projectModulePath string) (moduleTemplateData, error) {
	normalized := normalizeModuleName(rawName)
	if !moduleNamePattern.MatchString(normalized) {
		return moduleTemplateData{}, fmt.Errorf("invalid module name %q: use lowercase letters, digits, hyphen or underscore, and start with a letter", rawName)
	}

	words := splitWords(normalized)
	structName := toPascalCase(words)
	aliasBase := strings.Join(words, "")
	fields, err := parseFieldSpecs(fieldSpecs)
	if err != nil {
		return moduleTemplateData{}, err
	}

	return moduleTemplateData{
		ProjectModulePath: projectModulePath,
		PackageName:       strings.Join(words, "_"),
		StructName:        structName,
		RoutePath:         strings.Join(words, "-"),
		TableName:         pluralizeSnake(words),
		MigrationName:     "create_" + pluralizeSnake(words) + "_table",
		AppAlias:          aliasBase + "app",
		HTTPAlias:         aliasBase + "http",
		RepoAlias:         aliasBase + "repo",
		RuntimeAlias:      aliasBase + "runtime",
		ServiceVar:        lowerFirst(structName) + "Service",
		Fields:            fields,
		NeedsModelTime:    needsModelTime(fields),
	}, nil
}

func parseFieldSpecs(fieldSpecs []string) ([]moduleField, error) {
	if len(fieldSpecs) == 0 {
		return []moduleField{buildModuleField("name", "string")}, nil
	}

	fields := make([]moduleField, 0, len(fieldSpecs))
	seen := make(map[string]struct{}, len(fieldSpecs))

	for _, raw := range fieldSpecs {
		namePart, typePart, ok := strings.Cut(strings.TrimSpace(raw), ":")
		if !ok {
			return nil, fmt.Errorf("invalid field %q: use name:type", raw)
		}

		name := normalizeModuleName(namePart)
		fieldType := strings.TrimSpace(typePart)
		if !moduleNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid field name %q: use lowercase letters, digits, hyphen or underscore, and start with a letter", namePart)
		}
		if !fieldTypePattern.MatchString(fieldType) {
			return nil, fmt.Errorf("invalid field type %q for %s", fieldType, name)
		}
		if _, reserved := reservedFieldNames[name]; reserved {
			return nil, fmt.Errorf("field name %q is reserved by the scaffold base model", name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate field %q", name)
		}

		seen[name] = struct{}{}
		fields = append(fields, buildModuleField(name, fieldType))
	}

	return fields, nil
}

func buildModuleField(name, fieldType string) moduleField {
	words := splitWords(name)
	if len(words) == 0 {
		words = []string{name}
	}

	return moduleField{
		ColumnName:  strings.Join(words, "_"),
		FieldName:   toPascalCase(words),
		Type:        fieldType,
		ModelTag:    buildModelTag(strings.Join(words, "_"), fieldType),
		ResponseTag: fmt.Sprintf("json:\"%s\"", strings.Join(words, "_")),
	}
}

func buildModelTag(columnName, fieldType string) string {
	switch fieldType {
	case "string":
		return fmt.Sprintf("gorm:\"column:%s;size:255;not null;default:'';comment:%s\" json:\"%s\"", columnName, columnName, columnName)
	case "bool":
		return fmt.Sprintf("gorm:\"column:%s;not null;default:false;comment:%s\" json:\"%s\"", columnName, columnName, columnName)
	case "time.Time", "*time.Time":
		return fmt.Sprintf("gorm:\"column:%s;type:timestamp;comment:%s\" json:\"%s\"", columnName, columnName, columnName)
	default:
		return fmt.Sprintf("gorm:\"column:%s;not null;default:0;comment:%s\" json:\"%s\"", columnName, columnName, columnName)
	}
}

func needsModelTime(fields []moduleField) bool {
	for _, field := range fields {
		if field.Type == "time.Time" || field.Type == "*time.Time" {
			return true
		}
	}

	return false
}

func normalizeModuleName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "-", "_")
	return strings.Trim(name, "_")
}

func splitWords(name string) []string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_'
	})

	words := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		words = append(words, part)
	}

	return words
}

func toPascalCase(words []string) string {
	var builder strings.Builder
	for _, word := range words {
		builder.WriteString(strings.ToUpper(word[:1]))
		builder.WriteString(word[1:])
	}
	return builder.String()
}

func lowerFirst(v string) string {
	if v == "" {
		return ""
	}

	return strings.ToLower(v[:1]) + v[1:]
}

func pluralizeSnake(words []string) string {
	if len(words) == 0 {
		return ""
	}

	parts := append([]string(nil), words...)
	last := parts[len(parts)-1]
	parts[len(parts)-1] = pluralizeWord(last)
	return strings.Join(parts, "_")
}

func pluralizeWord(word string) string {
	switch {
	case strings.HasSuffix(word, "ch"), strings.HasSuffix(word, "sh"),
		strings.HasSuffix(word, "s"), strings.HasSuffix(word, "x"), strings.HasSuffix(word, "z"):
		return word + "es"
	case len(word) > 1 && strings.HasSuffix(word, "y") && !isVowel(word[len(word)-2]):
		return word[:len(word)-1] + "ies"
	default:
		return word + "s"
	}
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	default:
		return false
	}
}

func ensureLineBefore(content, line, anchor string) (string, bool, error) {
	if strings.Contains(content, line) {
		return content, false, nil
	}

	index := strings.Index(content, anchor)
	if index < 0 {
		return "", false, fmt.Errorf("anchor %q not found", strings.TrimSpace(anchor))
	}

	return content[:index] + line + content[index:], true, nil
}

func renderTemplate(source string, data moduleTemplateData, formatGoCode bool) ([]byte, error) {
	tpl, err := template.New("module").Funcs(template.FuncMap{
		"sqlColumn": sqlColumnDefinition,
	}).Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse module template: %w", err)
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute module template: %w", err)
	}

	if !formatGoCode {
		return buf.Bytes(), nil
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated Go source: %w", err)
	}

	return formatted, nil
}

func sqlColumnDefinition(fieldType string) string {
	switch fieldType {
	case "string":
		return "varchar(255) NOT NULL DEFAULT ''"
	case "bool":
		return "tinyint(1) NOT NULL DEFAULT 0"
	case "int8", "uint8":
		return "tinyint NOT NULL DEFAULT 0"
	case "int16", "uint16":
		return "smallint NOT NULL DEFAULT 0"
	case "int32", "uint32":
		return "int NOT NULL DEFAULT 0"
	case "int64", "uint64", "int", "uint":
		return "bigint NOT NULL DEFAULT 0"
	case "float32":
		return "float NOT NULL DEFAULT 0"
	case "float64":
		return "double NOT NULL DEFAULT 0"
	case "time.Time":
		return "timestamp NOT NULL"
	case "*time.Time":
		return "timestamp NULL DEFAULT NULL"
	default:
		return "varchar(255) NOT NULL DEFAULT ''"
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

const modelTemplate = `package models

import (
	"strconv"
{{- if .NeedsModelTime }}
	"time"
{{- end }}

	"github.com/gtkit/json"
)

type {{.StructName}} struct {
	BaseID

{{- range .Fields }}
	{{.FieldName}} {{.Type}} ` + "`{{.ModelTag}}`" + `
{{- end }}

	BaseTimeField
}

func (*{{.StructName}}) TableName() string {
	return "{{.TableName}}"
}

func (m *{{.StructName}}) GetStringID() string {
	return strconv.FormatInt(m.ID, 10)
}

func (m *{{.StructName}}) MarshalBinary() ([]byte, error) {
	return json.Marshal(m)
}

func (m *{{.StructName}}) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, m)
}
`

const repositoryTemplate = `package {{.PackageName}}

import (
	"context"

	"{{.ProjectModulePath}}/internal/repository/dbtx"

	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// mdbCtx 统一经 dbtx.DB 收口，使本 repo 的数据访问自动参与 TxManager 开启的事务。
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
	return dbtx.DB(ctx, r.db)
}
`

const repositoryUtilTemplate = `package {{.PackageName}}

import (
	"context"
	"errors"
	"fmt"

	"{{.ProjectModulePath}}/internal/models"

	"gorm.io/gorm"
)

// Get 按 ID 查询；未找到返回 found=false 且 err=nil，不把 gorm sentinel 泄漏给 application。
func (r *Repository) Get(ctx context.Context, id int64, fields ...string) (*models.{{.StructName}}, bool, error) {
	var entity models.{{.StructName}}

	query := r.mdbCtx(ctx)
	if len(fields) > 0 {
		query = query.Select(fields)
	}

	err := query.Where("id = ?", id).First(&entity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get {{.PackageName}} by id %d: %w", id, err)
	}

	return &entity, true, nil
}

func (r *Repository) List(ctx context.Context, offset, limit int, fields ...string) ([]*models.{{.StructName}}, int64, error) {
	var list []*models.{{.StructName}}
	var total int64

	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	baseQuery := r.mdbCtx(ctx).Model(&models.{{.StructName}}{})
	if err := baseQuery.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count {{.PackageName}} list: %w", err)
	}

	query := baseQuery.Session(&gorm.Session{})
	if len(fields) > 0 {
		query = query.Select(fields)
	}

	if err := query.Order("id ASC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("list {{.PackageName}}: %w", err)
	}

	return list, total, nil
}

// ExistsByID 判断指定主键的 {{.StructName}} 是否存在。
// 用强类型主键代替动态字段名，无需白名单即天然免于 SQL 注入。
func (r *Repository) ExistsByID(ctx context.Context, id int64) (bool, error) {
	var count int64
	if err := r.mdbCtx(ctx).Model(models.{{.StructName}}{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check {{.PackageName}} exists by id %d: %w", id, err)
	}
	return count > 0, nil
}
`

const applicationPortsTemplate = `package application

import (
	"context"

	"{{.ProjectModulePath}}/internal/models"
)

type {{.StructName}}Repository interface {
	Get(ctx context.Context, id int64, fields ...string) (*models.{{.StructName}}, bool, error)
	List(ctx context.Context, offset, limit int, fields ...string) ([]*models.{{.StructName}}, int64, error)
}
`

const applicationServiceTemplate = `package application

import (
	"context"
	"errors"
	"fmt"

	"{{.ProjectModulePath}}/internal/models"
	"{{.ProjectModulePath}}/internal/module/{{.PackageName}}/dto"
	"{{.ProjectModulePath}}/internal/pkg/paging"
)

const (
	defaultListPage     = 1
	defaultListPageSize = 20
	maxListPageSize     = 100
)

// Err{{.StructName}}NotFound 表示目标 {{.StructName}} 不存在（领域 error）；
// handler 据此返回 404，无需依赖 gorm sentinel。
var Err{{.StructName}}NotFound = errors.New("{{.PackageName}}: not found")

type Service struct {
	repo {{.StructName}}Repository
}

// NewWithDeps 创建 {{.StructName}} application service。
func NewWithDeps(repo {{.StructName}}Repository) *Service {
	return &Service{repo: repo}
}

// ListQuery 是与传输协议无关的分页查询参数。
type ListQuery struct {
	Page     int
	PageSize int
}

// List 返回有界分页列表。
func (s *Service) List(ctx context.Context, query ListQuery) (dto.{{.StructName}}ListResponse, error) {
	query = normalizeListQuery(query)
	offset := (query.Page - 1) * query.PageSize
	list, total, err := s.repo.List(ctx, offset, query.PageSize)
	if err != nil {
		return dto.{{.StructName}}ListResponse{}, fmt.Errorf("list {{.PackageName}}: %w", err)
	}

	response := make([]dto.{{.StructName}}Response, 0, len(list))
	for _, item := range list {
		response = append(response, to{{.StructName}}Response(item))
	}

	totalPage := 0
	if total > 0 {
		totalPage = int((total + int64(query.PageSize) - 1) / int64(query.PageSize))
	}

	return dto.{{.StructName}}ListResponse{
		List: response,
		Paging: paging.Paging{
			CurrentPage: query.Page,
			PageSize:    query.PageSize,
			TotalPage:   totalPage,
			TotalCount:  total,
		},
	}, nil
}

// Get 按 ID 返回详情。
func (s *Service) Get(ctx context.Context, id int64) (dto.{{.StructName}}Response, error) {
	entity, found, err := s.repo.Get(ctx, id)
	if err != nil {
		return dto.{{.StructName}}Response{}, fmt.Errorf("get {{.PackageName}} %d: %w", id, err)
	}
	if !found {
		return dto.{{.StructName}}Response{}, Err{{.StructName}}NotFound
	}

	return to{{.StructName}}Response(entity), nil
}

func normalizeListQuery(query ListQuery) ListQuery {
	if query.Page <= 0 {
		query.Page = defaultListPage
	}
	if query.PageSize <= 0 {
		query.PageSize = defaultListPageSize
	}
	if query.PageSize > maxListPageSize {
		query.PageSize = maxListPageSize
	}
	return query
}

func to{{.StructName}}Response(entity *models.{{.StructName}}) dto.{{.StructName}}Response {
	if entity == nil {
		return dto.{{.StructName}}Response{}
	}

	return dto.{{.StructName}}Response{
		ID:        entity.ID,
		CreatedAt: entity.CreatedAt,
		UpdatedAt: entity.UpdatedAt,
{{- range .Fields }}
		{{.FieldName}}: entity.{{.FieldName}},
{{- end }}
	}
}
`

const applicationServiceTestTemplate = `package application

import (
	"context"
	"errors"
	"testing"

	"{{.ProjectModulePath}}/internal/models"
)

type fake{{.StructName}}Repository struct {
	entity     *models.{{.StructName}}
	found      bool
	list       []*models.{{.StructName}}
	total      int64
	err        error
	lastOffset int
	lastLimit  int
}

func (f *fake{{.StructName}}Repository) Get(context.Context, int64, ...string) (*models.{{.StructName}}, bool, error) {
	return f.entity, f.found, f.err
}

func (f *fake{{.StructName}}Repository) List(_ context.Context, offset, limit int, _ ...string) ([]*models.{{.StructName}}, int64, error) {
	f.lastOffset = offset
	f.lastLimit = limit
	return f.list, f.total, f.err
}

func TestServiceList(t *testing.T) {
	t.Parallel()

	repoErr := errors.New("repository unavailable")
	tests := []struct {
		name          string
		query         ListQuery
		repo          *fake{{.StructName}}Repository
		wantErr       error
		wantPage      int
		wantPageSize  int
		wantTotalPage int
		wantOffset    int
		wantLimit     int
	}{
		{
			name:         "default pagination",
			repo:         &fake{{.StructName}}Repository{},
			wantPage:     1,
			wantPageSize: 20,
			wantOffset:   0,
			wantLimit:    20,
		},
		{
			name:          "page size is capped",
			query:         ListQuery{Page: 2, PageSize: 500},
			repo:          &fake{{.StructName}}Repository{total: 250},
			wantPage:      2,
			wantPageSize:  100,
			wantTotalPage: 3,
			wantOffset:    100,
			wantLimit:     100,
		},
		{
			name:    "repository error",
			repo:    &fake{{.StructName}}Repository{err: repoErr},
			wantErr: repoErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewWithDeps(tt.repo).List(t.Context(), tt.query)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("List() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("List() unexpected error: %v", err)
			}
			if got.Paging.CurrentPage != tt.wantPage || got.Paging.PageSize != tt.wantPageSize {
				t.Fatalf("List() paging = (%d,%d), want (%d,%d)", got.Paging.CurrentPage, got.Paging.PageSize, tt.wantPage, tt.wantPageSize)
			}
			if got.Paging.TotalPage != tt.wantTotalPage {
				t.Fatalf("List() total page = %d, want %d", got.Paging.TotalPage, tt.wantTotalPage)
			}
			if tt.repo.lastOffset != tt.wantOffset || tt.repo.lastLimit != tt.wantLimit {
				t.Fatalf("repository pagination = (%d,%d), want (%d,%d)", tt.repo.lastOffset, tt.repo.lastLimit, tt.wantOffset, tt.wantLimit)
			}
		})
	}
}

func TestServiceGet(t *testing.T) {
	t.Parallel()

	repoErr := errors.New("repository unavailable")
	tests := []struct {
		name    string
		repo    *fake{{.StructName}}Repository
		wantID  int64
		wantErr error
	}{
		{
			name:   "success",
			repo:   &fake{{.StructName}}Repository{entity: &models.{{.StructName}}{BaseID: models.BaseID{ID: 7}}, found: true},
			wantID: 7,
		},
		{
			name:    "not found",
			repo:    &fake{{.StructName}}Repository{},
			wantErr: Err{{.StructName}}NotFound,
		},
		{
			name:    "repository error",
			repo:    &fake{{.StructName}}Repository{err: repoErr},
			wantErr: repoErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewWithDeps(tt.repo).Get(t.Context(), 7)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Get() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get() unexpected error: %v", err)
			}
			if got.ID != tt.wantID {
				t.Fatalf("Get() ID = %d, want %d", got.ID, tt.wantID)
			}
		})
	}
}
`

const runtimeModuleTemplate = `package {{.PackageName}}

import (
	{{.AppAlias}} "{{.ProjectModulePath}}/internal/module/{{.PackageName}}/application"
	{{.RepoAlias}} "{{.ProjectModulePath}}/internal/repository/{{.PackageName}}"

	"gorm.io/gorm"
)

// NewService 在 runtime 层装配 {{.PackageName}} service：db→repo→application service，
// 使 application 不依赖 gorm / repository。
func NewService(db *gorm.DB) *{{.AppAlias}}.Service {
	return {{.AppAlias}}.NewWithDeps({{.RepoAlias}}.New(db))
}
`

const transportRouterTemplate = `package http

import (
	{{.AppAlias}} "{{.ProjectModulePath}}/internal/module/{{.PackageName}}/application"

	"github.com/gin-gonic/gin"
)

type {{.StructName}}Module struct {
	service *{{.AppAlias}}.Service
}

func NewModule(service *{{.AppAlias}}.Service) *{{.StructName}}Module {
	return &{{.StructName}}Module{service: service}
}

func (m *{{.StructName}}Module) RegisterRoutes(router *gin.RouterGroup) {
	handler := NewHandler(m.service)
	group := router.Group("/{{.RoutePath}}")
	group.GET("", handler.List)
	group.GET("/:id", handler.Get)
}
`

const transportHandlerTemplate = `package http

import (
	"strconv"

	"{{.ProjectModulePath}}/internal/module/{{.PackageName}}/application"
	"{{.ProjectModulePath}}/internal/module/{{.PackageName}}/dto"
	resp "{{.ProjectModulePath}}/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
)

// errMapper 统一把 {{.PackageName}} 领域 error 映射为对客状态码，避免各 handler 手写错误码。
// 未匹配到的 error 由 resp.MapErr 归为 StatusInternalServer，不泄漏库层细节。
var errMapper = resp.NewErrMapper(
	resp.ErrMapping{Target: application.Err{{.StructName}}NotFound, Status: goerr.StatusNotFound(), Message: "{{.StructName}} not found"},
)

type Handler struct {
	service *application.Service
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) List(c *gin.Context) {
	var req dto.{{.StructName}}ListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	data, err := h.service.List(c.Request.Context(), application.ListQuery{
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}

	resp.Ok(c, data)
}

func (h *Handler) Get(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		resp.Err(c, goerr.New(err, goerr.StatusParams()))
		return
	}

	data, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}

	resp.Ok(c, data)
}
`

const transportHandlerTestTemplate = `package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"{{.ProjectModulePath}}/internal/models"
	"{{.ProjectModulePath}}/internal/module/{{.PackageName}}/application"

	"github.com/gin-gonic/gin"
)

type fake{{.StructName}}Repository struct {
	entity *models.{{.StructName}}
	found  bool
	err    error
}

func (f *fake{{.StructName}}Repository) Get(context.Context, int64, ...string) (*models.{{.StructName}}, bool, error) {
	return f.entity, f.found, f.err
}

func (f *fake{{.StructName}}Repository) List(context.Context, int, int, ...string) ([]*models.{{.StructName}}, int64, error) {
	return nil, 0, f.err
}

func TestHandlerList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repoErr := errors.New("repository unavailable")
	tests := []struct {
		name       string
		target     string
		repo       *fake{{.StructName}}Repository
		wantStatus int
	}{
		{name: "success", target: "/?page=1&page_size=20", repo: &fake{{.StructName}}Repository{}, wantStatus: http.StatusOK},
		{name: "invalid page size", target: "/?page_size=101", repo: &fake{{.StructName}}Repository{}, wantStatus: http.StatusUnprocessableEntity},
		{name: "repository error", target: "/", repo: &fake{{.StructName}}Repository{err: repoErr}, wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.target, nil)

			NewHandler(application.NewWithDeps(tt.repo)).List(ctx)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestHandlerGet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		id         string
		repo       *fake{{.StructName}}Repository
		wantStatus int
	}{
		{
			name:       "success",
			id:         "7",
			repo:       &fake{{.StructName}}Repository{entity: &models.{{.StructName}}{BaseID: models.BaseID{ID: 7}}, found: true},
			wantStatus: http.StatusOK,
		},
		{name: "not found", id: "7", repo: &fake{{.StructName}}Repository{}, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = append(ctx.Params, gin.Param{Key: "id", Value: tt.id})
			ctx.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/"+tt.id, nil)

			NewHandler(application.NewWithDeps(tt.repo)).Get(ctx)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
		})
	}
}
`

const dtoTemplate = `package dto

import (
	"time"

	"{{.ProjectModulePath}}/internal/pkg/paging"
)

type {{.StructName}}ListRequest struct {
	Page     int ` + "`form:\"page\" binding:\"omitempty,gte=1\"`" + `
	PageSize int ` + "`form:\"page_size\" binding:\"omitempty,gte=1,lte=100\"`" + `
}

type {{.StructName}}DetailRequest struct {
	ID int64 ` + "`json:\"id\" form:\"id\" binding:\"required,gte=1\"`" + `
}

type {{.StructName}}Response struct {
	ID        int64     ` + "`json:\"id\"`" + `
	CreatedAt time.Time ` + "`json:\"created_at,omitempty\"`" + `
	UpdatedAt time.Time ` + "`json:\"updated_at,omitempty\"`" + `
{{- range .Fields }}
	{{.FieldName}} {{.Type}} ` + "`{{.ResponseTag}}`" + `
{{- end }}
}

type {{.StructName}}ListResponse struct {
	List   []{{.StructName}}Response ` + "`json:\"list\"`" + `
	Paging paging.Paging            ` + "`json:\"paging\"`" + `
}
`

const migrationUpTemplate = `-- Scaffold generated UP migration for {{.StructName}}
-- Module path: {{.ProjectModulePath}}
-- Table: {{.TableName}}
-- REVIEW REQUIRED: verify indexes, constraints, defaults, comments, data volume, and rollback before execution.

CREATE TABLE IF NOT EXISTS ` + "`{{.TableName}}`" + ` (
  ` + "`id`" + ` bigint NOT NULL AUTO_INCREMENT,
{{- range .Fields }}
  ` + "`{{.ColumnName}}`" + ` {{sqlColumn .Type}},
{{- end }}
  ` + "`created_at`" + ` timestamp NULL DEFAULT CURRENT_TIMESTAMP,
  ` + "`updated_at`" + ` timestamp NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  ` + "`deleted_at`" + ` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (` + "`id`" + `),
  KEY ` + "`idx_{{.TableName}}_deleted_at`" + ` (` + "`deleted_at`" + `)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
`

const migrationDownTemplate = `-- Scaffold generated DOWN migration for {{.StructName}}
-- Module path: {{.ProjectModulePath}}
-- Table: {{.TableName}}
-- REVIEW REQUIRED: confirm destructive rollback is acceptable before execution.

DROP TABLE IF EXISTS ` + "`{{.TableName}}`" + `;
`

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
	// Access / Public 严格二选一，且都不给会报错。
	// 访问策略是安全决策，刻意不设默认值——默认值等于允许"没想过"这个状态存在。
	Access string
	Public bool
}

type GenerateResult struct {
	DryRun  bool
	Created []string
	Updated []string
	Skipped []string
	// Notes 是必须让使用者看到的后续动作说明（如占位 authorizer 需要替换）。
	Notes []string
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
	// ConfigTypeName 是模块配置子树的类型名（生成在 platform/config 包内）。
	ConfigTypeName string
	// ConfigFieldName 是它在 ModuleConfigs 里的字段名。
	ConfigFieldName string
	// AccessPolicyExpr 是写进生成代码的访问策略字面量，由 --access/--public 决定。
	AccessPolicyExpr string
	// RateLimitPolicies 是写进生成代码的限流策略标识列表，随访问策略变化：
	// 没有身份的路由不能引用身份作用域的策略。
	RateLimitPolicies string
	// AuthorizerID 非空时表示本模块的访问策略需要一个具名授权实现。
	AuthorizerID string
	// OwnsAuthorizer 为真时需要在 runtime 层生成该授权实现并注册。
	OwnsAuthorizer bool
	Fields         []moduleField
	NeedsModelTime bool
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

func (g *ModuleGenerator) GenerateWithOptions(rawName string, opts GenerateOptions) (*GenerateResult, error) {
	if g == nil {
		return nil, fmt.Errorf("module generator is nil")
	}

	// 项目级建议锁包住"规划读取→提交"全程：两个生成进程交错读写共享
	// 注册文件（providers.go / modules.go）会互相用陈旧内容覆盖。
	// dry-run 同样加锁——与真实提交并发的读取会看到半写状态。
	release, err := acquireGenerationLock(g.root)
	if err != nil {
		return nil, err
	}
	defer release()

	data, err := newModuleTemplateData(rawName, opts.Fields, g.modulePath)
	if err != nil {
		return nil, err
	}

	// 访问策略先于任何文件生成解析：它是安全决策，没做选择就不该产出任何东西。
	choice, err := ResolveAccessChoice(opts.Access, opts.Public, data.PackageName)
	if err != nil {
		return nil, err
	}
	data.AccessPolicyExpr = choice.Expr
	data.RateLimitPolicies = choice.RateLimitPolicies
	data.AuthorizerID = choice.AuthorizerID
	data.OwnsAuthorizer = choice.OwnsAuthorizer

	result := &GenerateResult{}
	result.Notes = append(result.Notes, choice.Note)
	result.DryRun = opts.DryRun
	files := g.generatedFiles(data)
	migrationFiles, err := g.generatedMigrationFiles(data)
	if err != nil {
		return nil, err
	}
	files = append(files, migrationFiles...)

	// 两阶段生成：先完成全部渲染与预检（含所有锚点存在性），只暂存不落盘；
	// 预检全部通过后统一提交。任何一步失败时工作区保持原样（零落盘）。
	var plan []pendingWrite

	for _, file := range files {
		content, err := renderTemplate(file.template, data, file.formatGo)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", g.rel(file.path), err)
		}

		if err := g.writeManagedFile(file, content, opts.Force, opts.DryRun, result, &plan); err != nil {
			return nil, err
		}
	}

	if opts.Bootstrap {
		// 配置注册先于 providers：providers 的 service 构造行引用
		// inf.cfg.Modules.<Name>，那个字段必须已经存在。
		if err := g.updateModuleConfigs(data, opts.DryRun, result, &plan); err != nil {
			return nil, err
		}
		if err := g.updateConfigExample(data, opts.DryRun, result, &plan); err != nil {
			return nil, err
		}
		if err := g.updateProviders(data, opts.DryRun, result, &plan); err != nil {
			return nil, err
		}
		if err := g.updateMigrateModels(data, opts.DryRun, result, &plan); err != nil {
			return nil, err
		}
	}

	// --force 覆盖其余层却保留既有模型时，模型可能与生成的 DTO/迁移 SQL 分裂
	// （module-scaffold-wiring spec）。**告警而不阻断**：模型是使用者拥有并
	// 手写演进的那一层，而"改过模型后用 --force 刷新生成层"正是 --force 的
	// 主要用途——拒绝执行会让它在自己的主场景里没法用。产物是待评审的代码
	// （迁移 SQL 是草稿），让分裂可见即可，不必替使用者做决定。
	if opts.Force {
		note, err := g.modelDriftNote(data)
		if err != nil {
			return nil, err
		}
		if note != "" {
			result.Notes = append(result.Notes, note)
		}
	}

	if opts.DryRun {
		return result, nil
	}
	if err := commitWrites(plan); err != nil {
		return nil, fmt.Errorf("commit generated files: %w", err)
	}

	return result, nil
}

// updateModuleConfigs 把模块的配置字段与校验器接进 platform/config。
//
// 两处注入缺一不可：
//   - 字段进 ModuleConfigs —— 否则根层严格解码会把该模块的整段配置判为未知键，
//     进程直接启动失败；
//   - 校验器进 validateModuleConfigs —— 严格解码只保证"字段名认识"，
//     不保证取值有意义，缺了它 `page_size: -1` 会一路带到运行期。
func (g *ModuleGenerator) updateModuleConfigs(data moduleTemplateData, dryRun bool, result *GenerateResult, plan *[]pendingWrite) error {
	path := filepath.Join(g.root, "internal", "platform", "config", "modules.go")
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", g.rel(path), err)
	}

	fieldsMarker, validatorsMarker := moduleConfigMarkers()
	fieldLine := fmt.Sprintf("\t%s %s `mapstructure:\"%s\"`\n",
		data.ConfigFieldName, data.ConfigTypeName, data.PackageName)
	validatorLine := fmt.Sprintf("\tif err := validate%s(m.%s); err != nil {\n\t\terrs = append(errs, err)\n\t}\n",
		data.ConfigTypeName, data.ConfigFieldName)

	updated, changedField, err := ensureLineBefore(string(content), fieldLine, fieldsMarker)
	if err != nil {
		return fmt.Errorf("update module config field: %w", err)
	}

	updated, changedValidator, err := ensureLineBefore(updated, validatorLine, validatorsMarker)
	if err != nil {
		return fmt.Errorf("update module config validator: %w", err)
	}

	if !changedField && !changedValidator {
		result.Skipped = append(result.Skipped, g.rel(path))
		return nil
	}
	if dryRun {
		result.Updated = append(result.Updated, g.rel(path))
		return nil
	}

	formatted, err := formatWithImports(path, g.modulePath, []byte(updated))
	if err != nil {
		return fmt.Errorf("format %s: %w", g.rel(path), err)
	}
	*plan = append(*plan, pendingWrite{path: path, content: formatted, existed: true, original: content})

	result.Updated = append(result.Updated, g.rel(path))
	return nil
}

// migrateModelsMarker 是迁移命令里 DDL 模型集合的注入锚点。
const migrateModelsMarker = "\t\t\t// scaffold:migrate-models\n"

// updateMigrateModels 把新模型接进迁移命令的 DDL 模型集合。
//
// 不接的话 `--from-model` 看不到新模块的模型：使用者以为生成器把迁移这层
// 也接好了，而实际上漏了——那种漏法只有在真正需要按模型生成迁移时才暴露。
func (g *ModuleGenerator) updateMigrateModels(
	data moduleTemplateData, dryRun bool, result *GenerateResult, plan *[]pendingWrite,
) error {
	path := filepath.Join(g.root, "cmd", "migrate.go")
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", g.rel(path), err)
	}

	line := fmt.Sprintf("\t\t\t&models.%s{},\n", data.StructName)
	updated, changed, err := ensureLineBefore(string(content), line, migrateModelsMarker)
	if err != nil {
		return fmt.Errorf("update migrate ddl models: %w", err)
	}
	if !changed {
		result.Skipped = append(result.Skipped, g.rel(path))
		return nil
	}
	if dryRun {
		result.Updated = append(result.Updated, g.rel(path))
		return nil
	}

	formatted, err := formatWithImports(path, g.modulePath, []byte(updated))
	if err != nil {
		return fmt.Errorf("format %s: %w", g.rel(path), err)
	}
	*plan = append(*plan, pendingWrite{path: path, content: formatted, existed: true, original: content})

	result.Updated = append(result.Updated, g.rel(path))
	return nil
}

// moduleConfigMarkers 返回 platform/config 中的两个注入锚点。
func moduleConfigMarkers() (fields, validators string) {
	return "\t// scaffold:module-config-fields\n",
		"\t// scaffold:module-config-validators\n"
}

// configExampleMarker 是 config/env.yml.example 中模块配置区的锚点。
const configExampleMarker = "  # scaffold:module-config-example\n"

// updateConfigExample 在配置模板里给新模块补一段示例。
//
// 示例必须与字段同批产出：模板是使用者唯一的配置参照，一个存在于代码里
// 却不在模板里的配置项，等于没人知道它可以配。
func (g *ModuleGenerator) updateConfigExample(data moduleTemplateData, dryRun bool, result *GenerateResult, plan *[]pendingWrite) error {
	path := filepath.Join(g.root, "config", "env.yml.example")
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", g.rel(path), err)
	}

	block := fmt.Sprintf(
		"  %s:\n    page_size: 20                  # 列表默认分页大小；<=0 时兜底 20，上界由代码固定\n",
		data.PackageName)

	updated, changed, err := ensureLineBefore(string(content), block, configExampleMarker)
	if err != nil {
		return fmt.Errorf("update config example: %w", err)
	}
	if !changed {
		result.Skipped = append(result.Skipped, g.rel(path))
		return nil
	}
	if dryRun {
		result.Updated = append(result.Updated, g.rel(path))
		return nil
	}
	*plan = append(*plan, pendingWrite{path: path, content: []byte(updated), existed: true, original: content})

	result.Updated = append(result.Updated, g.rel(path))
	return nil
}

// modelDriftNote 在 --force 时报告"模型被保留、其余层被覆盖"的分裂风险。
//
// 返回空串表示没有风险。判据是**内容不一致**而不是"文件存在"：内容与模板
// 一致时覆盖与保留等价，那不构成分裂。
func (g *ModuleGenerator) modelDriftNote(data moduleTemplateData) (string, error) {
	path := filepath.Join(g.root, "internal", "models", data.PackageName+".go")
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // 模型不存在：本次会新建，不存在分裂
		}
		return "", fmt.Errorf("read %s: %w", g.rel(path), err)
	}

	rendered, err := renderTemplate(modelTemplate, data, true)
	if err != nil {
		return "", fmt.Errorf("render model template: %w", err)
	}
	if bytes.Equal(existing, rendered) {
		return "", nil
	}

	return fmt.Sprintf(
		"注意：%s 与本次模板产出不一致，而它**不会**被 --force 覆盖（模型由你手写演进）。"+
			"本次重新生成的 DTO 与迁移 SQL 依据的是模板字段（--fields），"+
			"请核对三者是否一致；需要让模型也回到模板形状时，先删除 %s 再生成。",
		g.rel(path), g.rel(path)), nil
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
			// 模块配置类型生成在 platform/config **自身**的文件里：
			// 基础设施层反向 import internal/module/* 会让依赖方向倒转，
			// 且任一模块的编译错误会连带让配置加载不可用。
			path:           filepath.Join(g.root, "internal", "platform", "config", "module_"+data.PackageName+".go"),
			template:       moduleConfigTemplate,
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

func (g *ModuleGenerator) updateProviders(data moduleTemplateData, dryRun bool, result *GenerateResult, plan *[]pendingWrite) error {
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
	// 注入块与 user/auth 模块同一标准：runtime 构造返回 error，装配缺陷
	// 在启动期上抛，不延迟到首个请求。整块作为单一插入单元，幂等判重。
	serviceLine := fmt.Sprintf(
		"\t%[4]sSvc, err := %[2]s.NewService(inf.mysqlDB, inf.cfg.Modules.%[3]s)\n"+
			"\tif err != nil {\n"+
			"\t\treturn services{}, fmt.Errorf(\"assemble %[4]s service: %%w\", err)\n"+
			"\t}\n"+
			"\tsvc.%[1]s = %[4]sSvc\n",
		data.ServiceVar, data.RuntimeAlias, data.ConfigFieldName, data.PackageName)
	// 模块用 gin 的方式注册：在 RegisterModules 闭包里加一行 Register 调用。
	//
	// **四个依赖字段全部填齐**，与所选访问策略无关：模板的 Deps 声明了它们，
	// 而任一项留空都能通过编译、却在启动或首个请求上以空值调用崩溃，
	// 崩溃点在业务路由注册处、与真正的原因（接线缺失）相隔很远。
	// 早先这里只填 Service/Auth/RateLimit，于是 --access=admin/custom 生成的模块
	// 启动即 panic，--access=optional 每请求 500。
	moduleLine := fmt.Sprintf(
		"\t\t%s.Register(v1, %s.Deps{Service: svc.%s, Auth: mw.auth, "+
			"OptionalAuth: mw.optionalAuth, Authorize: mw.authorizeByID, "+
			"RateLimit: rateLimitStager})\n",
		data.HTTPAlias, data.HTTPAlias, data.ServiceVar)

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
	*plan = append(*plan, pendingWrite{path: providersPath, content: formatted, existed: true, original: content})

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
	return []string{
		"\t// scaffold:module-workers\n",
		// scaffold:module-authorizers 位于 buildAuthorizers 内，是模块专属授权实现的
		// 注册锚点。它同样不参与注入：生成器不产出授权实现，未注册的标识由装配层
		// 兜底为恒拒绝并告警，因此"可启动且不放行"无需注入任何东西即成立。
		//
		// 但它必须被认领并校验。此前它既不在注入集也不在校验集里——是一个看起来
		// 是接线点、实际不参与任何流程的裸注释，而依赖它的功能会静默不生效。
		"\t// scaffold:module-authorizers\n",
		// scaffold:task-handlers 位于 buildWorkers 内的消费者构造参数表里，是
		// tasks.Registrar 的追加锚点。它不参与注入：生成器不产出任务处理器，
		// 而没有处理器时消费者根本不注册（不是注册一个空转的 worker），
		// 因此"未追加处理器也能正常启动"无需注入任何东西即成立。
		//
		// 缩进比其他标记深一级：它在函数调用的参数列表里，而不是语句层。
		"\t\t// scaffold:task-handlers\n",
	}
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
	// 解析符号链接后再向上找（module-scaffold-wiring spec）：项目根路径同时
	// 决定并发锁的路径，两条指向同一项目的等价路径（一条经符号链接）
	// 会算出两把不同的锁——并发防护就此被绕过，而两个进程都以为自己独占。
	//
	// 路径不存在时保持原样：那种情况由下面的向上查找报出更准确的错误。
	if resolved, evalErr := filepath.EvalSymlinks(current); evalErr == nil {
		current = resolved
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
		ConfigTypeName:    structName + "ModuleConfig",
		ConfigFieldName:   structName,
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

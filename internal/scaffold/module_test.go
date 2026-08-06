package scaffold

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModuleGeneratorGenerateCreatesFilesAndUpdatesProviders(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	generator, err := NewModuleGenerator(filepath.Join(root, "internal", "module"))
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}
	generator.now = func() time.Time {
		return time.Date(2026, 3, 24, 9, 8, 7, 0, time.UTC)
	}

	result, err := generator.GenerateWithOptions("admin", GenerateOptions{Force: false, Bootstrap: true, Public: true})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if len(result.Created) == 0 {
		t.Fatal("expected created files")
	}

	assertFileContains(t, filepath.Join(root, "internal", "models", "admin.go"), "type Admin struct")
	assertFileContains(t, filepath.Join(root, "internal", "models", "admin.go"), "BaseID")
	assertFileContains(t, filepath.Join(root, "internal", "models", "admin.go"), "BaseTimeField")

	assertFileContains(t, filepath.Join(root, "internal", "module", "admin", "application", "service.go"), "type Service struct")
	assertFileContains(t, filepath.Join(root, "internal", "module", "admin", "application", "service.go"), "maxListPageSize     = 100")
	assertFileContains(t, filepath.Join(root, "internal", "module", "admin", "application", "service_test.go"), "func TestServiceList")
	// 生成的模块必须产出 RouteSpec，而不是自行注册路由——
	// 后者会绕过访问策略与超时预算，也不会出现在双向契约测试里。
	routerFile := filepath.Join(root, "internal", "module", "admin", "transport", "http", "router.go")
	assertFileContains(t, routerFile, "func Register(r *gin.RouterGroup, deps Deps) {")
	assertFileContains(t, routerFile, `r.Group("/admin")`)
	assertFileContains(t, routerFile, `g.GET("/:id", handler.Get)`)
	// 策略必须显式声明：零值会在启动期被拒绝，生成物不能留下未声明的策略。
	assertFileContains(t, routerFile, "middleware.Timeout(")
	assertFileContains(t, filepath.Join(root, "internal", "module", "admin", "transport", "http", "handler_test.go"), "func TestHandlerList")
	assertFileContains(t, filepath.Join(root, "internal", "repository", "admin", "repository_util.go"), "func (r *Repository) Get")
	assertFileContains(t, filepath.Join(root, "internal", "repository", "admin", "repository_util.go"), "Limit(limit)")
	assertFileContains(t, filepath.Join(root, "internal", "module", "admin", "dto", "admin.go"), "type AdminResponse struct")
	assertFileContains(t, filepath.Join(root, "database", "migrations", "2026_03_24_090807_create_admins_table.up.sql"), "CREATE TABLE IF NOT EXISTS `admins`")
	assertFileContains(t, filepath.Join(root, "database", "migrations", "2026_03_24_090807_create_admins_table.down.sql"), "DROP TABLE IF EXISTS `admins`")

	// 配置能力注册的**四处**必须同批产出。缺任一处的后果都很具体：
	//   缺类型      → runtime 装配引用一个不存在的类型，编译不过；
	//   缺字段      → 根层严格解码把该模块整段配置判为未知键，进程启动失败；
	//   缺校验器    → 严格解码只认字段名，`page_size: -1` 一路带到运行期；
	//   缺示例 YAML → 配置项存在于代码里却没人知道可以配。
	configTypePath := filepath.Join(root, "internal", "platform", "config", "module_admin.go")
	assertFileContains(t, configTypePath, "type AdminModuleConfig struct")
	assertFileContains(t, configTypePath, "`mapstructure:\"page_size\"`")
	assertFileContains(t, configTypePath, "func validateAdminModuleConfig(c AdminModuleConfig) error")

	modulesPath := filepath.Join(root, "internal", "platform", "config", "modules.go")
	assertFileContains(t, modulesPath, "Admin AdminModuleConfig `mapstructure:\"admin\"`")
	assertFileContains(t, modulesPath, "validateAdminModuleConfig(m.Admin)")

	examplePath := filepath.Join(root, "config", "env.yml.example")
	assertFileContains(t, examplePath, "  admin:")
	assertFileContains(t, examplePath, "page_size: 20")

	// runtime 层读配置并翻译成 application 选项；application 不 import platform/config。
	runtimePath := filepath.Join(root, "internal", "runtime", "module", "admin", "module.go")
	assertFileContains(t, runtimePath, "func NewService(db *gorm.DB, cfg config.AdminModuleConfig) *adminapp.Service")
	assertFileContains(t, runtimePath, "adminapp.WithPageSize(cfg.EffectivePageSize())")
	// 判据针对 **import 行**而不是文本出现：注释里必须能自由解释
	// "application 为什么不 import platform/config"，否则这条规则会因为
	// 自己的说明而永远为真，最终只能被加豁免绕过。
	assertFileNotContains(t, filepath.Join(root, "internal", "module", "admin", "application", "service.go"),
		`"github.com/gtkit/gin-api/internal/platform/config"`)

	providersPath := filepath.Join(root, "internal", "bootstrap", "providers.go")
	assertFileContains(t, providersPath, "adminapp \"github.com/gtkit/gin-api/internal/module/admin/application\"")
	assertFileContains(t, providersPath, "adminruntime \"github.com/gtkit/gin-api/internal/runtime/module/admin\"")
	assertFileContains(t, providersPath, "adminhttp \"github.com/gtkit/gin-api/internal/module/admin/transport/http\"")
	assertFileContains(t, providersPath, "adminService *adminapp.Service")
	assertFileContains(t, providersPath, "svc.adminService = adminruntime.NewService(inf.mysqlDB, inf.cfg.Modules.Admin)")
	assertFileContains(t, providersPath, "adminhttp.Register(v1, adminhttp.Deps{Service: svc.adminService")

	// 生成的代码必须符合架构边界（与 scripts/check-architecture.sh 一致）。
	servicePath := filepath.Join(root, "internal", "module", "admin", "application", "service.go")
	assertFileNotContains(t, servicePath, "gorm.io/gorm")
	assertFileNotContains(t, servicePath, "internal/repository/")
	assertFileNotContains(t, servicePath, "internal/dto")
	handlerPath := filepath.Join(root, "internal", "module", "admin", "transport", "http", "handler.go")
	assertFileNotContains(t, handlerPath, "gorm.io/gorm")
	modelPath := filepath.Join(root, "internal", "models", "admin.go")
	assertFileNotContains(t, modelPath, "*gorm.DB")
	assertFileContains(t, filepath.Join(root, "internal", "runtime", "module", "admin", "module.go"), "func NewService(db *gorm.DB, cfg config.AdminModuleConfig) *adminapp.Service")
	assertFileContains(t, filepath.Join(root, "internal", "repository", "admin", "repository.go"), "dbtx.DB(ctx, r.db)")
}

func TestModuleGeneratorGenerateIsIdempotentAndPreservesExistingModel(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	modelPath := filepath.Join(root, "internal", "models", "admin.go")
	original := []byte("package models\n\ntype Admin struct{}\n")
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(modelPath, original, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}
	generator.now = func() time.Time {
		return time.Date(2026, 3, 24, 9, 8, 7, 0, time.UTC)
	}

	if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Force: false, Bootstrap: true, Public: true}); err != nil {
		t.Fatalf("first Generate returned error: %v", err)
	}
	if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Force: true, Bootstrap: true, Public: true}); err != nil {
		t.Fatalf("second Generate returned error: %v", err)
	}

	content, err := os.ReadFile(modelPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(content) != string(original) {
		t.Fatalf("expected existing model to be preserved, got %q", string(content))
	}

	providersContent, err := os.ReadFile(filepath.Join(root, "internal", "bootstrap", "providers.go"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if count := strings.Count(string(providersContent), "adminhttp.Register(v1, adminhttp.Deps{Service: svc.adminService"); count != 1 {
		t.Fatalf("expected single admin module registration, got %d", count)
	}
	if count := strings.Count(string(providersContent), "adminruntime \"github.com/gtkit/gin-api/internal/runtime/module/admin\""); count != 1 {
		t.Fatalf("expected single admin runtime import, got %d", count)
	}
	if count := strings.Count(string(providersContent), "adminService *adminapp.Service"); count != 1 {
		t.Fatalf("expected single admin service field, got %d", count)
	}

	migrationMatches, err := filepath.Glob(filepath.Join(root, "database", "migrations", "*_create_admins_table.*.sql"))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(migrationMatches) != 2 {
		t.Fatalf("expected paired admin migration files, got %d", len(migrationMatches))
	}
}

func TestModuleGeneratorGenerateWithFields(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}
	generator.now = func() time.Time {
		return time.Date(2026, 3, 24, 10, 11, 12, 0, time.UTC)
	}

	_, err = generator.GenerateWithOptions("staff_member", GenerateOptions{
		Bootstrap: true,
		Public:    true,
		Fields: []string{
			"nickname:string",
			"status:uint8",
			"enabled:bool",
			"last_login_at:*time.Time",
		},
	})
	if err != nil {
		t.Fatalf("GenerateWithOptions returned error: %v", err)
	}

	modelPath := filepath.Join(root, "internal", "models", "staff_member.go")
	assertFileContains(t, modelPath, "Nickname")
	assertFileContains(t, modelPath, "json:\"nickname\"")
	assertFileContains(t, modelPath, "Status")
	assertFileContains(t, modelPath, "json:\"status\"")
	assertFileContains(t, modelPath, "Enabled")
	assertFileContains(t, modelPath, "json:\"enabled\"")
	assertFileContains(t, modelPath, "LastLoginAt *time.Time")

	dtoPath := filepath.Join(root, "internal", "module", "staff_member", "dto", "staff_member.go")
	assertFileContains(t, dtoPath, "Nickname")
	assertFileContains(t, dtoPath, "json:\"nickname\"")
	assertFileContains(t, dtoPath, "Status")
	assertFileContains(t, dtoPath, "json:\"status\"")
	assertFileContains(t, dtoPath, "Enabled")
	assertFileContains(t, dtoPath, "json:\"enabled\"")
	assertFileContains(t, dtoPath, "LastLoginAt *time.Time")

	servicePath := filepath.Join(root, "internal", "module", "staff_member", "application", "service.go")
	assertFileContains(t, servicePath, "Nickname:")
	assertFileContains(t, servicePath, "entity.Nickname")
	assertFileContains(t, servicePath, "Status:")
	assertFileContains(t, servicePath, "entity.Status")
	assertFileContains(t, servicePath, "Enabled:")
	assertFileContains(t, servicePath, "entity.Enabled")
	assertFileContains(t, servicePath, "LastLoginAt:")
	assertFileContains(t, servicePath, "entity.LastLoginAt")
}

func TestModuleGeneratorGenerateWithInvalidFieldSpec(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}

	_, err = generator.GenerateWithOptions("staff", GenerateOptions{
		Bootstrap: true,
		Fields:    []string{"nickname"},
	})
	if err == nil || !strings.Contains(err.Error(), "use name:type") {
		t.Fatalf("expected invalid field spec error, got %v", err)
	}

	_, err = generator.GenerateWithOptions("staff", GenerateOptions{
		Bootstrap: true,
		Fields:    []string{"nickname:string", "nickname:int"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate field") {
		t.Fatalf("expected duplicate field error, got %v", err)
	}

	_, err = generator.GenerateWithOptions("staff", GenerateOptions{
		Bootstrap: true,
		Fields:    []string{"created_at:string"},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved field error, got %v", err)
	}
}

func TestModuleGeneratorDryRunDoesNotWriteFiles(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}
	generator.now = func() time.Time {
		return time.Date(2026, 3, 24, 11, 12, 13, 0, time.UTC)
	}

	providersBefore, err := os.ReadFile(filepath.Join(root, "internal", "bootstrap", "providers.go"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}

	result, err := generator.GenerateWithOptions("dry_run_demo", GenerateOptions{DryRun: true, Bootstrap: true, Public: true})
	if err != nil {
		t.Fatalf("GenerateWithOptions returned error: %v", err)
	}
	if !result.DryRun {
		t.Fatal("GenerateResult.DryRun = false, want true")
	}
	if len(result.Created) == 0 {
		t.Fatal("expected dry-run to report created files")
	}

	if _, err := os.Stat(filepath.Join(root, "internal", "models", "dry_run_demo.go")); !os.IsNotExist(err) {
		t.Fatalf("expected dry-run not to write model file, got err=%v", err)
	}

	providersAfter, err := os.ReadFile(filepath.Join(root, "internal", "bootstrap", "providers.go"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Equal(providersBefore, providersAfter) {
		t.Fatal("expected dry-run not to modify providers.go")
	}
}

func TestModuleGeneratorBootstrapFalseDoesNotModifyProviders(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}
	generator.now = func() time.Time {
		return time.Date(2026, 3, 24, 12, 13, 14, 0, time.UTC)
	}

	providersPath := filepath.Join(root, "internal", "bootstrap", "providers.go")
	before, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}

	result, err := generator.GenerateWithOptions("no_bootstrap_demo", GenerateOptions{Bootstrap: false, Public: true})
	if err != nil {
		t.Fatalf("GenerateWithOptions returned error: %v", err)
	}
	if len(result.Created) == 0 {
		t.Fatal("expected scaffold files to be created")
	}

	after, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("expected bootstrap=false not to modify providers.go")
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "models", "no_bootstrap_demo.go")); err != nil {
		t.Fatalf("expected model file to be generated, got err=%v", err)
	}
}

// TestProvidersMarkersPresentInRealProvidersFile 守卫脚手架生成器与真实 providers.go 的标记一致性。
func TestProvidersMarkersPresentInRealProvidersFile(t *testing.T) {
	t.Parallel()

	providersPath := filepath.Join("..", "bootstrap", "providers.go")
	content, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", providersPath, err)
	}

	imports, serviceFields, services, modules := providersMarkers()
	for _, marker := range []string{imports, serviceFields, services, modules} {
		if !strings.Contains(string(content), marker) {
			t.Errorf("providers.go 缺少脚手架标记 %q；scaffold 生成器无法注入新模块", strings.TrimSpace(marker))
		}
	}

	// 校验型标记同样必须在真实 providers.go 中存在，否则 buildWorkers 阶段失去注册锚点。
	for _, marker := range providersValidateOnlyMarkers() {
		if !strings.Contains(string(content), marker) {
			t.Errorf("providers.go 缺少校验型脚手架标记 %q；buildWorkers 阶段失去后台任务注册锚点", strings.TrimSpace(marker))
		}
	}
}

// TestModuleGeneratorMissingWorkerMarkerFails 验证校验型标记与注入型标记享有同等的缺失保护：
// scaffold:module-workers 不参与插入，因此若无独立断言就会静默失去检测。
func TestModuleGeneratorMissingWorkerMarkerFails(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	providersPath := filepath.Join(root, "internal", "bootstrap", "providers.go")
	before, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}

	broken := strings.Replace(string(before), "\t// scaffold:module-workers\n", "", 1)
	if broken == string(before) {
		t.Fatal("fixture 未包含 scaffold:module-workers 标记，测试前提不成立")
	}
	if err := os.WriteFile(providersPath, []byte(broken), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}

	_, err = generator.GenerateWithOptions("admin", GenerateOptions{Force: false, Bootstrap: true, Public: true})
	if err == nil {
		t.Fatal("Generate returned nil error with missing worker marker")
	}
	if !strings.Contains(err.Error(), "scaffold:module-workers") {
		t.Fatalf("error %q does not name the missing anchor", err)
	}

	after, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(after) != broken {
		t.Fatal("providers.go was rewritten after worker marker failure")
	}
}

// TestModuleGeneratorDoesNotRegisterWorker 验证校验型标记的「不注入」语义：
// 生成新模块既不改动 worker 集合，也不产出 worker 源码文件。
func TestModuleGeneratorDoesNotRegisterWorker(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}
	if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Force: false, Bootstrap: true, Public: true}); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	providersPath := filepath.Join(root, "internal", "bootstrap", "providers.go")
	content, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}

	// buildWorkers 函数体除标记外仍为空：标记前后不得出现任何注册行。
	got := string(content)
	if !strings.Contains(got, "func buildWorkers(_ services) {\n\t// scaffold:module-workers\n}") {
		t.Fatalf("buildWorkers 被意外注入内容:\n%s", got)
	}
	if strings.Contains(got, "adminworker") || strings.Contains(got, "NewFuncWorker") {
		t.Fatal("生成器不应向 buildWorkers 注入 worker 注册行")
	}

	// 不产出任何 worker 源码文件。
	if _, err := os.Stat(filepath.Join(root, "internal", "module", "admin", "worker")); !os.IsNotExist(err) {
		t.Fatalf("生成器不应产出 worker 目录, stat err=%v", err)
	}
}

func TestModuleGeneratorMissingProviderMarkerDoesNotRewriteFile(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	providersPath := filepath.Join(root, "internal", "bootstrap", "providers.go")
	before, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	broken := strings.Replace(string(before), "\t// scaffold:module-services\n", "", 1)
	if err := os.WriteFile(providersPath, []byte(broken), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}
	if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Force: false, Bootstrap: true, Public: true}); err == nil {
		t.Fatal("Generate returned nil error with missing provider marker")
	}

	after, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(after) != broken {
		t.Fatal("providers.go was partially rewritten after marker failure")
	}
}

// TestModuleGeneratorProvidersGoimportsClean 验证生成器注入后的 providers.go 已 goimports 干净：
// 用同一 goimports 引擎（formatWithImports，FormatOnly + local-prefix）再格式化一次应无变化（幂等）。
func TestModuleGeneratorProvidersGoimportsClean(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator returned error: %v", err)
	}
	generator.now = func() time.Time {
		return time.Date(2026, 3, 24, 13, 14, 15, 0, time.UTC)
	}

	if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Force: false, Bootstrap: true, Public: true}); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	providersPath := filepath.Join(root, "internal", "bootstrap", "providers.go")
	got, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}

	reformatted, err := formatWithImports(providersPath, "github.com/gtkit/gin-api", got)
	if err != nil {
		t.Fatalf("formatWithImports returned error: %v", err)
	}
	if !bytes.Equal(got, reformatted) {
		t.Fatalf("generated providers.go is not goimports-clean:\n--- got ---\n%s\n--- reformatted ---\n%s", got, reformatted)
	}
}

func TestSQLColumnDefinition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fieldType string
		want      string
	}{
		{name: "string", fieldType: "string", want: "varchar(255) NOT NULL DEFAULT ''"},
		{name: "bool", fieldType: "bool", want: "tinyint(1) NOT NULL DEFAULT 0"},
		{name: "int8", fieldType: "int8", want: "tinyint NOT NULL DEFAULT 0"},
		{name: "uint16", fieldType: "uint16", want: "smallint NOT NULL DEFAULT 0"},
		{name: "int32", fieldType: "int32", want: "int NOT NULL DEFAULT 0"},
		{name: "uint64", fieldType: "uint64", want: "bigint NOT NULL DEFAULT 0"},
		{name: "float32", fieldType: "float32", want: "float NOT NULL DEFAULT 0"},
		{name: "float64", fieldType: "float64", want: "double NOT NULL DEFAULT 0"},
		{name: "time", fieldType: "time.Time", want: "timestamp NOT NULL"},
		{name: "optional time", fieldType: "*time.Time", want: "timestamp NULL DEFAULT NULL"},
		{name: "unknown type fallback", fieldType: "custom.Type", want: "varchar(255) NOT NULL DEFAULT ''"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := sqlColumnDefinition(tt.fieldType); got != tt.want {
				t.Fatalf("sqlColumnDefinition(%q) = %q, want %q", tt.fieldType, got, tt.want)
			}
		})
	}
}

func createProjectFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module github.com/gtkit/gin-api\n\ngo 1.26.1\n",
		// fixture 镜像真实 providers.go 的分阶段结构与五个脚手架标记
		// （四个注入型 + 两个校验型：scaffold:module-workers、scaffold:module-authorizers）。
		"internal/bootstrap/providers.go": `package bootstrap

import (
	"github.com/gtkit/gin-api/internal/middleware"
	userapp "github.com/gtkit/gin-api/internal/module/user/application"
	userhttp "github.com/gtkit/gin-api/internal/module/user/transport/http"
	"github.com/gtkit/gin-api/internal/router"
	userruntime "github.com/gtkit/gin-api/internal/runtime/module/user"
	// scaffold:module-imports
)

type infra struct {
	mysqlDB any
	cfg     any
}

type services struct {
	user *userapp.Service
	// scaffold:module-service-fields
}

type middlewares struct {
	auth any
}

func buildServices(inf infra) services {
	var svc services
	svc.user = userruntime.NewService(inf.mysqlDB)
	// scaffold:module-services
	return svc
}

func buildEngine(svc services, mw middlewares) {
	opt.RegisterModules = func(v1 *gin.RouterGroup) {
		userhttp.Register(v1, userhttp.Deps{Service: svc.user, Auth: mw.auth, RateLimit: rateLimitByID})
		// scaffold:modules
	}
	_ = middleware.Auth
}

func buildWorkers(_ services) {
	// scaffold:module-workers
}

func buildAuthorizers() {
	// scaffold:module-authorizers
}
`,
		// config 锚点文件：模块配置字段与校验器的注入点。
		"internal/platform/config/modules.go": `package config

import "errors"

type ModuleConfigs struct {
	// scaffold:module-config-fields
}

func validateModuleConfigs(m ModuleConfigs) error {
	var errs []error

	// scaffold:module-config-validators

	return errors.Join(errs...)
}
`,
		// 配置模板锚点：示例必须与字段同批产出。
		"config/env.yml.example": "modules:\n  # scaffold:module-config-example\n",
	}

	for name, content := range files {
		fullPath := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("MkdirAll returned error: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile returned error: %v", err)
		}
	}

	return root
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	if !strings.Contains(string(content), want) {
		t.Fatalf("expected %s to contain %q, got %q", path, want, string(content))
	}
}

func assertFileNotContains(t *testing.T, path, unwanted string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	if strings.Contains(string(content), unwanted) {
		t.Fatalf("expected %s NOT to contain %q, got %q", path, unwanted, string(content))
	}
}

// TestModuleConfigAnchorsAreRequired 是配置注册锚点的**负向证明**。
//
// 逐个破坏锚点后生成必须失败。没有这一步，上面那组"四处同步"的正向断言
// 可能因为别的原因恒真（例如注入逻辑根本没跑、而断言的内容恰好来自模板），
// 从而失去鉴别力。
func TestModuleConfigAnchorsAreRequired(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		file   string
		anchor string
		why    string
	}{
		{
			name:   "config field anchor",
			file:   filepath.Join("internal", "platform", "config", "modules.go"),
			anchor: "// scaffold:module-config-fields",
			why:    "字段缺失会让根层严格解码把该模块整段配置判为未知键",
		},
		{
			name:   "config validator anchor",
			file:   filepath.Join("internal", "platform", "config", "modules.go"),
			anchor: "// scaffold:module-config-validators",
			why:    "校验器缺失会让非法取值一路带到运行期",
		},
		{
			name:   "config example anchor",
			file:   filepath.Join("config", "env.yml.example"),
			anchor: "# scaffold:module-config-example",
			why:    "示例缺失会让配置项存在于代码里却没人知道可以配",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := createProjectFixture(t)
			path := filepath.Join(root, tc.file)

			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture %s: %v", tc.file, err)
			}
			if !bytes.Contains(content, []byte(tc.anchor)) {
				t.Fatalf("fixture %s does not contain anchor %q; the test itself is stale", tc.file, tc.anchor)
			}
			broken := bytes.ReplaceAll(content, []byte(tc.anchor), []byte("// scaffold-BROKEN"))
			if err := os.WriteFile(path, broken, 0o644); err != nil {
				t.Fatalf("write broken fixture: %v", err)
			}

			generator, err := NewModuleGenerator(root)
			if err != nil {
				t.Fatalf("NewModuleGenerator: %v", err)
			}
			generator.now = func() time.Time { return time.Date(2026, 3, 24, 9, 8, 7, 0, time.UTC) }

			if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Bootstrap: true, Public: true}); err == nil {
				t.Fatalf("generation succeeded with anchor %q removed; %s", tc.anchor, tc.why)
			}
		})
	}
}

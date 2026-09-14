//go:build scaffold

// module_templates.go 收纳模块生成器的全部代码模板。
// 模板与编排、提交逻辑分文件：本文件只有字符串常量，无任何行为。
package scaffold

const moduleConfigTemplate = `package config

import "fmt"

// {{.ConfigTypeName}} 是 {{.PackageName}} 模块的配置子树。
//
// 类型与 mapstructure tag 定义在 config 包内，而不是 import 该模块——
// 基础设施层反向依赖 internal/module/* 会让依赖方向倒转。
//
// 生成的字段只是**起点**：按模块实际需要增删。每加一个字段，
// 同步在 validate{{.ConfigTypeName}} 里给出它的语义约束（严格解码只保证
// 字段名认识，不保证取值有意义），并在 config/env.yml.example 里给出示例。
type {{.ConfigTypeName}} struct {
	// PageSize 是列表接口的默认分页大小。
	PageSize int ` + "`" + `mapstructure:"page_size"` + "`" + `
}

// Default{{.StructName}}PageSize 是未配置 page_size 时的取值。
const Default{{.StructName}}PageSize = 20

// EffectivePageSize 返回生效的分页大小。
//
// 用"未设置时取默认值"而不是"必填"：脚手架生成的模块要能在
// 完全不写配置的情况下跑起来，否则每加一个模块都会让现有部署启动失败。
func (c {{.ConfigTypeName}}) EffectivePageSize() int {
	if c.PageSize <= 0 {
		return Default{{.StructName}}PageSize
	}
	return c.PageSize
}

// validate{{.ConfigTypeName}} 校验 {{.PackageName}} 模块配置的语义。
func validate{{.ConfigTypeName}}(c {{.ConfigTypeName}}) error {
	if c.PageSize < 0 {
		return fmt.Errorf("modules.{{.PackageName}}.page_size must not be negative, got %d", c.PageSize)
	}
	return nil
}
`

const modelTemplate = `package models
{{- if .NeedsModelTime }}

import "time"
{{- end }}

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
	"{{.ProjectModulePath}}/internal/pkg/paging"

	"github.com/gtkit/ormx/paginator"
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

// listSortMapping 是排序白名单：外部排序键 → 受信任列名。
// 动态排序列进不了参数占位符，白名单是 ORM 项目里防注入的唯一手段
// （docs/db-patterns.md「防御性查询」）。未命中的键由执行器回退默认排序（模型主键）。
// 新增可排序字段时，与 dto.{{.StructName}}ListRequest 的 sort oneof 白名单同步更新。
var listSortMapping = map[string]string{
	"id":         "id",
	"created_at": "created_at",
}

// List 经 ormx/paginator 执行有界分页查询。
//
// 执行器自动追加主键次级排序，等值排序键下翻页不重不漏；越界页码钳制到
// 最后一页，返回的分页元信息是实际生效值（空结果 CurrentPage/TotalPage 为 0）。
// fields 投影同时作用于总数统计（GORM Count 语义随 Select 变化，见 ormx 包文档），
// 需要精确总数的自定义投影请另写专用查询。
func (r *Repository) List(ctx context.Context, query paging.Query, fields ...string) ([]*models.{{.StructName}}, paging.Paging, error) {
	// 防御钳制与 application 归一化同值：repo 也可能被 application 之外的
	// 调用方直接使用，无界查询不能指望上游都归一化过。
	if query.PageSize <= 0 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}

	q := r.mdbCtx(ctx).Model(&models.{{.StructName}}{})
	if len(fields) > 0 {
		q = q.Select(fields)
	}

	page, err := paginator.Paginate[*models.{{.StructName}}](q, paginator.Params{
		Page:     query.Page,
		PageSize: query.PageSize,
		Sort:     query.Sort,
		Order:    query.Order,
	}, paginator.WithSortMapping(listSortMapping))
	if err != nil {
		return nil, paging.Paging{}, fmt.Errorf("list {{.PackageName}}: %w", err)
	}

	return page.Items, paging.Paging{
		CurrentPage: page.CurrentPage,
		PageSize:    page.PageSize,
		TotalPage:   page.TotalPage,
		TotalCount:  page.TotalCount,
	}, nil
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
	"{{.ProjectModulePath}}/internal/pkg/paging"
)

type {{.StructName}}Repository interface {
	Get(ctx context.Context, id int64, fields ...string) (*models.{{.StructName}}, bool, error)
	// List 返回一页数据与实际生效的分页元信息。query.Sort 是外部排序键，
	// 由实现方经白名单映射后才进入 SQL，未命中回退默认排序。
	List(ctx context.Context, query paging.Query, fields ...string) ([]*models.{{.StructName}}, paging.Paging, error)
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
	// pageSize 是未显式指定分页大小时的默认值，由 runtime 层从配置注入。
	pageSize int
}

// ServiceOption 配置 Service 的可选策略。
//
// 只有**真正可选**的策略走 option；必需依赖走构造参数，
// 否则"依赖是否齐全"会从编译期问题退化成运行期问题。
type ServiceOption func(*Service)

// WithPageSize 注入默认分页大小（<=0 时保留内置默认值）。
//
// application 不 import platform/config：配置的读取与翻译在 runtime 层完成，
// 否则业务编排会绑死在一份具体的配置结构上，单测也必须先造一个完整 Config 才能跑。
func WithPageSize(size int) ServiceOption {
	return func(s *Service) {
		if size > 0 {
			s.pageSize = size
		}
	}
}

// NewWithDeps 创建 {{.StructName}} application service。
//
// 必需依赖在此校验：装配缺陷必须在启动期失败，不许延迟到首个请求才以
// 空指针暴露——那时崩溃点在业务代码里，与真正的原因（接线缺失）相隔很远。
func NewWithDeps(repo {{.StructName}}Repository, opts ...ServiceOption) (*Service, error) {
	if repo == nil {
		return nil, errors.New("{{.PackageName}}: repository is required")
	}
	s := &Service{repo: repo, pageSize: defaultListPageSize}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// List 返回有界分页列表。
//
// 入参是 paging.Query（协议无关）：排序键在 dto 层经 oneof 白名单校验、
// 在 repository 层经映射白名单进入 SQL。分页元信息取执行器返回的实际生效值：
// 越界页码被钳制到最后一页，空结果 CurrentPage/TotalPage 为 0。
func (s *Service) List(ctx context.Context, query paging.Query) (dto.{{.StructName}}ListResponse, error) {
	query = s.normalizeListQuery(query)
	list, page, err := s.repo.List(ctx, query)
	if err != nil {
		return dto.{{.StructName}}ListResponse{}, fmt.Errorf("list {{.PackageName}}: %w", err)
	}

	response := make([]dto.{{.StructName}}Response, 0, len(list))
	for _, item := range list {
		response = append(response, to{{.StructName}}Response(item))
	}

	return dto.{{.StructName}}ListResponse{
		List:   response,
		Paging: page,
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

// normalizeListQuery 收敛分页参数（Sort/Order 原样透传，白名单在 dto 与 repository 两侧）。
//
// 上界 maxListPageSize 无条件生效，**配置也不能突破**：它挡的是
// "一次请求把整张表拉出来"，而那既是内存风险也是慢查询来源。
func (s *Service) normalizeListQuery(query paging.Query) paging.Query {
	if query.Page <= 0 {
		query.Page = defaultListPage
	}
	if query.PageSize <= 0 {
		query.PageSize = s.effectivePageSize()
	}
	if query.PageSize > maxListPageSize {
		query.PageSize = maxListPageSize
	}
	return query
}

// effectivePageSize 返回生效的默认分页大小。
// 零值 Service（未经构造函数）也安全：回退到内置默认值。
func (s *Service) effectivePageSize() int {
	if s.pageSize <= 0 {
		return defaultListPageSize
	}
	if s.pageSize > maxListPageSize {
		return maxListPageSize
	}
	return s.pageSize
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
	"{{.ProjectModulePath}}/internal/pkg/paging"
)

type fake{{.StructName}}Repository struct {
	entity    *models.{{.StructName}}
	found     bool
	list      []*models.{{.StructName}}
	page      paging.Paging
	err       error
	lastQuery paging.Query
}

func (f *fake{{.StructName}}Repository) Get(context.Context, int64, ...string) (*models.{{.StructName}}, bool, error) {
	return f.entity, f.found, f.err
}

func (f *fake{{.StructName}}Repository) List(_ context.Context, query paging.Query, _ ...string) ([]*models.{{.StructName}}, paging.Paging, error) {
	f.lastQuery = query
	return f.list, f.page, f.err
}

// mustService 构造 service；测试 fake 恒为合法依赖，构造失败即测试装置错误。
func mustService(t *testing.T, repo {{.StructName}}Repository) *Service {
	t.Helper()
	svc, err := NewWithDeps(repo)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	return svc
}

// TestNewWithDepsRequiresRepository 钉住装配缺陷启动期失败：缺仓储即构造错误，
// 不延迟到首个请求才以空指针暴露。
func TestNewWithDepsRequiresRepository(t *testing.T) {
	t.Parallel()

	if _, err := NewWithDeps(nil); err == nil {
		t.Fatal("NewWithDeps(nil) must fail: assembly defects must surface at startup")
	}
}

func TestServiceList(t *testing.T) {
	t.Parallel()

	repoErr := errors.New("repository unavailable")
	tests := []struct {
		name      string
		query     paging.Query
		repo      *fake{{.StructName}}Repository
		wantErr   error
		wantQuery paging.Query
	}{
		{
			name:      "default pagination",
			repo:      &fake{{.StructName}}Repository{},
			wantQuery: paging.Query{Page: 1, PageSize: 20},
		},
		{
			name:  "page size capped and sort passed through",
			query: paging.Query{Page: 2, PageSize: 500, Sort: "created_at", Order: "desc"},
			repo: &fake{{.StructName}}Repository{
				page: paging.Paging{CurrentPage: 2, PageSize: 100, TotalPage: 3, TotalCount: 250},
			},
			wantQuery: paging.Query{Page: 2, PageSize: 100, Sort: "created_at", Order: "desc"},
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

			got, err := mustService(t, tt.repo).List(t.Context(), tt.query)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("List() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("List() unexpected error: %v", err)
			}
			if tt.repo.lastQuery != tt.wantQuery {
				t.Fatalf("repository query = %+v, want %+v", tt.repo.lastQuery, tt.wantQuery)
			}
			// 分页元信息必须原样来自执行器（越界钳制后的实际生效值），
			// 由 application 自行推算会与实际执行脱节。
			if got.Paging != tt.repo.page {
				t.Fatalf("List() paging = %+v, want repository page %+v", got.Paging, tt.repo.page)
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

			got, err := mustService(t, tt.repo).Get(t.Context(), 7)
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
	"fmt"

	{{.AppAlias}} "{{.ProjectModulePath}}/internal/module/{{.PackageName}}/application"
	"{{.ProjectModulePath}}/internal/platform/config"
	{{.RepoAlias}} "{{.ProjectModulePath}}/internal/repository/{{.PackageName}}"

	"gorm.io/gorm"
)

// NewService 在 runtime 层装配 {{.PackageName}} service：db→repo→application service，
// 使 application 不依赖 gorm / repository。
//
// 配置在**这一层**被读取并翻译成 application 的选项：application 不 import
// platform/config，否则业务编排会绑死在一份具体的配置结构上，
// 单测也必须先造一个完整 Config 才能跑。
func NewService(db *gorm.DB, cfg config.{{.ConfigTypeName}}) (*{{.AppAlias}}.Service, error) {
	svc, err := {{.AppAlias}}.NewWithDeps(
		{{.RepoAlias}}.New(db),
		{{.AppAlias}}.WithPageSize(cfg.EffectivePageSize()),
	)
	if err != nil {
		return nil, fmt.Errorf("assemble {{.PackageName}} service: %w", err)
	}
	return svc, nil
}
`

const transportRouterTemplate = `package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"{{.ProjectModulePath}}/internal/middleware"
	{{.AppAlias}} "{{.ProjectModulePath}}/internal/module/{{.PackageName}}/application"
)

// 本模块的业务超时预算。按实际情况调整。
const (
	{{.ServiceVar}}Deadline   = 5 * time.Second
	{{.ServiceVar}}WriteGrace = 2 * time.Second
)

// Deps 是注册路由所需的依赖。
type Deps struct {
	Service *{{.AppAlias}}.Service
	// Auth 是认证中间件，由装配层注入。需要鉴权的路由挂它。
	Auth gin.HandlerFunc
	// OptionalAuth 允许匿名访问，但带了 token 就必须合法。
	OptionalAuth gin.HandlerFunc
	// Authorize 按 ID 取授权中间件。
	Authorize func(authorizerID string) gin.HandlerFunc
	// RateLimit 按策略标识取限流中间件，并按作用域分成认证前、认证后两组。
	// 首参是引用发生处的路由标识，装配失败时用于指认现场。
	//
	// 模块只声明受哪些策略约束，位置由策略自身的作用域决定。
	RateLimit func(route string, policyIDs ...string) (preAuth, postAuth []gin.HandlerFunc)
	// Cache 按 TTL 取路由级 GET 响应缓存中间件，由装配层注入。
	// ttl <= 0 取配置默认值；缓存关闭时注入的是透传中间件，无需判空。
	//
	// 只对**匿名可访问的只读路由**有意义：带身份的请求在中间件内一律绕过
	// 缓存。挂法（写在具体路由上，不 Group.Use 全组开启）：
	//
	//	g.GET("", deps.Cache(0), handler.List)
	Cache func(ttl time.Duration) gin.HandlerFunc
}

// Register 在给定路由组上注册本模块的路由。
//
// 用法就是 gin 的用法：拿 *gin.RouterGroup，用 Group 挂中间件，
// 用 GET/POST 注册处理函数。
//
// **访问控制由代码位置表达**：新增路由必须写在 g 里面。写到 g 外面就是公开
// 接口，而编译与测试都不会有任何异常——新增路由时需要人工确认。
// 本模块的访问策略在生成时由 --access/--public 选定，已注入到下面的位置。
func Register(r *gin.RouterGroup, deps Deps) {
	handler := NewHandler(deps.Service)

	g := r.Group("/{{.RoutePath}}")
	g.Use(middleware.Timeout({{.ServiceVar}}Deadline, {{.ServiceVar}}WriteGrace))

	// 限流策略在 config 的 ratelimit.policies 里定义；引用解析不到时装配失败，
	// 不存在静默不限流的通道。
	//
	// 每条挂在访问策略的哪一侧由它自身的作用域决定，这里只声明受哪些策略约束：
	// 不需要身份的作用域必须在访问策略之前（它挡的是尚未证明身份的流量），
	// 需要身份的作用域必须在之后（否则取不到主体，每个请求都被判 429）。
	preAuth, postAuth := stageRateLimits(deps.RateLimit, "/{{.RoutePath}}", {{.RateLimitPolicies}})

	g.Use(preAuth...)
	{{.AccessPolicyExpr}}
	g.Use(postAuth...)

	g.GET("", handler.List)
	g.GET("/:id", handler.Get)
}

// stageRateLimits 容错地调用装配层注入的分组函数。
//
// nil 容错只服务于单测手工构造 Deps 的场景；生产装配恒注入 stager，
// 引用解析不到时由装配层在路由注册完成后统一失败，不存在静默不限流的通道。
func stageRateLimits(
	stager func(string, ...string) ([]gin.HandlerFunc, []gin.HandlerFunc), route string, ids ...string,
) (preAuth, postAuth []gin.HandlerFunc) {
	if stager == nil {
		return nil, nil
	}
	return stager(route, ids...)
}
`

const transportHandlerTemplate = `package http

import (
	"strconv"

	"{{.ProjectModulePath}}/internal/module/{{.PackageName}}/application"
	"{{.ProjectModulePath}}/internal/module/{{.PackageName}}/dto"
	"{{.ProjectModulePath}}/internal/pkg/paging"
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

// 添加写操作（Create / Update）时，body 绑定用 ginx.BindBody 而**不是**
// c.ShouldBind：后者在 Content-Type 为 form 时会把 query 参数一并并入绑定结果，
// 于是本该只来自 body 的字段可以被 query 注入。架构传感器会拦下 c.ShouldBind，
// 但先知道原因比撞上门禁更省事。
func (h *Handler) List(c *gin.Context) {
	var req dto.{{.StructName}}ListRequest
	// 这里用 ShouldBindQuery 是对的：列表参数的来源本就是 query。
	if err := c.ShouldBindQuery(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	data, err := h.service.List(c.Request.Context(), paging.Query{
		Page:     req.Page,
		PageSize: req.PageSize,
		Sort:     req.Sort,
		Order:    req.Order,
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
	// 非正主键一律拒绝：它不可能对应任何行，放行只会让每个这样的请求
	// 白打一次数据库，而返回的"找不到"看起来与真实查询无异。
	if id <= 0 {
		resp.BadRequestErr(c, "id must be positive")
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
	"{{.ProjectModulePath}}/internal/pkg/paging"

	"github.com/gin-gonic/gin"
)

type fake{{.StructName}}Repository struct {
	entity     *models.{{.StructName}}
	found      bool
	err        error
	listCalled bool
}

func (f *fake{{.StructName}}Repository) Get(context.Context, int64, ...string) (*models.{{.StructName}}, bool, error) {
	return f.entity, f.found, f.err
}

func (f *fake{{.StructName}}Repository) List(context.Context, paging.Query, ...string) ([]*models.{{.StructName}}, paging.Paging, error) {
	f.listCalled = true
	return nil, paging.Paging{}, f.err
}

// mustService 构造 service；测试 fake 恒为合法依赖，构造失败即测试装置错误。
func mustService(t *testing.T, repo application.{{.StructName}}Repository) *application.Service {
	t.Helper()
	svc, err := application.NewWithDeps(repo)
	if err != nil {
		t.Fatalf("NewWithDeps: %v", err)
	}
	return svc
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
		{name: "sorted success", target: "/?sort=created_at&order=desc", repo: &fake{{.StructName}}Repository{}, wantStatus: http.StatusOK},
		{name: "invalid page size", target: "/?page_size=101", repo: &fake{{.StructName}}Repository{}, wantStatus: http.StatusUnprocessableEntity},
		{name: "sort key outside whitelist", target: "/?sort=password", repo: &fake{{.StructName}}Repository{}, wantStatus: http.StatusUnprocessableEntity},
		{name: "invalid order", target: "/?order=sideways", repo: &fake{{.StructName}}Repository{}, wantStatus: http.StatusUnprocessableEntity},
		{name: "repository error", target: "/", repo: &fake{{.StructName}}Repository{err: repoErr}, wantStatus: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.target, nil)

			NewHandler(mustService(t, tt.repo)).List(ctx)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			// 绑定层拒绝的请求不得触达仓储：非法排序键若继续执行，
			// 白名单就退化成了"只是响应了 422 的日志"。
			if tt.wantStatus == http.StatusUnprocessableEntity && tt.repo.listCalled {
				t.Fatal("repository List executed despite binding rejection")
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

			NewHandler(mustService(t, tt.repo)).Get(ctx)

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
	// Sort 的 oneof 是可排序键白名单，与 repository 的 listSortMapping 同源；
	// 新增可排序字段时两处同步更新。非法键在绑定层拒绝（422），
	// 而不是静默回退——静默回退会把客户端拼错的键藏起来。
	Sort  string ` + "`form:\"sort\" binding:\"omitempty,oneof=id created_at\"`" + `
	Order string ` + "`form:\"order\" binding:\"omitempty,oneof=asc desc\"`" + `
}

type {{.StructName}}DetailRequest struct {
	ID int64 ` + "`json:\"id\" form:\"id\" binding:\"required,gte=1\"`" + `
}

// 时间字段用 omitzero 而不是 omitempty：后者对 struct 类型永不触发，零值时间
// 照样输出 "0001-01-01T00:00:00Z"——标注写了却不生效。omitzero（Go 1.24 起）
// 按 IsZero() 判定。这是对外响应契约，写错会跟着每个生成的模块一起扩散。
type {{.StructName}}Response struct {
	ID        int64     ` + "`json:\"id\"`" + `
	CreatedAt time.Time ` + "`json:\"created_at,omitzero\"`" + `
	UpdatedAt time.Time ` + "`json:\"updated_at,omitzero\"`" + `
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
) ENGINE=InnoDB DEFAULT CHARSET={{.Charset}} COLLATE={{.Collation}};
`

const migrationDownTemplate = `-- Scaffold generated DOWN migration for {{.StructName}}
-- Module path: {{.ProjectModulePath}}
-- Table: {{.TableName}}
-- REVIEW REQUIRED: confirm destructive rollback is acceptable before execution.

DROP TABLE IF EXISTS ` + "`{{.TableName}}`" + `;
`

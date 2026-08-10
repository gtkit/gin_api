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

// ListQuery 是与传输协议无关的分页查询参数。
type ListQuery struct {
	Page     int
	PageSize int
}

// List 返回有界分页列表。
func (s *Service) List(ctx context.Context, query ListQuery) (dto.{{.StructName}}ListResponse, error) {
	query = s.normalizeListQuery(query)
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

// normalizeListQuery 收敛分页参数。
//
// 上界 maxListPageSize 无条件生效，**配置也不能突破**：它挡的是
// "一次请求把整张表拉出来"，而那既是内存风险也是慢查询来源。
func (s *Service) normalizeListQuery(query ListQuery) ListQuery {
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
		{name: "invalid page size", target: "/?page_size=101", repo: &fake{{.StructName}}Repository{}, wantStatus: http.StatusUnprocessableEntity},
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
`

const migrationDownTemplate = `-- Scaffold generated DOWN migration for {{.StructName}}
-- Module path: {{.ProjectModulePath}}
-- Table: {{.TableName}}
-- REVIEW REQUIRED: confirm destructive rollback is acceptable before execution.

DROP TABLE IF EXISTS ` + "`{{.TableName}}`" + `;
`

package http

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/module/example/application"
	"github.com/gtkit/gin-api/internal/module/example/dto"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/ginx"
	"github.com/gtkit/goerr"
	"time"
)

type Deps struct {
	Service *application.Service
	Auth    gin.HandlerFunc
}

// Register mounts authenticated CRUD. The caller supplies its existing auth middleware.
func Register(parent *gin.RouterGroup, deps Deps) {
	if deps.Service == nil || deps.Auth == nil {
		panic("record service and authentication required")
	}
	group := parent.Group("/example", deps.Auth, middleware.NoStore(), middleware.Timeout(5*time.Second, time.Second))
	handleError := func(c *gin.Context, err error) {
		switch {
		case errors.Is(err, application.ErrNotFound):
			resp.NotFoundError(c, "record not found")
		case errors.Is(err, application.ErrInvalid):
			resp.Err(c, goerr.New(err, goerr.StatusValidateParams(), "invalid record input"))
		case errors.Is(err, application.ErrForbidden):
			resp.Err(c, goerr.New(err, goerr.StatusPermission()))
		default:
			resp.MapErr(c, err, nil)
		}
	}
	group.GET("", middleware.NoBody(), func(c *gin.Context) {
		var query dto.ListRequest
		if err := c.ShouldBindQuery(&query); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		result, err := deps.Service.List(c.Request.Context(), ginc.UserIDFromContext(c), query.Page, query.PageSize)
		if err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, result)
	})
	group.GET("/:id", middleware.NoBody(), func(c *gin.Context) {
		result, err := deps.Service.Get(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id"))
		if err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, result)
	})
	group.POST("", middleware.JSONBody(8192), func(c *gin.Context) {
		var request dto.WriteRequest
		if err := ginx.BindBody(c, &request); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		result, err := deps.Service.Create(c.Request.Context(), ginc.UserIDFromContext(c), request.Title)
		if err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, result)
	})
	group.PUT("/:id", middleware.JSONBody(8192), func(c *gin.Context) {
		var request dto.WriteRequest
		if err := ginx.BindBody(c, &request); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		result, err := deps.Service.Update(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id"), request.Title)
		if err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, result)
	})
	group.DELETE("/:id", middleware.NoBody(), func(c *gin.Context) {
		if err := deps.Service.Delete(c.Request.Context(), ginc.UserIDFromContext(c), c.Param("id")); err != nil {
			handleError(c, err)
			return
		}
		resp.Ok(c, nil)
	})
}

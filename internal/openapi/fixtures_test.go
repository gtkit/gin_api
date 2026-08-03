package openapi

import (
	"reflect"
	"testing"

	"github.com/gtkit/gin-api/internal/module/user/dto"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/router/routespec"
)

// mustGenerate 用**真实信封** response.ResResult 生成文档。
//
// 测试不自造信封 fixture：那样测的是一个只有测试才用的形状，
// 而真实信封换了形状（比如 data 不再是 interface）测试仍然全绿。
func mustGenerate(t *testing.T, routes []routespec.CompiledRoute, opts Options) Document {
	t.Helper()

	opts.Envelope = resp.ResResult{}
	doc, err := Generate(routes, opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return doc
}

// realDTOs 列出仓库里真实对外的 DTO。
//
// 选型实测必须跑真实类型：合成 fixture 全过但真实类型失败是常见情形——
// 真实类型会带上合成用例想不到的组合（内嵌、自定义类型、指针与校验 tag 并存）。
func realDTOs() map[Role][]any {
	return map[Role][]any{
		RoleRequest:  {dto.UserUpdateRequest{}, dto.UserDeactivateRequest{}},
		RoleResponse: {dto.UserProfileResponse{}},
	}
}

// reflectTypeOf 是 reflect.TypeOf 的别名，让测试不必直接 import reflect。
func reflectTypeOf(v any) reflect.Type { return reflect.TypeOf(v) }

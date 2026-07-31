# OpenAPI Contract

`openapi.json` 是当前服务对外 HTTP API 的 OpenAPI 3.1 契约，覆盖根路径健康探针和已注册的 v1 user 模块路由。

## 更新规则

新增、删除或修改公开路由时，同步更新：

1. `paths` 中的 method/path、`operationId`、请求体和响应。
2. `components.schemas` 中对应 DTO 与统一响应信封。
3. 认证方式或请求头发生变化时更新 `components.securitySchemes`。

提交前执行：

```bash
make check-openapi
```

该检查会装配真实 Gin router，并要求契约中的 method/path 与注册路由完全一致。OpenAPI 当前不通过运行时端点或 Swagger UI 暴露。

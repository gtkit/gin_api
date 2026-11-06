# 授权接入约定

`middleware.Auth` 验证身份并写入 `shared/auth.Principal`；`middleware.Authorize` 判断操作权限。业务模块必须显式选择自己的 `Authorizer`，通过已有 `shared/auth.Authorizer` 接口接入。已有 `bootstrap/authorizers.go` 展示了查询权威角色源的实现，不新增权限引擎、不默认放行管理员。

JWT 中的角色只随身份传播，不能替代当前权限源。角色被撤销后，后续授权查询立即按当前结果拒绝；权限源错误返回 503，拒绝访问返回 403。身份缺失返回 401。不得把数据库或权限源错误当作允许。

## 功能权限与对象归属

功能权限通过后，还必须在 application/repository 路径限制对象归属。已登录用户有某功能的权限，并不意味着可以操作其他用户的数据。对象 ID、owner_id 和会话版本都不能从请求体直接作为可信身份使用；身份取自认证上下文。

`internal/module/example` 和 `project module` 生成模板使用同一约定：

- HTTP `Deps` 必须提供 `Service`、`Auth`、`Authorizer`，缺少依赖时拒绝注册。
- 分组使用 `Auth → Authorize(authorizer, "<模块名>.access")`，再进入 handler。
- 每个查询和修改同时限定 `owner_id` 与目标 ID；不存在和属于其他用户的对象统一返回 404。
- `DELETE /<模块名>/batch` 接受 `{"ids":["规范 UUID", "..."]}`，数量为 1—100 且不能重复。在事务内按 `owner_id + id IN (...)` 删除，受影响行数不等于请求数量即整体回滚。不能先删除自己拥有的部分再返回失败。

模块接入时，应用通过原有 bootstrap 装配数据库服务和权限源，再将 `Auth` 与 `Authorizer` 交给 HTTP `Register`。示例未注册到默认服务，生成命令不修改默认路由或迁移清单。

## 已有验证与边界

回归测试覆盖 JWT 声称管理员但权限源拒绝、运行中撤销角色、权限源故障、跨用户 CRUD 和混合归属批量删除回滚。SQLmock 核对 SQL 条件及事务调用；不替代真实 MySQL 的锁、索引和并发测试。业务接入自己的资源时，应按同一约定补充对象、字段及状态权限。

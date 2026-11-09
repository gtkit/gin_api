# 可选扩展

## 独立接线编译

`make check-optional-wiring`验证default、taskqueue、upload、outbound、auditlog、all、removed七种样例。
唯一实现为`scripts/check-optional-wiring.go`，标准库Go工具按40处wiring标记恢复自有临时副本，
默认不改主工作区、不启用可选业务、不启动服务；removed只在副本中排除可选目录。
它不在check/check-ci/CI中，接入门禁须先记录冷/热成本并取得明确决定。
命令、复制白名单、离线环境及证明范围见[脚本说明](../../scripts/README.md#check-optional-wiringgo)。

## 能力与边界

这里集中保存项目内部按需启用、可整体删除的能力。使用 `internal` 保持 Go 的包访问限制；
`optional` 只表示默认未接线，不提供动态插件加载、注册表或统一扩展接口。

| 目录 | 能力 | 目录外接线标记 |
|---|---|---|
| [taskqueue](taskqueue/README.md) | 持久化队列、失败重试、周期投递 | TASKQUEUE |
| [upload](upload/README.md) | 文件上传构造、文件策略、下载响应头 | UPLOAD |
| [outbound](outbound/README.md) | 具名 HTTP 客户端、熔断、有界重试 | OUTBOUND |
| [auditlog](auditlog/README.md) | 审计记录、中间件、批量落库、表结构 | AUDITLOG |

每个能力保留自己的配置、实现、测试和说明；包名直接使用功能名。
可选包可以引用项目的通用能力，核心包默认不导入可选包。
需要进程装配的位置保留显式注释，位于 bootstrap、根配置或迁移入口。

## 构建与删除

默认主程序不导入以上四个模块，`make build` 构建主程序 `.` 时不会编译这些包。
`go build ./...`、`go test ./...` 会枚举全部 Go 包，仍会编译和检查这些模块。
`go mod tidy` 也会保留尚未删除的可选包所需依赖。

当前默认状态下，可直接删除某个能力目录，或整个本目录，再执行 `go mod tidy`。
其他功能也在使用的依赖会继续保留；不能仅凭本目录未被主程序导入就删除 go.mod 中的依赖。
目录外的注释接线不影响编译，需要彻底清理时按各模块 README 删除对应注释、模板及文档入口。

已启用的项目，先移除实际 import、字段、装配及业务调用，再删目录。
已复制到默认迁移目录的 SQL 和既有数据库内容，需要按项目实际部署状态处理。

## 启用

按目标目录 README 恢复对应注释接线，填写配置，注入实际业务并验证。
只取消某个 enabled 字段或只改 YAML 不能恢复尚未接入的代码。
派生项目修改 module 名后，注释里的 import 也要手动改成当前 module 名。

使用前还须完成对应的故障、重试、取消、关闭、权限和资源预算验收。业务幂等、Outbox/Inbox、多副本调度、资源归属/租户和可靠审计只提供设计契约，没有默认接线或虚构业务实现，见 [可选能力契约](../../docs/optional-capability-contracts.md)。默认 API 的静态契约见 [OpenAPI](../../docs/openapi.json)。

# gin_api 脚手架审查 — 发现的问题

> 审查日期：2026-10-23

---

## 目录模块化依赖

- **`bootstrap/resources.go: processLogs`**：把 robot/mail/sms/notification/logger 全部作为字段展开存储。`dispatch.Owner` 已经承担渠道聚合职责，`processLogs` 只需持有 `dispatch.Owner` 和 `logging.Owner`，现在展开四个子渠道字段，新增渠道时需要同时追加字段和 Close 调用，有遗漏风险。

---

## 配置多环境与密钥安全

- **`config/load.go: envReservedNames`**：目前只有 `APP_ROOT` 一项。后续新增进程级 `APP_*` 变量时若未同步登记，启动会失败并报"未知配置项"，排查方向被误导。代码注释已提及，属运营风险。

---

## Gin 中间件 & 统一返回

- **`cors.go: allowedRequestHeaders`**：白名单包含 `token`、`x-token`、`accesstoken` 等非标准存量头。新项目若不需要这些头，会无意间开放不必要的请求头，建议在 `SCAFFOLD.md` 的"移除 Laravel 兼容层"清单中补充此项。

---

## GORM 连接池 + 迁移

- **`mysql/open.go: WithPrepareStmt(true)`**：开启了预编译语句缓存。在 ProxySQL / PgBouncer 等连接池代理前置的场景下，可能引发"prepared statement does not exist"错误。注释和配置说明中未提及此限制。

---

## Redis 缓存规范

- **配置样例缺少 DB 用途说明**：多 DB 模式下限流/缓存/撤销分别使用不同 DB 号，但配置样例没有列出每个能力对应哪个 DB，运维修改时不清楚影响范围。

---

## 全局 panic 捕获

- **`middleware/recovery.go: recoveryPublicMessage`**：硬编码为 `"服务器内部错误"`，与 `goerr` 体系的 i18n 能力不一致。项目若后续需要多语言，这里需要单独处理。

---

## 优雅关停

- **`bootstrap/app.go: gracefulShutdown`**：函数体约 120 行，9 个编号步骤全部内联。逻辑正确，但作为脚手架母版，下游开发者阅读学习成本较高。建议提取为 `drainProducers`、`drainHTTP`、`drainConsumers` 等命名方法。

---

## Dockerfile

- **`libssl3/libcrypto3` 钉了具体版本**（`3.5.9-r0`）：可复现但不会自动跟随 Alpine 安全补丁，需要主动维护。建议在 `release-check` 中加入镜像扫描步骤，或建立定期检查机制。

---

## README 文档

- **`GOPRIVATE` 配置说明不足**：只在 `gonew` 命令前提了一次 `export GOPRIVATE`，没有说明是否需要全局配置，新项目开发者首次拉取私有依赖时会卡住。
- **各 `gtkit/*` 包对应能力没有汇总**：`go.mod` 引用约 30 个私有包，部分能力（LLM、SMS、uploader）是可选的，但没有说明不需要时如何精简，下游项目 `go.mod` 会显得很重。

---

## JWT 安全

- **pem 文件权限未在启动路径校验**：`diagnostics/keyperm_unix.go` 有 pem 权限诊断，但它属于 `doctor` 命令，不在 `server` 启动路径上强制执行。配置文件本身已校验 `0o022`，但私钥 pem 文件权限没有同等保护。建议在 `SCAFFOLD.md` 中明确要求（如 `0400`），或在启动期添加警告。

---

## 连接泄漏风险

- **`closeResources` 异常路径有限泄漏**：worker 无法退出或超时时，`closeResources` 跳过连接关闭，连接留给进程退出回收。正常关停路径不触发，异常路径下属已知取舍（代码注释有说明），不是逻辑错误，但需知晓。

---

## 过度设计

- **`router/init.go: NewRateLimitStager`**：pre/post 分阶段逻辑约 60 行 + 3 个闭包，对初期只需 IP 限流的新项目不直观。建议在 `SCAFFOLD.md` 中说明默认只需 IP 级策略，用户级策略的使用时机。
- **`go.mod` 依赖过重**：LLM、SMS、uploader 等可选能力作为编译依赖引入，建议在 `SCAFFOLD.md` 增加"可选能力与对应依赖"表格及精简方法。

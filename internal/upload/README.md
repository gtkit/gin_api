# 可选文件上传

本目录包含本地上传器构造、文件展示策略、专用响应头和测试。
普通上传、分片、对象存储等能力使用已锁定版本的 gouploader。
当前没有业务上传路由，目录外的 UPLOAD 接线默认全部注释。

| 文件 | 作用 |
|---|---|
| `uploader.go` | `NewLocalUploader`，校验必填策略并接入后台错误日志 |
| `policy.go` | 文件内联展示白名单；零值策略全部强制下载 |
| `headers.go` | `FileHeaders`，设置 nosniff 和附件下载头 |

## 普通上传入口

业务服务复用装配时创建的上传器，使用请求的 context 和文件流：

```go
result, err := uploader.UploadService().Upload(
    ctx, fileName, reader, size, "", "",
)
if err != nil {
    return err
}
// result.StorageKey 用于保存文件记录；结果中还包含文件名、大小和校验值。
```

最后两个参数是预期校验值和算法，空值使用 SDK 的默认处理。
读取 multipart 文件后，处理器仍需关闭文件流。
不要使用 `Storage().Save` 代替业务上传入口；它是底层存储操作，不执行上传服务的文件策略。

## 启用接线

从项目根目录定位示例：

```powershell
rg -n UPLOAD internal/bootstrap
```

1. **构造与关停。**
   - `internal/bootstrap/upload.go`：去掉包围 import 和实现的那一对 `/*`、`*/`。
   - `internal/bootstrap/providers.go`：恢复两个 import 声明、`infra.uploader` 字段、`uploadOptions` 和 `buildLocalUploader` 调用块。
   - 按部署填写上传根目录、分片暂存目录、单文件大小和扩展名白名单。示例值不是强制默认值；测试应使用临时目录。
   - 创建发生在 `buildServices` 之前。共享实例经 `inf.uploader` 注入业务服务，`Close` 已登记到 provider 的关闭与启动回滚流程。
2. **业务处理器与路由。**
   - `internal/bootstrap/modules.go`：恢复 upload import、`services.uploadFile/downloadFile` 两个字段和路由注册块。
   - 在 `buildServices` 中由业务模块构造处理器并赋值。没有处理器时不会注册对应路由。
   - 上传路由先执行 `middleware.MultipartBody(0)` 再认证，沿用全局请求体上限；单文件还受 `MaxFileSize` 限制。
   - 下载处理器按文件记录检查归属和权限，并按记录中的安全文件名设置 Content-Type。示例路由的 `:id` 不是文件扩展名，默认会强制附件下载；路径、文件名由业务明确控制后才考虑放宽内联策略。
3. **验证。**

```powershell
gofmt -w internal/upload internal/bootstrap
go build ./...
go vet ./...
go test -race ./internal/upload
```

若派生项目已修改 go.mod 的 module 名，恢复注释时将示例 import 中的
`github.com/gtkit/gin-api` 替换为当前 module 名。注释中的 import 不会被 Go 导入重写工具自动更新。

## 保留的安全边界

- 上传根目录、大小上限、扩展名白名单必须显式给出，缺失会返回错误。
- `FileHeaders` 始终设置 `X-Content-Type-Options: nosniff`；白名单外强制附件下载。
- 零值 Policy 不允许内联；默认策略排除 HTML 和 SVG。
- 后台错误默认进入项目 logger/v2，可用 `WithOnBackgroundError` 显式替换。
- 身份验证和文件归属授权由业务路由、处理器负责。

本地上传器关闭使用 SDK 的 `Close()`，SDK 不提供 context 或 error 返回；
后台错误仍由上面的回调记录。对象存储、分片和 STS 直传按 SDK 原生 API 接入，不另建 Provider 体系。

## 默认构建与删除

- 默认主程序不导入本目录，`make build` 构建主程序 `.` 时不包含 upload/gouploader。
- `go build ./...` 和 `go test ./...` 仍会包含本包。
- 本目录还在时，`go mod tidy` 会保留它需要的 SDK 依赖。

当前默认状态下，可直接删除本目录，再执行 `go mod tidy`。
需要清理模板时，删除 `internal/bootstrap/upload.go` 以及 providers/modules 中的 UPLOAD 注释。
文档里的相关入口位于 `SCAFFOLD.md`，删除目录时同步移除其上传说明和链接。
已经接入业务的项目，还需处理业务服务依赖、上传/下载路由及历史文件的保留策略。

通用 `MultipartBody`、请求体大小限制、认证和授权中间件继续保留。

# 可选文件上传

统一通过 `-tags optional` 编译接线，按各能力配置启用；默认构建不导入可选包。
验证入口为 `GOWORK=off go run -trimpath scripts/check-optional-wiring.go`，直接在原项目检查
默认/optional 的依赖图和构建，不复制源码、不启动服务。完整说明见 [可选能力接入](../../../docs/optional-capabilities.md)。

本目录包含本地上传器构造、文件展示策略、专用响应头和测试。
普通上传、分片、对象存储等能力使用已锁定版本的 gouploader。
当前没有业务上传路由；optional 构建可按 upload 配置创建共享上传器。

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

使用 `-tags optional` 构建，按 `config/optional.yml.example` 设置 upload.enabled、dir、chunk_dir、max_file_size 和 allowed_types。
目录、单文件上限和白名单没有安全默认，启用时必须明确配置；上传器由进程共享并在启动失败或关停时关闭。
业务仍须自行注入 inf.uploader、实现文件记录与归属授权、注册上传/下载路由。
上传路由使用 MultipartBody 和鉴权，下载路由使用 FileHeaders；不得将存储键直接视为公开授权。

## 保留的安全边界

- 上传根目录、大小上限、扩展名白名单必须显式给出，缺失会返回错误。
- `FileHeaders` 始终设置 `X-Content-Type-Options: nosniff`；白名单外强制附件下载。
- 零值 Policy 不允许内联；默认策略排除 HTML 和 SVG。
- 后台错误默认进入项目 logger/v2，可用 `WithOnBackgroundError` 显式替换。
- 身份验证和文件归属授权由业务路由、处理器负责。

本地上传器关闭使用 SDK 的 `Close()`，SDK 不提供 context 或 error 返回；
后台错误仍由上面的回调记录。对象存储、分片和 STS 直传按 SDK 原生 API 接入，不另建 Provider 体系。

## 默认构建与停用

默认主程序不导入本包，显式全仓测试仍编译本包。upload.enabled=false 不创建上传器，但不自动清理历史文件。
物理删除须同步清理 optional 装配和配置、业务调用及测试；历史文件保留由业务决定。

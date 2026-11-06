# OpenAPI 与 Go 客户端

`docs/openapi.json` 是默认 API 的可审阅契约。`internal/bootstrap/openapi*_test.go` 对照真实路由装配、认证 DTO 的字段/必填/长度、响应信封及 HTTP 错误状态；它不代替业务数据库验收。

Go 客户端位于 `internal/pkg/apiclient/client.gen.go`，生成源为 OpenAPI 与 `internal/projectinit/templates/client/runtime.tmpl`。开发构建提供：

```sh
go run -trimpath -tags scaffold . project client --check
go test -vet=off -trimpath -tags scaffold ./internal/projectinit -run TestGeneratedClientMatchesContract
```

`project client` 在目标不存在时生成，拒绝覆盖已有源码。更新时先审阅契约与模板，保存已有变更后移除旧的生成文件，再运行该命令。`--check` 只比较、零写入；禁止手改生成文件掩盖契约差异。

这是本项目的 OpenAPI 3.1 配置生成器：支持现有的具名对象、字符串/数字/布尔、数组、字典、引用和认证响应组合，不接受任意第三方 schema。未支持的方法、参数、类型和 schema 关键字会报错。minLength/maxLength 等约束留给服务端校验，生成器输出 Go 类型，不承担完整 JSON Schema 运行时验证。

```go
client, err := apiclient.New("https://api.example.com", nil)
if err != nil { return err }
result, err := client.Login(ctx, apiclient.LoginRequest{Phone: phone, Password: password})
if err != nil { return err }
_ = result.Data.AccessToken
```

客户端复用传入的 HTTP transport。没有指定客户端超时时使用 10 秒，同时尊重调用方 context；响应体上限 1 MiB，读取后关闭。重定向被拒绝，避免凭据转发；不会自动重试写请求。Logout 显式接收本次 access token，实例不保存 token。

`ChangePassword` 和 `LogoutAll` 同样显式接收访问令牌；服务端需先完成迁移并开启
`auth.session_lifecycle`，否则相应路由不存在。二者成功后不返回新令牌，客户端须清理登录态并重新登录。

`APIError` 同时提供 HTTP 状态和业务码。HTTP 200、code 非零仍返回错误；注册已经创建账号但签发令牌失败属于这种情况，调用方应按业务码提示登录，不能无条件重发注册。OpenAPI 为该返回声明独立错误分支。Error() 不携带响应正文、token 或服务端错误详情；需要展示时使用服务端已脱敏的 Message。

# 测试与验证

## 统一入口

提交前跑：

```bash
make check
```

它依次执行架构传感器、能力清单判据（含自检）、依赖清单整洁、镜像摘要固定、
发布接线、应用身份零漂移、钩子安装语义、`golangci-lint run ./...`、`go vet ./...`、
race 测试并生成聚合 coverage profile，最后校验全仓覆盖率不低于门禁。

**权威清单是 `Makefile` 的 `check` 目标**，本文只做导读。

单项与补充命令：

```bash
make test-race                          # 只跑 race 测试
make test-integration                   # 真实 MySQL 的 repository 集成测试（无 DB 时自动 SKIP）
make check-coverage COVERAGE_MIN=75     # 临时提高覆盖率门禁试跑
go test -run TestName -count=1 ./internal/...
go test -bench=. -benchmem -count=3 ./...
```

受限环境（沙箱禁止绑定回环监听端口）可排除依赖本地 listener 的测试：

```bash
ALLOW_NO_LOOPBACK=1 make check
```

正常开发与 CI 不设置该变量，始终跑完整测试。

### 覆盖率门禁

以 `Makefile` 的 `COVERAGE_MIN` 为**单一来源**（本文不重复该数字——写死会与实际门禁分叉，
而分叉时读者无从判断哪个是真的）。门禁只升不降，提高之前必须先让代码在受限环境也实际达到目标。

**阶段目标：全仓覆盖率不低于 80%。** 这个目标写在这里而门禁当前值不写，两者不矛盾：
目标是方向，很久不变；门禁值随每次抬升而变，因此只能有一处。门禁值去 `Makefile` 看。

门禁取"任意环境都能复现的可移植地板"：受限环境（`ALLOW_NO_LOOPBACK=1`）的实测值是下界，
loopback 可用的完整跑更高。取下界保证两种环境的 `make check` 都通过。

新增和修改的公开行为必须同步增加测试，不得以当前基线为由扩大测试债务。

### 带 tag 的测试口径

`internal/scaffold`、`internal/projectinit` 与 `cmd/{module,project}.go` 由 `//go:build scaffold`
门控。**测试与覆盖率入口必须带该 tag**，否则这些包根本不会被编译，它们的保护会静默消失
而 `make check` 依然全绿。`make` 的相关目标已封装好，直接跑 `make check` 即可。

## 测试分层

| 层 | 覆盖什么 |
|---|---|
| Unit | application 业务规则、值对象、工具函数 |
| Handler | `httptest` 覆盖绑定、鉴权上下文、错误映射、响应结构 |
| Repository / Integration | GORM 查询、事务、锁、schema 一致性 |
| Contract | 公开 API 响应结构、错误码、分页结构、DTO json tag |
| Regression | 修 bug 先补一个能失败的复现用例 |

集成测试用真实 MySQL 验证 repository 与 schema 一致性（配置取自 `config/env/env.yml`），
**事务内强制回滚、零持久化**，无 DB 时自动 SKIP。

## Table-Driven 的适用边界

**存在多组输入时**必须 table-driven：

```go
func TestValidateCreateUser(t *testing.T) {
    tests := []struct {
        name    string
        input   CreateUserInput
        wantErr error
    }{
        {name: "missing email", input: CreateUserInput{Name: "A"}, wantErr: ErrEmailRequired},
        {name: "ok", input: CreateUserInput{Name: "A", Email: "a@example.com"}},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := ValidateCreateUser(tt.input)
            if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
                t.Fatalf("want %v, got %v", tt.wantErr, err)
            }
            if tt.wantErr == nil && err != nil {
                t.Fatalf("unexpected error: %v", err)
            }
        })
    }
}
```

**单一场景的测试直接写断言即可**（如「启动时缺少某项配置必须失败」）——硬套 table 结构
只增加噪音，不增加覆盖。规范要可满足：一条长期无法满足的规则会让人连能满足的部分也一起忽略。

case 名说明业务场景；断言具体；覆盖 success、error、edge 三类。

## 契约声明必须有反证测试

任何契约声明（幂等、有界、线程安全、原子、不阻塞、最多一次）都必须配一个
**「若该声明为假就会失败」的测试**，否则不许写进文档或注释。

写下「重复调用是安全的」却没有测试，等于凭空承诺。同理，声称某个竞态窗口「已关闭」之前，
必须有一个「移除该机制就会失败」的测试；没有这种测试只能说「已缩窄」。

这条对**判据本身**同样适用。`make check` 里的门禁脚本有几个带 `--self-test`：
它们在临时工作树里构造一个应当失败的场景并断言判据确实失败——因为一条永远为真的判据
和一条正确的判据，在日常运行中表现完全一样。

## Mock 与外部依赖

- mock 经接口注入，不 monkey patch。
- service 测试 mock repository / provider。
- repository 测试用真实 DB（集成测试）。
- HTTP 外部依赖用 `httptest.Server`。
- 时间、随机数、ID 生成器尽量可注入——否则测试要么不确定，要么只能靠 sleep。

## 并发与 race

- 声明并发安全的类型必须有并发测试。
- goroutine 必须能经 context 或 channel 退出，测试要覆盖退出路径。
- 幂等、状态流转、回调覆盖并发重复请求。
- 涉及共享状态跑 `-race`（`make check` 已默认带上）。

## API 与错误契约

- Handler 测试断言 HTTP 状态、业务 code、客户端文案、data 结构。
- 未知错误统一为内部错误，不暴露底层字符串。
- 鉴权和限流覆盖未登录、权限不足、命中限流、正常通过四种。
- DTO / 响应变更同步文档与调用方。

## 回归要求

- 修 bug 先写失败测试或最小复现步骤。
- 修复后重跑受影响包的全量测试，再跑 `make check`。
- **测试失败不能只改断言凑绿。**
- 交付说明实际跑过的命令；没跑要说明原因。

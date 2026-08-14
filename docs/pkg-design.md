# 可复用包设计

本文约束根 `pkg/` 下的包——它们可被仓库外的项目 import。`internal/pkg/` 的分类与边界
见 [`internal-pkg.md`](internal-pkg.md)。

## 两条硬约束（有可执行判据）

### 1. 不得 import 本仓 `internal/`

包一旦被外部项目引用，而它自身 import 了本仓 `internal/`，外部项目的编译会因 Go 的
internal 可见性规则直接失败。违反这条的包只是**看起来**可复用：在本仓内怎么跑都正常，
只有真正被外部引用时才暴露，而那时已经发布出去了。

### 2. 不产生日志，运行期事件经调用方注入的回调暴露

包内直接写日志会把使用者绑定到某个具体日志栈，而换一个库只是换一个绑定对象——问题不变。
把事件交出去则彻底消除这个选择：调用方接入自己的栈，包本身对日志一无所知。

`pkg/ratelimit` 的做法是 `Observer` 接口 + `ObserverFunc`：

```go
type Observer interface {
    BackendPhaseChanged(from, to HealthPhase)
    // ...
}

// ObserverFunc 让一组函数满足 Observer，省去为一次性用途定义类型。
// 各字段可为 nil，此时对应事件被忽略。
type ObserverFunc struct { ... }
```

未注入回调时包正常工作且不产生任何输出。

**回调必须在锁外发出。** 持锁回调把「Observer 只准写日志」变成了没有编译器背书的隐式契约，
违反者的代价是全部判定被阻塞或进程死锁。`pkg/ratelimit` 的实现是先 `Unlock()`
再 `notify(events)`，因此 Observer 重入包的公开方法不会死锁、慢 Observer 不会阻塞判定路径，
且通知顺序与状态迁移的发生顺序一致。

## 依赖选型

1. **零外部依赖优先**：能用标准库实现的不引入第三方。
2. **选型顺序**：标准库 → `gtkit` 原生包 → 业界事实标准 → 其他第三方。
   `gtkit` 同名包若仅是事实标准库的轻封装，可直连事实标准库。
3. **JSON 固定例外**：用 `github.com/gtkit/json` 或 `github.com/gtkit/json/v2`，
   不用 `encoding/json`。
4. **日志**：见上面的硬约束——可复用包不写日志。需要业务日志的场景说明本项目统一用
   `github.com/gtkit/logger`。

## 包结构

```
pkgname/
├── pkgname.go        # 核心类型和接口定义
├── options.go        # Functional Options
├── errors.go         # 包级 sentinel error
├── doc.go            # 包级文档
├── pkgname_test.go
├── example_test.go   # 示例测试（出现在 GoDoc 中）
├── internal/         # 内部实现细节，不对外暴露
├── README.md
└── CHANGELOG.md
```

## Functional Options

```go
type Option func(*config)

type config struct {
    timeout  time.Duration
    retries  int
    observer Observer      // 事件回调，而不是 logger
}

func defaultConfig() *config {
    return &config{
        timeout: 10 * time.Second,
        retries: 3,
    }
}

func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }
func WithObserver(o Observer) Option     { return func(c *config) { c.observer = o } }

func New(opts ...Option) *Client {
    cfg := defaultConfig()
    for _, opt := range opts {
        opt(cfg)
    }
    return &Client{cfg: cfg}
}
```

必需依赖用具名 struct 让缺失变成编译错误；可选行为用 option 让默认值成为正常路径。

## 错误设计

```go
// 包级 sentinel，调用方用 errors.Is 判断
var (
    ErrTimeout    = errors.New("pkgname: operation timeout")
    ErrInvalidArg = errors.New("pkgname: invalid argument")
)

// 需要携带上下文的错误用自定义类型
type ValidationError struct {
    Field   string
    Message string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("pkgname: validation failed on %s: %s", e.Field, e.Message)
}
```

错误消息带包名前缀：调用方看到的是一长串聚合错误，没有前缀就无从判断它来自哪一层。

## 零值语义必须是安全的或被拒绝

策略类配置的零值要么有明确且安全的含义，要么在构造期就被拒绝。

`pkg/ratelimit` 的选择是**零值即拒绝**：一个忘记配置的策略不会变成「不限流」。
默认放行是这类组件最危险的失效形态——它在正常情况下完全看不出来，
只在被攻击时才表现为「限流没生效」。

## 必须包含

- **README.md**：安装、快速上手、完整示例、API 概览
- **GoDoc 注释**：每个导出的 type/func/const/var
- **Example 测试**：核心功能至少一个（出现在 GoDoc 中）
- **测试覆盖率 ≥ 80%**
- **CHANGELOG.md**：语义化版本，每版列出 Added/Changed/Fixed
- **LICENSE**

## API 稳定性

- 导出的接口一旦发布，不随意改签名。
- 新增方法用新接口（接口组合），不改旧接口。
- 废弃的 API 用 `// Deprecated:` 标记。
- 破坏性变更留在现有 major 线以 MINOR 发布，且只允许 **fail-closed** 形态：
  旧的危险行为变成显式报错，并在错误信息里指引出路，不静默改变语义。
  CHANGELOG 用 ⚠ 标注并附迁移说明。

## context

- 所有可能耗时的操作接受 `context.Context` 作为第一个参数。
- 尊重 cancel 与 deadline。
- 不在内部创建新的 `context.Background()`，用调用方传入的——
  例外是那些必须脱离调用方生命周期的操作（如关停期的 flush），
  这时用 `context.WithoutCancel` 并显式加超时，且必须在 GoDoc 里说明原因。

## 并发安全

- GoDoc 明确标注是否 goroutine-safe，**两种都要写**——不写等于让调用方去猜。
- 声明线程安全的，必须有并发测试（见 [`testing.md`](testing.md) 的「契约声明必须有反证测试」）。

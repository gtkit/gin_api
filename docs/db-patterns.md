# 数据访问规范

本文约束 repository 层与 GORM 实践。分层与事务边界的总规则见 [`architecture.md`](architecture.md)，
迁移的执行流程见 [`migration.md`](migration.md)。

## Repository 模式

接口定义在使用方（application 层的 `ports.go`），实现在 `internal/repository/`：

```go
// internal/module/<m>/application/ports.go
type UserRepository interface {
    Create(ctx context.Context, user *models.User) error
    GetByID(ctx context.Context, id int64) (*models.User, bool, error)
    Update(ctx context.Context, userID int64, fields map[string]any) error
}
```

规则：

- 入参用 repo 自己的 query/options 类型，或 application port 的中立输入类型。
- **禁止用带 `binding`/`form` tag 的 HTTP 请求体当 repo 查询条件**——那会让 HTTP 协议细节
  一路渗到数据访问层，且请求体字段的可选性语义与查询条件的语义并不一致。
- Redis / MQ 等外部介质的 repo 不适用 GORM 规则，但依赖方向相同。

## 数据访问统一经 `mdbCtx(ctx)`

```go
// internal/repository/useraccount/useraccount.go
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
    return dbtx.DB(ctx, r.db)
}
```

`dbtx.DB(ctx, fallback)` 从 context 取事务句柄，没有则回退到注入的默认 DB。

**所有 GORM 访问走 `r.mdbCtx(ctx)`，禁止绕过它直接用 `r.db`。** 绕过的后果是那条语句
不在调用方开启的事务里——它会成功提交，然后在事务回滚时留下来，成为一条没有对应主记录的孤儿数据。
这类缺陷不报错、不进日志，只在对账时被发现。

`dbtx.DB` 同时用 `WithContext(ctx)` 绑定调用方的取消和 deadline；因此业务查询统一走
`mdbCtx(ctx)` 即可，不需要每条查询再重复 `WithContext`。不要用 `context.Background()` /
`TODO()` 替换请求 ctx，也不要在查询链的 `Session` / `WithContext` 中抹掉它。

架构检查已接入 `scripts/check-repository-context.go`：用 Go 类型信息检查
`internal/repository/...` 当前构建条件下的生产文件，唯一豁免为底层 `dbtx` 包。
检查 `mdbCtx` 确实返回 `dbtx.DB(自身 context 参数, 接收者 DB 字段)`，再追踪经典
`*gorm.DB` 调用的局部来源，接受统一入口、其查询链/局部别名及受管 `Transaction` 内的句柄，
拒绝裸 DB、Background/TODO 和无法证明的来源。

这是保守的开发期检查：局部变量的所有赋值都参与判断；跨函数返回值、动态 `Session`
选项、任意 `Scopes` 回调等无法证明时会报错，需要保持上述入口和直接可追踪的写法。
它不分析测试文件、其他构建条件、GORM 泛型 API、反射/unsafe 或第三方内部执行，也不能
证明驱动一定及时响应取消；实际取消行为仍需对应数据库集成验证。执行方式见
[仓储 context 检查](../scripts/README.md#check-repository-contextgo)。

## 必须遵守

1. 数据访问走 `mdbCtx(ctx)`。
2. 禁止全局 DB 变量，`*gorm.DB` 经构造函数注入。
3. 列表、大表、跨关联查询、对外响应只需部分字段时显式 `.Select(...)` / `.Omit(...)`；
   简单按主键读取完整聚合可不强制。
4. 批量操作分批，单批默认不超过 500。
5. 列表查询必须有 LIMIT，默认最大 100。
6. 软删除用 `gorm.DeletedAt`。

## 部分更新用 map，不用 struct

```go
func (r *Repository) UpdateProfile(ctx context.Context, userID int64, fields map[string]any) error {
    if len(fields) == 0 {
        return nil
    }
    return r.mdbCtx(ctx).Model(&models.User{}).
        Where("id = ?", userID).
        Updates(fields).Error
}
```

`Updates(struct)` 会**忽略零值字段**，因此无法表达「把这个字段改成 0 / 空字符串 / false」。
用它承载「只改显式传了的字段」的语义，结果是用户把某个字段清空的请求被静默丢弃。

由 application 根据请求的指针字段决定哪些列纳入 map——指针能区分「没传」与「传了零值」，
这个区分必须在 DTO 层就保住，到了 repo 层已经无从恢复。

## 事务

单 repo / 聚合内部的多语句原子写可在 repo 内做：

```go
func (r *OrderRepository) CreateWithItems(ctx context.Context, order *models.Order, items []*models.OrderItem) error {
    return r.mdbCtx(ctx).Transaction(func(tx *gorm.DB) error {
        if err := tx.Create(order).Error; err != nil {
            return fmt.Errorf("create order: %w", err)
        }
        for _, item := range items {
            item.OrderID = order.ID
        }
        if err := tx.CreateInBatches(items, 100).Error; err != nil {
            return fmt.Errorf("create order items: %w", err)
        }
        return nil
    })
}
```

跨多个 repo 的事务由 application 经 `TxManager.Do` 编排（实现是 `dbtx.NewManager(db)`），
`*gorm.DB` 不出 repository 侧。

## 错误处理

不要把 `gorm.ErrRecordNotFound` 泄漏到 application——那会让 application 为了判断
「没找到」而 import gorm，而 application 禁止 import gorm（传感器强制）。

单一「找不到」场景优先 comma-ok：

```go
func (r *ChannelRepository) GetByCode(ctx context.Context, code string) (models.Channel, bool, error) {
    var channel models.Channel
    err := r.mdbCtx(ctx).Where("code = ?", code).First(&channel).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return models.Channel{}, false, nil
    }
    if err != nil {
        return models.Channel{}, false, fmt.Errorf("get channel by code: %w", err)
    }
    return channel, true, nil
}
```

## Redis-only / 外部介质仓储

- 不实现 `mdbCtx`，直接注入 Redis / MQ client。
- 不参与 GORM 事务；需要原子性时用 Lua、SetNX、原子计数等介质自身能力。
- 按职责/聚合组织，不强套「一表一 repo」。

## 防御性查询

- WHERE 的用户输入必须参数化，禁止字符串拼接。
- 动态字段名（排序列、过滤列）必须走白名单——它们进不了参数占位符，
  是 SQL 注入在 ORM 项目里仅存的入口。
- Preload/Joins 注意 N+1。
- 大表查询加索引或用 EXPLAIN 验证。
- offset 过大时考虑游标分页。

## 连接池

连接池参数在 `config/env/env.yml` 的 `database` 段配置（`maxidleconn` / `maxopenconn` /
`maxlifeseconds` / `maxidletimeseconds`），由 `internal/platform/data` 装配，不在业务代码里设置。

连接建立时会实测 vendor/版本与会话的时区、字符集、sql_mode——这些在不同 MySQL 部署上
默认值不同，而它们的差异会表现为「同一份代码在测试库正常、在生产库行为不一致」。
启动时验一次，比事后从数据里反推便宜得多。

## 迁移

- **GORM `AutoMigrate` 仅限本地临时验证，生产禁止依赖它自动改表。**
- 本项目用 `gtkit/migratex`，迁移文件在 `database/migrations/`，执行走内置 `migrate` CLI
  （`make migrate-up` / `make migrate-status`），连库验证用 `make migrate-verify`。
- 迁移 SQL 使用包含 Up/Down 区段的单文件格式，上线前须经 DBA 审阅。
- 迁移必须可审计：up/down 或等价回滚方案、索引、唯一约束、默认值、comment、数据修复步骤。

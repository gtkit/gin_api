# database

数据库迁移、DDL 与 schema 快照。

## 目录

| 路径 | 作用 | 进镜像 |
|------|------|--------|
| `migrations/` | goose 格式的迁移历史，**唯一的执行来源** | 是 |
| `ddl/` | 由 GORM 模型生成的严格 CREATE TABLE，供 `make ddl diff` 检测模型漂移 | 否 |
| `schema/baseline.snapshot` | 迁移执行后实际 schema 的期望快照，供 `make migrate-verify` 比对 | 否 |

`migrations/` 之外的内容不进构建上下文（见 `.dockerignore`）。

## 运行环境要求

| 项 | 值 | 来源 |
|----|----|------|
| 最低 MySQL 版本 | **8.0** | `database.min_version`（留空取默认 8.0） |
| 受支持 vendor | Oracle MySQL、Percona Server | 启动期实测 `SELECT VERSION(), @@version_comment` |
| 字符集 | `utf8mb4` | `database.charset` |
| Collation | `utf8mb4_unicode_ci` | `database.collation` |
| 表引擎 | `InnoDB` | 基线迁移的表选项里显式写死 |

**MariaDB 与未知 vendor 默认拒绝启动。** 它的 `10.x` 数值上大于 MySQL `8.0`，只比版本号会被误判为"足够高"，但两者 SQL 语义并不等价（默认 collation、`sql_mode` 默认值、JSON 实现都不同）。放开需要单独的兼容 change。

表选项在迁移里显式声明而不继承服务器默认值：同一份 SQL 在不同默认 collation 的实例上，比较与排序结果会不同，而这种差异只在数据量上来之后才暴露。

## 迁移文件命名

`<纯数字版本>_<名称>.sql`，含 `-- +goose Up` 与 `-- +goose Down` 区段。

MySQL 的 DDL 隐式提交，因此 DDL 迁移必须带 `-- +goose NO TRANSACTION`——否则 goose 的事务包装是误导性的（中途失败不会回滚已建的表）。`migrate lint` 会强制这一点。

同理不要用 `-- +goose StatementBegin/StatementEnd` 块，`migrate lint` 会拒绝：语句必须能独立解析。

## 常用命令

```bash
make migrate-status              # 查看迁移状态
make migrate-pretend             # 预览待执行的 Up SQL
make migrate-lint                # 迁移文件静态检查
make migrate-up                  # 执行迁移
make migrate-verify              # 连库验证：自带 MySQL 容器，往返 + schema 比对
```

## 迁移被中断后必须核对 schema

MySQL 的 DDL 非事务性，**且不随客户端断连回滚**。因此任何形式的中断——Ctrl-C、
CI job timeout、容器被杀、网络断开——都可能留下"schema 已变更、迁移版本表未记录"
的状态：命令报了失败，数据库侧却已经执行完成。

迁移路径本身不施加客户端 I/O 超时（`data.OpenMigrationDB`），所以大表 DDL 不会
因为跑得久而被掐断；墙钟上界由执行者承担。但上面几种中断仍然可能发生，中断后
按下面三步核对，不要直接重跑：

```bash
# 1. 迁移库记录到哪一版
make migrate-status

# 2. 实际 schema 是什么样（对着被中断的那个迁移改的表）
mysql -h<host> -u<user> -p <db> -e 'SHOW CREATE TABLE <table>\G'

# 3. 比对后二选一：
#    - 实际 schema 已是目标态而版本表落后 → 该版本的变更已生效，需要把版本表
#      对齐到它（见下方「存量库基线接管」，那里说明了逃生口的定性与限制）
#    - 实际 schema 仍是旧态 → 变更未生效，可以正常重跑 make migrate-up
```

判断依据放在实际 schema 上而不是命令的退出码：退出码只说明客户端那侧发生了
什么，说明不了服务端做完了多少。

## 重新生成基线

基线内容**由模型生成，不手写**。重新生成的完整步骤：

```bash
# 1. 起一个和门禁同版本的 MySQL（migrate-verify 用的是 mysql:8.0）
# 2. 让 config/env/env.yml 指向它
# 3. 由已注册的 GORM 模型生成严格 DDL
go run . make ddl --all --force        # 产出到 database/ddl/

# 4. 把 ddl/ 的 CREATE TABLE 按外键顺序拼进基线迁移，并补上表选项
#    （users 必须先于 user_profiles，后者有指向前者的外键；回滚取逆序）
#    表选项：ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci

# 5. 重新生成 schema 快照并跑验证
make migrate-verify
```

改动基线迁移后 `database/schema/baseline.snapshot` 必须同步更新，否则 `make migrate-verify` 会失败并逐行指出差异。

## 检测模型漂移

```bash
go run . make ddl diff          # 比较 GORM 模型与 database/ddl/ 的差异
```

模型改了但没写迁移时，这条命令会报出差异。它检测的是"模型 vs DDL"，而 `make migrate-verify` 检测的是"迁移产出 vs 期望 schema"——两条链路都要通，才能保证模型、迁移、实际库三者一致。

## 升级策略

- **遵循 expand/contract**：先加新列/新索引（expand）、双写并回填、再删旧列（contract）。不要在一个迁移里同时加和删。
- **`down` 不是生产回滚方案**。它只服务于本地开发与 `make migrate-verify` 的往返验证。生产上 DDL 隐式提交、数据已经变化，回滚要靠新的前向迁移（contract 的逆操作）加数据修复，而不是 `migrate down`。
- 迁移必须可审计：索引、唯一约束、默认值、comment 都写清楚。

## 存量库基线接管

**当前尚未提供安全的接管能力。**

正确的接管必须在迁移锁的**同一临界区内**原子完成三件事：读取实际 schema → 与基线期望逐项比对（engine、charset/collation、列的类型/顺序/默认值/auto_increment、各类索引的名称与列顺序、外键）→ 写入版本表。拆成"锁外校验 + 锁内标记"是 TOCTOU，中间可能有并发迁移改变状态。

该能力需要在上游迁移库中实现，待其提供后再接线。

> ⚠️ **`migrate mark-applied` 是低层逃生口，不是接管步骤。**
> 它只把版本写进版本表、**完全不校验 schema**（其自身帮助文本即写着 "can hide schema drift"）。
> 若实际 schema 与基线不一致而用它标记为已应用，后续所有迁移都会在一个错误的基线上执行，
> 且错误会在很久之后才以难以定位的形式暴露。
> 只有在人工逐项确认过 schema 等价之后，才可作为最后手段使用。

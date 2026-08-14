# 数据库迁移

**目录布局、运行环境要求（MySQL 版本/vendor/字符集/collation）、文件命名与 schema 快照
以 [`../database/README.md`](../database/README.md) 为准**，本文不重复那些内容——
写两遍必然分叉，而分叉时读者无从判断哪个是真的。

本文只写变更流程与脚本要求。

## 变更流程

1. 先改 `internal/models` 的模型定义。
2. 生成迁移草稿：`make migrate-migration create_xxx_table dev`。
3. **人工补齐**索引、唯一约束、默认值、comment 与回滚逻辑——生成器产出的是草稿，不是成品。
4. 静态检查：`make migrate-lint`。
5. 预览将执行的 SQL：`make migrate-pretend`。
6. 评估数据规模与上线窗口。
7. 连库验证：`make migrate-verify`（自带固定版本 MySQL 容器，空库 up→down→up 并比对实际 schema）。
8. 执行：`make migrate-up`（非 dev 环境走二进制并二次确认）。
9. 查看状态：`make migrate-status`。

`make check` 里的 `check-migration-files` 会离线断言迁移文件完整性，不连库。

## 脚本要求

正式迁移必须写清：

- up 逻辑
- **down 逻辑或等价回滚方案**
- 索引、唯一约束、默认值、comment
- 数据修复步骤与可重复执行策略
- 大表变更的锁表时长、回填方式、灰度与回滚影响

表选项（引擎、字符集、collation）在迁移里**显式声明**，不继承服务器默认值：
同一份 SQL 在不同默认 collation 的实例上，比较与排序结果会不同，
而这种差异只在数据量上来之后才暴露。

## AutoMigrate

GORM `AutoMigrate` **只允许本地临时验证**。生产环境禁止依赖它自动改表——
它不产生可审计的变更记录，不支持回滚，且对既有列的处理策略在不同 GORM 版本间并不稳定。

`database/migrations/` 是唯一的执行来源。

## 上线前

迁移草稿须经 DBA 审阅后再执行。本项目的数据库 schema 由外部维护，
迁移文件是变更的**提案与记录**，不是自动执行的指令。

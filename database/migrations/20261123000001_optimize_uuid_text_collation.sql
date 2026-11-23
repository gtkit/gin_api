-- UUID 文本列只存规范化小写 ASCII，使用二进制排序可缩小索引比较成本。
--
-- 迁移前提：files 表已经由受管 schema 创建。example_records 由外部 DDL 管理，
-- 其新建脚本已同步；已有 example_records 表需由 schema owner 单独排期。
-- MySQL DDL 可能持有表锁，
-- DBA 应在目标数据量、online DDL 能力和维护窗口确认后执行。
-- +goose NO TRANSACTION

-- +goose Up
-- example_records is managed by database/ddl/create_example_records_table.sql and
-- is intentionally not altered by this goose migration.

ALTER TABLE `files`
  MODIFY COLUMN `id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT '主键编码（uuid v7）';

-- +goose Down
ALTER TABLE `files`
  MODIFY COLUMN `id` char(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '主键编码（uuid v7）';

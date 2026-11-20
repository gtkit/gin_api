-- files 表：上传对象的归属记录（file 模块）。
--
-- 草稿状态：与本仓其余迁移一致，须经 DBA 审阅后执行。schema 由外部维护。
--
-- 唯一键 (owner_id, storage_key) 是合并幂等的兜底：同一会话重复合并
-- 返回同一存储键，记录侧不产生第二行。无秒传时每次上传生成新 uuid 键，
-- 软删除保留旧行不会与后续上传冲突。
--
-- MySQL 的 DDL 隐式提交，goose 的事务包装在这里是误导性的：
-- 中途失败不会回滚已建的表。显式声明 NO TRANSACTION，让语义与现实一致。
-- +goose NO TRANSACTION

-- +goose Up
CREATE TABLE `files` (
  `id` char(36) NOT NULL COMMENT '主键编码（uuid v7）',
  `owner_id` bigint NOT NULL COMMENT '归属用户ID',
  `file_name` varchar(255) NOT NULL COMMENT '清理后的原始文件名',
  `storage_key` varchar(512) NOT NULL COMMENT '存储对象键',
  `driver` varchar(32) NOT NULL COMMENT '上传时存储驱动（local/s3/aliyun/tencent/huawei）',
  `bucket` varchar(128) NOT NULL DEFAULT '' COMMENT '上传时桶名，local 为空',
  `size` bigint NOT NULL COMMENT '文件字节数',
  `checksum` varchar(128) NOT NULL DEFAULT '' COMMENT '整文件校验和（默认 sha256）',
  `mime_type` varchar(128) NOT NULL DEFAULT '' COMMENT '按扩展名推断的 MIME',
  `created_at` datetime COMMENT '创建时间',
  `updated_at` datetime COMMENT '最后更新时间',
  `deleted_at` datetime COMMENT '删除时间',
  PRIMARY KEY (`id`),
  UNIQUE INDEX `uk_files_owner_storage` (`owner_id`, `storage_key`),
  INDEX `idx_files_owner` (`owner_id`, `deleted_at`, `created_at`, `id`)
-- 表选项显式声明而不继承服务器默认值：同一份 SQL 在不同默认 collation 的实例上，
-- 比较与排序结果会不同，而这种差异只在数据量上来之后才暴露。
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
-- 直接 DROP：files 无其它表引用；历史存储对象的清理由业务决定。
DROP TABLE IF EXISTS `files`;
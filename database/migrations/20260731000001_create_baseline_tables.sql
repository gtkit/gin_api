-- 基线迁移：由 `make ddl --all` 从已注册的 GORM 模型生成，再补上显式表选项。
-- 不手写、不虚构；重新生成的方式见 database/README.md。
--
-- 表选项显式钉死 ENGINE/CHARSET/COLLATE，不继承服务器默认值——
-- 同一份 SQL 在不同默认 collation 的实例上，比较与排序结果会不同。

-- MySQL 的 DDL 隐式提交，goose 的事务包装在这里是误导性的：
-- 中途失败不会回滚已建的表。显式声明 NO TRANSACTION，让语义与现实一致。
-- +goose NO TRANSACTION

-- +goose Up
CREATE TABLE `users` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `nickname` varchar(50) NOT NULL DEFAULT '' COMMENT '用户昵称',
  `gender` tinyint(1) NOT NULL DEFAULT 0 COMMENT '性别 0-未知 1-男 2-女',
  `avatar` varchar(255) NOT NULL DEFAULT '' COMMENT '头像URL',
  `phone` varchar(20) COMMENT '手机号',
  `email` varchar(100) COMMENT '邮箱',
  `password` varchar(255) NOT NULL COMMENT '密码',
  `status` tinyint(1) NOT NULL DEFAULT 1 COMMENT '用户状态 1-正常 2-禁用 3-删除',
  `vip_status` tinyint(1) NOT NULL DEFAULT 0 COMMENT '会员状态 0-非会员 1-会员 2-已过期 3-已取消',
  `vip_level` tinyint(1) NOT NULL DEFAULT 0 COMMENT '会员级别',
  `vip_expire_at` timestamp COMMENT '会员到期时间',
  `follow` tinyint(1) NOT NULL DEFAULT 0 COMMENT '是否关注公众号 0-未关注 1-关注',
  `created_at` datetime COMMENT '创建时间',
  `updated_at` datetime COMMENT '最后更新时间',
  `deleted_at` datetime COMMENT '删除时间',
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_users_phone` (`phone`),
  UNIQUE INDEX `idx_users_email` (`email`),
  INDEX `idx_users_status` (`status`),
  INDEX `idx_users_vip_status` (`vip_status`),
  INDEX `idx_users_vip_level` (`vip_level`),
  INDEX `idx_users_follow` (`follow`),
  INDEX `idx_users_created_at` (`created_at`),
  INDEX `idx_users_updated_at` (`updated_at`),
  INDEX `idx_users_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE IF EXISTS `users`;

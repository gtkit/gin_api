-- 基线迁移：由 `make ddl --all` 从已注册的 GORM 模型生成，再补上显式表选项。
-- 不手写、不虚构；重新生成的方式见 database/README.md。
--
-- 建表顺序由外键决定：user_profiles 引用 users，users 必须先建；
-- 回滚顺序相应取逆序。
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

CREATE TABLE `user_profiles` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `user_id` bigint NOT NULL COMMENT '用户ID',
  `register_ip` varchar(45) NOT NULL DEFAULT '' COMMENT '注册IP',
  `last_login_ip` varchar(45) NOT NULL DEFAULT '' COMMENT '最后登录IP',
  `last_login_time` timestamp NOT NULL COMMENT '最后登录时间',
  `device_system` varchar(50) NOT NULL DEFAULT '' COMMENT '手机系统',
  `device_model` varchar(100) NOT NULL DEFAULT '' COMMENT '手机型号',
  `device_id` varchar(100) NOT NULL DEFAULT '' COMMENT '设备识别码',
  `source` varchar(50) NOT NULL DEFAULT 'direct' COMMENT '用户来源',
  `channel_id` bigint NOT NULL COMMENT '发布渠道ID',
  `invite_code` varchar(20) NOT NULL DEFAULT '' COMMENT '邀请码',
  `invited_by` bigint(20) DEFAULT 0 COMMENT '邀请人ID',
  `remark` varchar(255) NOT NULL DEFAULT '' COMMENT '备注',
  `extra_data` json COMMENT '扩展JSON数据',
  `created_at` datetime COMMENT '创建时间',
  `updated_at` datetime COMMENT '最后更新时间',
  `deleted_at` datetime COMMENT '删除时间',
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_user_profiles_user_id` (`user_id`),
  INDEX `idx_user_profiles_device_id` (`device_id`),
  INDEX `idx_channel` (`channel_id`),
  INDEX `idx_user_profiles_invite_code` (`invite_code`),
  INDEX `idx_user_profiles_invited_by` (`invited_by`),
  INDEX `idx_user_profiles_created_at` (`created_at`),
  INDEX `idx_user_profiles_updated_at` (`updated_at`),
  INDEX `idx_user_profiles_deleted_at` (`deleted_at`),
  CONSTRAINT `fk_users_profile` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- user_deactivations 保存注销前的个人信息快照（手机号、邮箱、昵称、来源 IP），
-- 明文存储，用于注销后的申诉、风控回溯与合规举证。
--
-- **保留期限与清理策略必须由使用方按所在司法辖区确定后再上线**：
-- 各地对个人信息的最长保留期、删除权响应时限、以及"注销后仍需留存多久"的
-- 行业要求并不一致，脚手架替你选一个数字只会让它在某些辖区是错的。
--
-- 需要落定的三件事：保留多久、到期由谁清理、谁能读这张表。
-- 前两者可用定时任务按 created_at 清理；第三者靠库账号权限，而不是应用层判断——
-- 应用层的读取限制绕不过直连数据库的人。
CREATE TABLE `user_deactivations` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `user_id` bigint NOT NULL COMMENT '被注销的用户ID',
  `original_phone` varchar(20) NOT NULL DEFAULT '' COMMENT '注销前手机号',
  `original_email` varchar(100) COMMENT '注销前邮箱',
  `original_nickname` varchar(50) NOT NULL DEFAULT '' COMMENT '注销前昵称',
  `reason` varchar(255) NOT NULL DEFAULT '' COMMENT '注销原因（用户填写）',
  `channel` varchar(32) NOT NULL DEFAULT '' COMMENT '注销渠道（app/h5/admin 等）',
  `operator_ip` varchar(64) NOT NULL DEFAULT '' COMMENT '发起注销的来源IP',
  `created_at` datetime COMMENT '创建时间',
  `updated_at` datetime COMMENT '最后更新时间',
  `deleted_at` datetime COMMENT '删除时间',
  PRIMARY KEY (`id`),
  INDEX `idx_user_deactivations_user_id` (`user_id`),
  INDEX `idx_user_deactivations_channel` (`channel`),
  INDEX `idx_user_deactivations_created_at` (`created_at`),
  INDEX `idx_user_deactivations_updated_at` (`updated_at`),
  INDEX `idx_user_deactivations_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE IF EXISTS `user_deactivations`;
DROP TABLE IF EXISTS `user_profiles`;
DROP TABLE IF EXISTS `users`;

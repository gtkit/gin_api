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
);
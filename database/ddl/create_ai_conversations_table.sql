CREATE TABLE `ai_conversations` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `user_id` bigint NOT NULL COMMENT '归属用户ID',
  `title` varchar(120) NOT NULL DEFAULT '' COMMENT '会话标题',
  `provider` varchar(32) NOT NULL DEFAULT '' COMMENT '最近使用的厂商',
  `model` varchar(64) NOT NULL DEFAULT '' COMMENT '最近使用的模型',
  `message_count` int NOT NULL DEFAULT 0 COMMENT '消息条数',
  `prompt_tokens` bigint NOT NULL DEFAULT 0 COMMENT '累计输入token',
  `completion_tokens` bigint NOT NULL DEFAULT 0 COMMENT '累计输出token',
  `last_message_at` datetime COMMENT '最后消息时间',
  `created_at` datetime COMMENT '创建时间',
  `updated_at` datetime COMMENT '最后更新时间',
  `deleted_at` datetime COMMENT '删除时间',
  PRIMARY KEY (`id`),
  INDEX `idx_ai_conversations_user_active` (`user_id`,`last_message_at` DESC),
  INDEX `idx_ai_conversations_created_at` (`created_at`),
  INDEX `idx_ai_conversations_updated_at` (`updated_at`),
  INDEX `idx_ai_conversations_deleted_at` (`deleted_at`)
);

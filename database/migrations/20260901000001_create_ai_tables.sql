-- AI 对话：会话头（ai_conversations）与消息（ai_messages）。
--
-- 两者是一个聚合：会话是聚合根，消息是内部实体，读写收口在
-- internal/repository/aiconversation。会话上的 message_count / prompt_tokens /
-- completion_tokens 是冗余计数，必须与消息写入在同一事务里更新，否则会分叉。
--
-- 刻意**不建外键**：ai_messages.conversation_id 只做逻辑关联。理由是软删除——
-- 会话走 gorm.DeletedAt 软删，外键的级联语义与"行还在但被标记删除"对不上，
-- 而 RESTRICT 会让软删本身失败。归属与存在性由仓储层保证。
--
-- 草稿状态：与本仓其余迁移一致，须经 DBA 审阅后执行。
-- 数据规模提示：ai_messages 的写入速率 = 对话轮次 × 2，正文列是 mediumtext，
-- 上线前按业务量评估保留期与归档策略；长期运行建议按 created_at 归档。

-- MySQL 的 DDL 隐式提交，goose 的事务包装在这里是误导性的：
-- 中途失败不会回滚已建的表。显式声明 NO TRANSACTION，让语义与现实一致。
-- +goose NO TRANSACTION

-- +goose Up
CREATE TABLE `ai_conversations` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `user_id` bigint NOT NULL COMMENT '归属用户ID',
  `title` varchar(120) NOT NULL DEFAULT '' COMMENT '会话标题',
  -- 最近一次成功回复所用的厂商/模型；逐条消息的真实取值在 ai_messages 上。
  `provider` varchar(32) NOT NULL DEFAULT '' COMMENT '最近使用的厂商',
  `model` varchar(64) NOT NULL DEFAULT '' COMMENT '最近使用的模型',
  `message_count` int NOT NULL DEFAULT 0 COMMENT '消息条数',
  -- bigint 而不是 int：长期会话的累计输入 token 能越过 21 亿，
  -- 32 位溢出后变成负数且不报错。
  `prompt_tokens` bigint NOT NULL DEFAULT 0 COMMENT '累计输入token',
  `completion_tokens` bigint NOT NULL DEFAULT 0 COMMENT '累计输出token',
  -- 会话列表按它倒序；不复用 updated_at——改标题不该让会话跳到最前面。
  `last_message_at` datetime COMMENT '最后消息时间',
  `created_at` datetime COMMENT '创建时间',
  `updated_at` datetime COMMENT '最后更新时间',
  `deleted_at` datetime COMMENT '删除时间',
  PRIMARY KEY (`id`),
  -- 会话列表的唯一查询形态：某用户最近聊过什么。
  INDEX `idx_ai_conversations_user_active` (`user_id`,`last_message_at` DESC),
  INDEX `idx_ai_conversations_created_at` (`created_at`),
  INDEX `idx_ai_conversations_updated_at` (`updated_at`),
  INDEX `idx_ai_conversations_deleted_at` (`deleted_at`)
-- 表选项显式声明而不继承服务器默认值：同一份 SQL 在不同默认 collation 的实例上，
-- 比较与排序结果会不同，而这种差异只在数据量上来之后才暴露。
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE `ai_messages` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `conversation_id` bigint NOT NULL COMMENT '会话ID',
  -- 冗余归属人：越权校验与按用户清理都需要它，否则每条消息读取都要联表。
  `user_id` bigint NOT NULL COMMENT '归属用户ID',
  `role` tinyint(1) NOT NULL COMMENT '角色 1-system 2-user 3-assistant',
  -- mediumtext 而不是 text：长文档粘贴 + 长回复很容易越过 64KB，
  -- 而严格 sql_mode 下那是整条写入失败——发生在用户已经看到内容之后。
  `content` mediumtext NOT NULL COMMENT '消息正文',
  `reasoning` mediumtext COMMENT '推理过程',
  `provider` varchar(32) NOT NULL DEFAULT '' COMMENT '厂商',
  `model` varchar(64) NOT NULL DEFAULT '' COMMENT '模型',
  `prompt_tokens` int NOT NULL DEFAULT 0 COMMENT '输入token',
  `completion_tokens` int NOT NULL DEFAULT 0 COMMENT '输出token',
  `total_tokens` int NOT NULL DEFAULT 0 COMMENT '总token',
  -- stop / length / content_filter / aborted：被截断的回答与正常结束的回答
  -- 在正文上看不出区别，这是唯一能分辨的依据。
  `finish_reason` varchar(32) NOT NULL DEFAULT '' COMMENT '终止原因',
  `created_at` datetime COMMENT '创建时间',
  `updated_at` datetime COMMENT '最后更新时间',
  `deleted_at` datetime COMMENT '删除时间',
  PRIMARY KEY (`id`),
  -- 组装上下文时按会话取最近 N 条：(conversation_id, id) 让它走覆盖到主键的
  -- 倒序扫描，不需要回表排序。
  INDEX `idx_ai_messages_conversation` (`conversation_id`,`id`),
  INDEX `idx_ai_messages_user` (`user_id`),
  INDEX `idx_ai_messages_created_at` (`created_at`),
  INDEX `idx_ai_messages_updated_at` (`updated_at`),
  INDEX `idx_ai_messages_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
-- 先删消息再删会话：与建表相反的顺序，即便将来补上外键也成立。
DROP TABLE IF EXISTS `ai_messages`;

DROP TABLE IF EXISTS `ai_conversations`;

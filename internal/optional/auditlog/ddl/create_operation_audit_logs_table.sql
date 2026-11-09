CREATE TABLE `operation_audit_logs` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `occurred_at` datetime NOT NULL COMMENT '发生时间',
  `method` varchar(10) NOT NULL COMMENT 'HTTP方法',
  `route` varchar(255) NOT NULL COMMENT '路由模板',
  `status` int NOT NULL COMMENT 'HTTP状态码',
  `latency_ms` bigint NOT NULL COMMENT '处理时延毫秒',
  `user_id` bigint NOT NULL COMMENT '用户ID',
  `request_id` varchar(64) NOT NULL COMMENT '请求ID',
  `client_ip` varchar(64) NOT NULL COMMENT '客户端IP',
  PRIMARY KEY (`id`),
  INDEX `idx_operation_audit_logs_occurred_at` (`occurred_at`),
  INDEX `idx_operation_audit_logs_user_occurred` (`user_id`, `occurred_at`)
);

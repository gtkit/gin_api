-- 写操作审计表：谁、何时、对哪条路由、结果如何。
--
-- 采集是异步有界、尽力而为的（队列满载丢弃并计数告警）；
-- 记录不可变、无软删除，保留期清理见本模块 Repository.DeleteOlderThan。
-- 刻意不存请求体/响应体/query string——脱敏事故的既往来源，
-- 访问日志已按各自受控策略覆盖。
--
-- 草稿状态：与本仓其余迁移一致，须经 DBA 审阅后执行。schema 由外部维护。
-- 数据规模提示：写入速率 = 挂了审计中间件的请求速率；上线前按业务量
-- 评估保留期与分区/归档策略，高写入量场景建议按月归档。

-- MySQL 的 DDL 隐式提交，goose 的事务包装在这里是误导性的：
-- 中途失败不会回滚已建的表。显式声明 NO TRANSACTION，让语义与现实一致。
-- +goose NO TRANSACTION

-- +goose Up
CREATE TABLE `operation_audit_logs` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `occurred_at` datetime NOT NULL COMMENT '发生时间',
  `method` varchar(10) NOT NULL COMMENT 'HTTP方法',
  -- 路由模板（低基数，如 /api/v1/users/:id）；未匹配路由回退原始路径（构造点截断）。
  `route` varchar(255) NOT NULL COMMENT '路由模板',
  `status` int NOT NULL COMMENT 'HTTP状态码',
  `latency_ms` bigint NOT NULL COMMENT '处理时延毫秒',
  -- 未认证请求为 0。
  `user_id` bigint NOT NULL COMMENT '用户ID',
  `request_id` varchar(64) NOT NULL COMMENT '请求ID',
  `client_ip` varchar(64) NOT NULL COMMENT '客户端IP',
  PRIMARY KEY (`id`),
  -- 保留期清理按时间扫；审计查询按"某用户在某时段做了什么"扫。
  INDEX `idx_operation_audit_logs_occurred_at` (`occurred_at`),
  INDEX `idx_operation_audit_logs_user_occurred` (`user_id`, `occurred_at`)
-- 表选项显式声明而不继承服务器默认值：同一份 SQL 在不同默认 collation 的实例上，
-- 比较与排序结果会不同，而这种差异只在数据量上来之后才暴露。
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
-- 直接 DROP：审计表无其它表引用。执行前确认保留期内的记录已按合规要求归档。
DROP TABLE IF EXISTS `operation_audit_logs`;

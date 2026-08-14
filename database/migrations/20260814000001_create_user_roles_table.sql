-- 角色关联表：role.admin 判定的权威角色源。
--
-- **这是示例能力的配套迁移，可按需删除**（删除清单见 SCAFFOLD.md）。
--
-- 草稿状态：与本仓其余迁移一致，须经 DBA 审阅后执行。schema 由外部维护。
--
-- 为什么是独立关联表而不是 users 上的一个 role 列：
-- 单列只能表达一个角色，而"一个用户多个角色"是这类需求几乎必然的下一步——
-- 届时要同时改表结构、改判定并迁移存量数据。关联表从一开始就支持多角色，
-- 代价只是多一张表；可删性也更好（删表 + 删目录，而不是动 users 的模型与既有查询）。

-- MySQL 的 DDL 隐式提交，goose 的事务包装在这里是误导性的：
-- 中途失败不会回滚已建的表。显式声明 NO TRANSACTION，让语义与现实一致。
-- +goose NO TRANSACTION

-- +goose Up
CREATE TABLE `user_roles` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `user_id` bigint NOT NULL COMMENT '用户ID',
  -- 角色标识由业务定义。它与授权注册表里的 ID（如 role.admin）不是同一个东西：
  -- 前者是本表的值，后者是路由声明引用的标识。两者可能不再对应。
  `role` varchar(64) NOT NULL COMMENT '角色标识',
  `created_at` datetime COMMENT '创建时间',
  PRIMARY KEY (`id`),
  -- 唯一索引兼作幂等保证：重复授予同一角色不会产生第二行。
  -- 它同时是判定查询 (user_id, role) 的覆盖索引。
  UNIQUE INDEX `uk_user_roles_user_role` (`user_id`, `role`),
  INDEX `idx_user_roles_role` (`role`)
-- 表选项显式声明而不继承服务器默认值：同一份 SQL 在不同默认 collation 的实例上，
-- 比较与排序结果会不同，而这种差异只在数据量上来之后才暴露。
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
-- 直接 DROP：这张表是示例，且没有其它表引用它。
DROP TABLE IF EXISTS `user_roles`;

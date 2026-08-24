CREATE TABLE `user_roles` (
  `id` bigint AUTO_INCREMENT COMMENT '主键编码',
  `user_id` bigint NOT NULL COMMENT '用户ID',
  `role` varchar(64) NOT NULL COMMENT '角色标识',
  `created_at` datetime COMMENT '创建时间',
  PRIMARY KEY (`id`),
  UNIQUE INDEX `uk_user_roles_user_role` (`user_id`,`role`)
);
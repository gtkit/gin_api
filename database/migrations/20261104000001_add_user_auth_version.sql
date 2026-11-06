-- Additive rollout: migrate, deploy all version-aware replicas, then enable auth.session_lifecycle.
-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE `users`
  ADD COLUMN `auth_version` bigint NOT NULL DEFAULT 0 COMMENT '认证会话版本' AFTER `password`;

-- +goose Down
-- Requires disabled traffic and invalidation of previous signing keys; dropping this
-- column loses revocation state. Never use Down alone as a session-safe rollback.
ALTER TABLE `users` DROP COLUMN `auth_version`;

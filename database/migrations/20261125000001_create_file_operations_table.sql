-- Apply before enabling the new writers. Review pending operations before rollback.
-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE file_operations (
  id CHAR(36) NOT NULL,
  parent_id CHAR(36) NULL,
  owner_id BIGINT NOT NULL,
  kind VARCHAR(12) NOT NULL,
  business_key VARCHAR(80) NOT NULL,
  file_id CHAR(36) NOT NULL,
  driver VARCHAR(32) NOT NULL,
  bucket VARCHAR(128) NOT NULL,
  namespace CHAR(64) NOT NULL,
  storage_key VARCHAR(512) NOT NULL,
  file_name VARCHAR(255) NOT NULL,
  size BIGINT NOT NULL,
  checksum VARCHAR(128) NOT NULL,
  algorithm VARCHAR(16) NOT NULL,
  state VARCHAR(12) NOT NULL,
  generation BIGINT UNSIGNED NOT NULL,
  lease_until DATETIME(6) NULL,
  next_at DATETIME(6) NOT NULL,
  failure VARCHAR(32) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_file_op_business (kind, owner_id, business_key),
  UNIQUE KEY uk_file_op_attempt (parent_id, generation),
  KEY idx_file_op_due (namespace, state, next_at),
  KEY idx_file_op_file (owner_id, file_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
-- The rollback operator must stop writers and reconcile all outstanding operations.
DROP TABLE file_operations;

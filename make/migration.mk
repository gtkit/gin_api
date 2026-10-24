migrate-verify: ## 连库验证迁移：自带固定版本 MySQL 容器，空库 up→down→up + 实际 schema 比对
	@ bash ./scripts/migrate-verify.sh

check-migration-files: ## 离线断言迁移文件完整性（不连库；镜像门禁第一步同款判据）
	@ bash ./scripts/check-migration-files.sh database/migrations

MIGRATE = $(if $(filter dev,$(ENV)),$(RUN) .,$(BIN_DIR)/$(APP_NAME))

## 环境占位目标：允许 `make migrate-xxx <env>` 把环境名作为第二个目标传入(dev/test/prod)
prod:
	@:

# migrate-migration 走位置参数: make migrate-migration create_xxx_table dev
# 迁移名 = 命令行去掉目标名与环境名后的第一个词; 声明为空目标, 避免 make 误当目标报 "No rule to make target"
ifneq ($(filter migrate-migration,$(MAKECMDGOALS)),)
MIGRATION_NAME := $(firstword $(filter-out migrate-migration dev test prod,$(MAKECMDGOALS)))
ifneq ($(MIGRATION_NAME),)
$(eval $(MIGRATION_NAME): ; @:)
endif
endif

migrate-migration: ## 生成建表迁移(仅本地): make migrate-migration create_xxx_table dev
	@ if [ -z "$(MIGRATION_NAME)" ]; then echo "用法: make migrate-migration create_<table>_table dev"; exit 1; fi
	$(RUN) . make migration $(MIGRATION_NAME) --from-model

migrate-lint: ## 迁移文件静态检查
	$(MIGRATE) migrate lint --strict

migrate-pretend: ## 预览待执行的 Up SQL
	$(MIGRATE) migrate pretend

migrate-up: ## 执行迁移建表(非 dev 走二进制并二次确认)
	@if [ "$(ENV)" = "dev" ]; then \
		$(RUN) . migrate up; \
	else \
		echo "⚠️  即将用 $(BIN_DIR)/$(APP_NAME) 对配置指向的库执行 migrate up"; \
		echo "    请先确认已备份、已用 make migrate-pretend prod 预览。继续请输入 yes:"; \
		read ans; [ "$$ans" = "yes" ] || { echo "已取消"; exit 1; }; \
		$(BIN_DIR)/$(APP_NAME) migrate up; \
	fi

migrate-status: ## 查看迁移状态
	$(MIGRATE) migrate status

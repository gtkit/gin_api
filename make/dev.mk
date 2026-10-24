hooks: ## 安装 git 钩子（pre-push 跑 make check-ci）
	## 门禁存在不等于门禁被执行。外部冻结的部署流水线只负责部署、不承载质量 job，
	## 而本仓自己的质量 CI（.github/workflows/check.yml）调的正是 make check-ci ——
	## 与本地同一入口，因此两处不会漂移。本地这一道是提交前的第一个检查点。
	## 幂等可重跑；已存在非本仓的同名钩子时报错中止，不覆盖。
	@ bash ./scripts/install-hooks.sh

dev-up: ## 拉起本地开发依赖（MySQL/Redis）并等待健康
	@ test -f .env || { echo "缺少 .env：先 cp compose.env.example .env 并填写密码"; exit 1; }
	docker compose up -d --wait

dev-down: ## 停止本地开发依赖，**保留数据卷**
	docker compose down

check-compose-app: ## 仅用隔离环境解析Compose并验证app契约，不创建容器
	python3 ./scripts/check-compose-app.py --app-name "$(APP_NAME)"

dev-app-up: ## 显式构建并启动app及健康依赖；先准备隔离配置、钥和迁移
	@ test -f "$(COMPOSE_ENV_FILE)" || { echo "missing Compose env file" >&2; exit 1; }
	$(APP_COMPOSE_ENV) $(APP_COMPOSE) build --pull=false app
	$(APP_COMPOSE_ENV) $(APP_COMPOSE) up --pull never -d --wait app

dev-app-up-existing: ## 仅启动app，复用已有DB/Redis（不创建同类依赖容器）
	@ test -f "$(COMPOSE_ENV_FILE)" || { echo "missing Compose env file" >&2; exit 1; }
	$(APP_COMPOSE_ENV) $(APP_COMPOSE) build --pull=false app
	$(APP_COMPOSE_ENV) $(APP_COMPOSE) up --pull never --no-deps -d --wait app

dev-app-down: ## 仅停止并移除app；不停止DB/Redis，不删除数据卷
	$(APP_COMPOSE_ENV) $(APP_COMPOSE) stop app
	$(APP_COMPOSE_ENV) $(APP_COMPOSE) rm -f app

dev-reset: ## 删除本地开发依赖及其数据卷（不可恢复，需二次确认）
	@ echo "⚠️  这会删除本地开发库的**全部数据**（gin-api-dev-mysql-data / gin-api-dev-redis-data）。"
	@ echo "    确认请输入 yes:"
	@ read ans; [ "$$ans" = "yes" ] || { echo "已取消"; exit 1; }; \
	docker compose down -v

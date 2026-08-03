.PHONY: dev build check check-ci check-architecture check-openapi check-coverage check-generator check-appmeta check-package-vars security-source security-release dev-up dev-down dev-reset gen-appmeta gittag tag test-race test-coverage test-integration image tool vet port kill-port migrate-migration migrate-lint migrate-pretend migrate-up migrate-status prod test

## 应用身份的唯一来源。APP_NAME / APP_DISPLAY_NAME 由 project.mk 提供，
## internal/appmeta 由 gen-appmeta 从同一来源生成，check-appmeta 保证二者零差异。
## 注意：module path 与 COVERAGE_MIN 都不属于身份，不在 project.mk 里。
include project.mk

## 服务监听端口（可覆盖：make port PORT=9000）
PORT ?= 8008

ENV = $(firstword $(filter-out $@,$(MAKECMDGOALS)))

BIN_DIR := ./bin

Linux := CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN := go run -tags="sonic"
BUILD_FLAGS := -tags="sonic" -ldflags "-s -w"
## 构建mac版本,需要安装upx
# 多个 tag 的正确写法: 多个 tag 之间用逗号或空格分隔
#go build -tags=sonic,debug ./...
#go build -tags="sonic debug" ./...


dev: ## 本地启动 HTTP server
ifeq ($(filter migrate-%,$(MAKECMDGOALS)),)
	$(RUN) . server
else
	@:
endif

## 查看监听在 $(PORT) 端口的进程
port:
	lsof -nP -iTCP:$(PORT) -sTCP:LISTEN

## 杀掉监听在 $(PORT) 端口的进程
kill-port:
	@pids=$$(lsof -nP -iTCP:$(PORT) -sTCP:LISTEN -t); \
	if [ -z "$$pids" ]; then \
		echo "no process is listening on :$(PORT)"; \
	else \
		echo "killing pid(s) on :$(PORT): $$pids"; \
		kill -9 $$pids; \
	fi

## 构建linux版本,需要安装upx
build:
	@mkdir -p $(BIN_DIR)
	$(Linux) go build $(BUILD_FLAGS) -o $(BIN_DIR)/$(APP_NAME) . && upx -9 $(BIN_DIR)/$(APP_NAME)

GO_TARGETS ?= ./...
LINT_TARGETS ?= ./...
COVERAGE_PROFILE ?= .cache/coverage.out
# 覆盖率门禁：取“任意环境都能复现的可移植地板”，只升不降。
# remove-unused-infra-packages 删除 15 个零引用包与死脚本后基线上移：
# 受限环境（ALLOW_NO_LOOPBACK=1，跳过 loopback 监听类测试）实测 62.5%，是下界；
# loopback-allowed 完整跑（CI 正常网络）待 CI 复核（删包前为 61.8%，删包后应不低于受限值）。
# 门禁取 62.0%——低于受限下界，保证受限沙箱与 CI 两种环境 make check 都能通过。
# 阶段目标：62 -> 65 -> 72 -> 80；每次提高前必须先让代码在“受限环境”也实际达标。
COVERAGE_MIN ?= 62.0
tool: ## Lint Go code with the installed golangci-lint
	@ echo "▶️ golangci-lint run"
	golangci-lint run $(LINT_TARGETS)
	@ echo "✅ golangci-lint run"

vet: ## Run go vet
	go vet $(GO_TARGETS)

test-race: ## Run race-enabled tests
	go test -race -count=1 -timeout=5m $(GO_TARGETS)

test-integration: ## Run real-database integration tests locally (set REQUIRE_DB_INTEGRATION=1 to turn skips into failures)
	go test -tags integration -race -count=1 -timeout=5m ./internal/repository/...

image: ## Build the Docker image locally, tagged with the current commit short sha (requires Docker; servers do not run Docker)
	## APP_NAME 只决定 builder 阶段的产物名；容器内路径固定为 /app/bin/server（exec-form
	## ENTRYPOINT 不做变量展开，退回 shell form 会让 shell 成为 PID 1 并破坏优雅关停）。
	## GOPROXY 透传本机设置：容器内默认 proxy 在部分网络不可达，回退 direct 又需要
	## builder 镜像里并不存在的 git。
	docker build \
		--build-arg APP_NAME=$(APP_NAME) \
		--build-arg GOPROXY=$$(go env GOPROXY) \
		-t $(APP_NAME):$$(git rev-parse --short HEAD) .

test-coverage: ## Run race-enabled tests and write an aggregate coverage profile
	ALLOW_NO_LOOPBACK=$(ALLOW_NO_LOOPBACK) ./scripts/test-coverage.sh $(COVERAGE_PROFILE) $(GO_TARGETS)

check-coverage: test-coverage ## Enforce the aggregate statement coverage baseline
	./scripts/check-coverage.sh $(COVERAGE_PROFILE) $(COVERAGE_MIN)

check-architecture: ## 校验分层架构边界
	@ ./scripts/check-architecture.sh

check-openapi: ## 校验 OpenAPI 文档结构及其与真实 Gin 路由的一致性
	go test -count=1 -run '^TestOpenAPIContract$$' ./internal/router

check-generator: ## 脚手架防腐冒烟：导出发布候选工作树生成模块并验证编译/传感器/goimports（含门禁自检）
	@ bash ./scripts/check-generator.sh --self-test
	@ bash ./scripts/check-generator.sh --verify-scope
	@ bash ./scripts/check-generator.sh

migrate-verify: ## 连库验证迁移：自带固定版本 MySQL 容器，空库 up→down→up + 实际 schema 比对
	@ bash ./scripts/migrate-verify.sh

check-migration-files: ## 离线断言迁移文件完整性（不连库；镜像门禁第一步同款判据）
	@ bash ./scripts/check-migration-files.sh database/migrations

gen-appmeta: ## 由 project.mk 生成 internal/appmeta 身份常量（改身份先改 project.mk）
	@ bash ./scripts/gen-appmeta.sh

dev-up: ## 拉起本地开发依赖（MySQL/Redis）并等待健康
	@ test -f .env || { echo "缺少 .env：先 cp compose.env.example .env 并填写密码"; exit 1; }
	docker compose up -d --wait

dev-down: ## 停止本地开发依赖，**保留数据卷**
	docker compose down

dev-reset: ## 删除本地开发依赖及其数据卷（不可恢复，需二次确认）
	@ echo "⚠️  这会删除本地开发库的**全部数据**（gin-api-dev-mysql-data / gin-api-dev-redis-data）。"
	@ echo "    确认请输入 yes:"
	@ read ans; [ "$$ans" = "yes" ] || { echo "已取消"; exit 1; }; \
	docker compose down -v

security-source: ## 供应链门禁（日常）：发布候选快照的漏洞与密钥扫描
	@ bash ./scripts/security-gate.sh source

security-release: ## 供应链门禁（发版前）：追加全历史密钥扫描；禁止 SKIP_VULNCHECK
	@ bash ./scripts/security-gate.sh release

check-package-vars: ## 包级变量重绑定检查（每处必须删除或在登记表中认领）
	@ GOWORK=off go run ./scripts/check-package-vars.go internal cmd

check-appmeta: ## 校验 internal/appmeta 与 project.mk 零差异（防身份漂移）
	@ bash ./scripts/gen-appmeta.sh --check

MIGRATE = $(if $(filter dev,$(ENV)),$(RUN) .,$(BIN_DIR)/$(APP_NAME))

## 环境占位目标：允许 `make migrate-xxx <env>` 把环境名作为第二个目标传入(dev/test/prod)
prod test:
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

check: check-architecture check-package-vars check-appmeta check-openapi tool vet check-coverage ## Run all required pre-submit checks

check-ci: check check-generator ## CI 质量门禁（含脚手架防腐冒烟）


gittag:
	@set -e; \
	: "① 前置校验——任何一步不满足都不得进入发布流程"; \
	if [ -n "$$(git status --porcelain)" ]; then \
		echo "工作区或索引不干净，发布前必须完全干净："; git status --short; exit 1; \
	fi; \
	branch=$$(git symbolic-ref --short HEAD 2>/dev/null) || { \
		echo "当前处于 detached HEAD，拒绝发布：无法确定要推送的分支"; exit 1; }; \
	current=$$(grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' version.go | head -n1 | tr -d 'v'); \
	if [ -z "$$current" ]; then echo "version.go 中未找到版本号"; exit 1; fi; \
	echo "$$current" | grep -qE '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$$' || { \
		echo "当前版本 $$current 不是无前导零的稳定 SemVer"; exit 1; }; \
	maj=$$(echo $$current | cut -d. -f1); min=$$(echo $$current | cut -d. -f2); patch=$$(echo $$current | cut -d. -f3); \
	new="v$$maj.$$min.$$(expr $$patch + 1)"; \
	if git rev-parse -q --verify "refs/tags/$$new" >/dev/null; then \
		echo "tag $$new 已存在，拒绝重复发布"; exit 1; \
	fi; \
	printf "Bump: v%s -> %s (branch %s)\n" "$$current" "$$new" "$$branch"; \
	: "② 先在本地生成 release commit——但此时绝不推送、绝不打 tag"; \
	sed -E -i.bak 's/(const Version = ")([^"]+)(")/\1'"$$new"'\3/' version.go; \
	rm -f version.go.bak; \
	git add version.go; \
	git commit -m "chore(release): $$new"; \
	: "③ 在**最终 commit** 上跑全部门禁。失败时保留本地 commit、不建 tag、不碰远端"; \
	if ! $(MAKE) check check-generator security-release; then \
		echo ""; \
		echo "发布门禁失败。本地 release commit 已保留，未创建 tag、未推送任何内容。"; \
		echo "修复后可 git reset --soft HEAD~1 撤销该 commit，或修好后重跑 make tag。"; \
		exit 1; \
	fi; \
	: "④ 门禁全绿后才创建 annotated tag"; \
	git tag -a "$$new" -m "release $$new"; \
	: "⑤ 用显式 refspec 原子推送分支与 tag"; \
	: "   不依赖 push.default / 远端 HEAD 推导；服务端不支持 atomic 或分支非 fast-forward 时整体失败，"; \
	: "   绝不退化成两次 push——那会留下「有 release commit、无 tag」的半发布状态"; \
	git push --atomic origin "HEAD:refs/heads/$$branch" "refs/tags/$$new:refs/tags/$$new"; \
	printf "Done: %s pushed to %s\n" "$$new" "$$branch"

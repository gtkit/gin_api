.PHONY: dev build check check-ci check-architecture check-openapi check-coverage check-generator gittag tag test-race test-coverage test-integration image tool vet port kill-port migrate-migration migrate-lint migrate-pretend migrate-up migrate-status prod test

APP=gin_api

## 服务监听端口（可覆盖：make port PORT=9000）
PORT ?= 8008

ENV = $(firstword $(filter-out $@,$(MAKECMDGOALS)))

BIN_DIR := ./bin
IMAGE ?= app-client

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
	$(Linux) go build $(BUILD_FLAGS) -o $(BIN_DIR)/$(APP) . && upx -9 $(BIN_DIR)/$(APP)

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
	docker build -t gin_api:$$(git rev-parse --short HEAD) .

test-coverage: ## Run race-enabled tests and write an aggregate coverage profile
	ALLOW_NO_LOOPBACK=$(ALLOW_NO_LOOPBACK) ./scripts/test-coverage.sh $(COVERAGE_PROFILE) $(GO_TARGETS)

check-coverage: test-coverage ## Enforce the aggregate statement coverage baseline
	./scripts/check-coverage.sh $(COVERAGE_PROFILE) $(COVERAGE_MIN)

check-architecture: ## 校验分层架构边界
	@ ./scripts/check-architecture.sh

check-openapi: ## 校验 OpenAPI 文档结构及其与真实 Gin 路由的一致性
	go test -count=1 -run '^TestOpenAPIContract$$' ./internal/router

check-generator: ## 脚手架防腐冒烟：导出当前工作树生成模块并验证编译/传感器/goimports（含门禁自检）
	@ bash ./scripts/check-generator.sh --self-test
	@ bash ./scripts/check-generator.sh

MIGRATE = $(if $(filter dev,$(ENV)),$(RUN) .,$(BIN_DIR)/$(APP))

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
		echo "⚠️  即将用 $(BIN_DIR)/$(APP) 对配置指向的库执行 migrate up"; \
		echo "    请先确认已备份、已用 make migrate-pretend prod 预览。继续请输入 yes:"; \
		read ans; [ "$$ans" = "yes" ] || { echo "已取消"; exit 1; }; \
		$(BIN_DIR)/$(APP) migrate up; \
	fi

migrate-status: ## 查看迁移状态
	$(MIGRATE) migrate status

check: check-architecture check-openapi tool vet check-coverage ## Run all required pre-submit checks

check-ci: check check-generator ## CI 质量门禁（含脚手架防腐冒烟）


gittag:
	git tag --sort=-version:refname | head -1

tag:
	@current=$$(grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' version.go | head -n1 | tr -d 'v'); \
	if [ -z "$$current" ]; then echo "version not found in version.go"; exit 1; fi; \
	maj=$$(echo $$current | cut -d. -f1); \
	min=$$(echo $$current | cut -d. -f2); \
	patch=$$(echo $$current | cut -d. -f3); \
	newpatch=$$(expr $$patch + 1); \
	new="v$$maj.$$min.$$newpatch"; \
	printf "Bump: v%s -> %s\n" "$$current" "$$new"; \
	sed -E -i.bak 's/(const Version = ")([^"]+)(")/\1'"$$new"'\3/' version.go; \
	git add version.go; \
	git commit -m "chore(release): $$new"; \
	printf "Release: %s\n" "$$new"; \
	git push origin HEAD; \
	git tag -a "$$new" -m "release $$new"; \
	printf "Tag: %s\n" "$$new"; \
	git push origin "$$new"; \
	printf "Done\n"
	rm -f version.go.bak

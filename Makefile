.PHONY: dev build check check-ci hooks check-hooks check-build-context check-architecture check-image-digests check-security-eval check-release-wiring release-check check-coverage check-generator check-appmeta check-package-vars security-source security-release dev-up dev-down dev-reset gen-appmeta tag test-race test-coverage test-integration image tool vet port kill-port migrate-migration migrate-lint migrate-pretend migrate-up migrate-status prod test

## 应用身份的唯一来源。APP_NAME / APP_DISPLAY_NAME 由 project.mk 提供，
## internal/appmeta 由 gen-appmeta 从同一来源生成，check-appmeta 保证二者零差异。
## 注意：module path 与 COVERAGE_MIN 都不属于身份，不在 project.mk 里。
include project.mk
# 供应链工具的固定版本与摘要（GOLANGCI_LINT_VERSION / *_IMAGE）。
include security/toolchain.mk

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
# 验证口径的构建约束。
#
# `scaffold` 门控着模块生成器（internal/scaffold）与项目实例化器
# （internal/projectinit）——它们是开发期工具，不进服务二进制：`module` 会重写
# internal/bootstrap/providers.go，`project init` 带 --force-reinit-git 且会永久
# 丢弃 git 历史，两者对派生项目都不存在正确用法。
#
# 但**验证口径必须包含它们**：这些包的测试锁定着 module-scaffold-wiring 与
# project-instantiation 两组需求。只加构建约束而不在这里带上 tag，那些测试就不再
# 被编译，两组需求会静默失去保护而 `make check` 依然全绿——比不做隔离更危险。
#
# 因此：vet / test / coverage / lint 带 tag；build 目标与 Dockerfile 不带。
GO_BUILD_TAGS ?= scaffold
COVERAGE_PROFILE ?= .cache/coverage.out
# 覆盖率门禁：取“任意环境都能复现的可移植地板”，只升不降。
#
# 两条路径的实测值（isolate-loopback-tests 后首次真实可复现）：
#   受限路径 ALLOW_NO_LOOPBACK=1（强制跳过绑端口测试）：70.3%  ← 下界
#   正常路径（loopback 可用）：                          74.1%
#
# 此前注释里的“受限环境实测 62.5%”是**无效数字**：当时 test-coverage.sh 按测试名
# 黑名单分组，而名单里的三个测试早已随代码删除，`grep -Ev` 一个都没排除掉——
# 所谓受限路径实际就是全量跑，那个数字是正常环境的数字。现在受限判定由
# testutil.RequireLoopback 在测试内部完成，标志在任意机器上都真正生效，
# 上面两个数字因此可以被任何人重新测出来，而不是只能被声称。
#
# 门禁取 70.0。**每批新增依赖 Redis 的能力都会压低受限下界**：miniredis 要绑
# 回环端口，受限路径整包跳过，于是新增代码抬高了分母却没抬高受限分子。
# 认证、指标、单实例租约三批都遇到过这一点，每次都是靠补一套**不碰网络的
# 手写替身测试**把受限覆盖率拉回来的（见 internal/pkg/{tokenstore,singleton}
# 的 logic_test.go）。
#
# 因此新增此类能力时的固定动作：miniredis 版覆盖真实语义，替身版覆盖故障
# 处置与纯逻辑。两者不是重复——替身能精确制造 miniredis 难触发的故障。
#
# 下一次动这个数字之前必须先把受限实测抬上去，而不是直接改门禁。
# 阶段目标：70 -> 75 -> 80；每次提高前必须先让代码在**受限路径**实际达标。
COVERAGE_MIN ?= 70.0
tool: ## Lint Go code with the installed golangci-lint
	## 版本必须与 security/toolchain.mk 声明一致。
	## 声明了却不执行等于没声明：工具版本漂移会让同一份代码在不同机器上得到
	## 不同的检查结果，而那种差异最先表现为"我这里是好的"。
	@ bash ./scripts/check-tool-version.sh golangci-lint "$(GOLANGCI_LINT_VERSION)"
	@ echo "▶️ golangci-lint run ($(GOLANGCI_LINT_VERSION))"
	golangci-lint run $(LINT_TARGETS)
	@ echo "✅ golangci-lint run"

vet: ## Run go vet
	go vet -tags $(GO_BUILD_TAGS) $(GO_TARGETS)

test-race: ## Run race-enabled tests
	go test -tags $(GO_BUILD_TAGS) -race -count=1 -timeout=5m $(GO_TARGETS)

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
	ALLOW_NO_LOOPBACK=$(ALLOW_NO_LOOPBACK) GO_BUILD_TAGS=$(GO_BUILD_TAGS) ./scripts/test-coverage.sh $(COVERAGE_PROFILE) $(GO_TARGETS)

check-coverage: test-coverage ## Enforce the aggregate statement coverage baseline
	./scripts/check-coverage.sh $(COVERAGE_PROFILE) $(COVERAGE_MIN)

check-architecture: ## 校验分层架构边界
	@ ./scripts/check-architecture.sh

check-capability-inventory: ## 校验 SCAFFOLD.md 的预置能力清单穷尽
## 先自检后实检：自检为每个已声明的 scope 造一个未登记条目并断言判据失败，
## 再断言登记后通过。缺了它，一个 scope 的枚举实现写坏（返回空）时实检会全绿——
## 判据本身失效恰恰不会有任何症状，而那正是本次扩围要消除的失败模式。
	@ ./scripts/check-capability-inventory.sh --self-test
	@ ./scripts/check-capability-inventory.sh

## 单独跑一次**不带 tag** 的命令树断言。
##
## 必须单独一步：日常测试口径带 $(GO_BUILD_TAGS)，而在那个口径下 module/project
## 本就应当在场，"缺席"断言只可能在默认口径下成立。不单独跑，它永远不会在门禁里
## 执行——一个从不执行的断言不是保护，是装饰。
check-scaffold-isolation: ## 断言默认构建的命令树不含脚手架命令
	go test -count=1 -run TestDefaultBuildExcludesScaffoldCommands ./cmd/

## 脚手架入口：模块生成器与项目实例化器由 `scaffold` 构建约束门控，不在服务二进制里。
## 经 make 目标封装 tag，使用者不必记住它——两个入口形态一致，少一处能记错的地方。
module: ## Generate a business module (usage: make module NAME=order ARGS="--field title:string")
	@ test -n "$(NAME)" || { echo "usage: make module NAME=<module-name> [ARGS=\"--field title:string\"]" >&2; exit 2; }
	go run -tags $(GO_BUILD_TAGS) . module $(NAME) $(ARGS)

project-init: ## Instantiate a new project from this scaffold mother (usage: make project-init NAME=your_project ARGS="--dry-run")
	@ test -n "$(NAME)" || { echo "usage: make project-init NAME=<project-name> [ARGS=\"--dry-run\"]" >&2; exit 2; }
	go run -tags $(GO_BUILD_TAGS) . project init --name $(NAME) $(ARGS)

hooks: ## 安装 git 钩子（pre-push 跑 make check-ci）
	## 门禁存在不等于门禁被执行。CI 侧只部署、不承载质量 job（外部冻结契约），
	## 所以本地这一道是代码进入共享历史前唯一的自动检查点。
	## 幂等可重跑；已存在非本仓的同名钩子时报错中止，不覆盖。
	@ bash ./scripts/install-hooks.sh

check-hooks: ## 校验钩子安装语义（临时仓库实跑，不碰本机 .git/hooks）
	@ bash ./scripts/check-hooks.sh

check-build-context: ## 校验主二进制依赖的源码目录都被 .dockerignore 放行
	## 零成本静态检查，因此进日常门禁：漏放行的后果是镜像构建编译失败，
	## 而那一步只在 security-release 里跑——只在发版时才告诉你的问题，
	## 等于让每次发版都变成一次排雷（pkg/ 就这么潜伏到了发版当天）。
	@ bash ./scripts/check-build-context.sh

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


check-tidy: ## 依赖清单必须整洁
	## 手工跑过的检查必须进门禁：只在记得的时候跑，等于它不存在。
	## 实测代价——删掉 mailx 代码后 go.mod 里的依赖没清，而 make check 全绿。
	@ GOWORK=off go mod tidy -diff

check-security-eval: ## 安全判定逻辑自检（它是唯一决定发布能否通过的逻辑）
	@ GOWORK=off go run ./scripts/security-eval.go --self-test

check-image-digests: ## 所有镜像引用必须钉不可变摘要
	@ bash ./scripts/check-image-digests.sh --self-test
	@ bash ./scripts/check-image-digests.sh

check-restricted: ## 在禁止 bind(2) 的容器里实跑测试（受限环境的**事实**判定）
	@ bash ./scripts/check-restricted.sh

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

check: check-architecture check-capability-inventory check-scaffold-isolation check-tidy check-package-vars check-image-digests check-security-eval check-release-wiring check-appmeta check-hooks check-build-context tool vet check-coverage ## Run all required pre-submit checks

check-ci: check check-generator ## CI 质量门禁（含脚手架防腐冒烟）

release-check: ## 发布门禁：日常门禁 + 数据库/迁移真实验证 + 供应链
	## 比 `make check` 多出的部分都依赖 Docker 或真实数据库。
	## `make check` 刻意不依赖它们——那是为了任何环境都能复现；
	## 但发布本就允许依赖（供应链门禁已经在用），把数据库验证留在发布路径之外，
	## 意味着仓储层与迁移的回归可以直接进入发布。
	@ $(MAKE) check-ci
	## 受限环境的事实判定：静态传感器猜不准（实测 37 报 11 中，26 个误报），
	## 而误报会让测试被加上 RequireLoopback 从而在受限环境被跳过——
	## 那是真实的覆盖损失。这里用 seccomp 让 bind(2) 返回 EPERM，实跑给出事实。
	@ $(MAKE) check-restricted
	@ $(MAKE) check-migration-files
	## REQUIRE_DB_INTEGRATION=1 把"跳过"变成"失败"：
	## 集成测试默认在无数据库时跳过，那在日常是便利，在发布时是漏洞——
	## 跳过等于没跑，却仍然记为通过。
	@ REQUIRE_DB_INTEGRATION=1 $(MAKE) test-integration
	@ $(MAKE) migrate-verify
	@ $(MAKE) security-release
	@ echo "✅ release-check"


tag: ## 发布：bump 版本 → 门禁 → annotated tag → 原子推送
	@ bash ./scripts/release.sh

check-release-wiring: ## 证明发布入口是真实目标，且门禁确实被调用
	@ if $(MAKE) -n tag 2>&1 | grep -q "Nothing to be done"; then \
		echo "FAIL: 'make tag' is a no-op that exits 0 — the release gate would be silently skipped" >&2; \
		exit 1; \
	fi
	@ bash ./scripts/release.sh --self-test

.PHONY: check-compose-app dev-app-up dev-app-up-existing dev-app-down check-make-entries run lint clean dev build check check-ci hooks check-hooks check-build-context check-architecture check-image-digests check-security-eval check-release-wiring release-check check-coverage check-generator check-appmeta check-package-vars security-source security-release dev-up dev-down dev-reset gen-appmeta tag test-race test-coverage test-integration image tool vet port kill-port migrate-migration migrate-lint migrate-pretend migrate-up migrate-status prod test

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
# 构建追溯输入，可由CI显式固定；直用go build/Docker build未注入时为unknown。
BUILD_COMMIT ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ || printf unknown)
BUILD_FLAGS := -tags="sonic" -ldflags "-s -w -X main.BuildCommit=$(BUILD_COMMIT) -X main.BuildTime=$(BUILD_TIME)"
# app profile专用；不改变既有dev-up的默认依赖入口。
COMPOSE_ENV_FILE ?= .env
COMPOSE_PROJECT ?= $(APP_NAME)
APP_COMPOSE = docker compose --project-name "$(COMPOSE_PROJECT)" --env-file "$(COMPOSE_ENV_FILE)" --profile app
APP_COMPOSE_ENV = DEV_APP_NAME="$(APP_NAME)" DEV_BUILD_COMMIT="$(BUILD_COMMIT)" DEV_BUILD_TIME="$(BUILD_TIME)"

GO_TARGETS ?= ./...
LINT_TARGETS ?= ./...
# 验证口径的构建约束。
#
# `scaffold` 门控着模块生成器（internal/scaffold）与项目实例化器
# （internal/projectinit）——它们是开发期工具，不进服务二进制：`module` 会重写
# internal/bootstrap/modules.go，`project init` 带 --force-reinit-git 且会永久
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
# 两条路径的实测值（2026-09-17，AI 模块合入后重测）：
#   受限路径 ALLOW_NO_LOOPBACK=1（强制跳过绑端口测试）：73.0%  ← 下界
#   正常路径（loopback 可用）：                          79.5%
#   历史测量：70.3% / 74.1%（isolate-loopback-tests 后首次可复现），
#             72.4% / 79.2%（2026-09-14）。
#
# 此前注释里的“受限环境实测 62.5%”是**无效数字**：当时 test-coverage.sh 按测试名
# 黑名单分组，而名单里的三个测试早已随代码删除，`grep -Ev` 一个都没排除掉——
# 所谓受限路径实际就是全量跑，那个数字是正常环境的数字。现在受限判定由
# testutil.RequireLoopback 在测试内部完成，标志在任意机器上都真正生效，
# 上面两个数字因此可以被任何人重新测出来，而不是只能被声称。
#
# 门禁取 72.5（受限实测 73.0 之下留 0.5 余量）。**每批新增依赖 Redis 的能力都会压低受限下界**：miniredis 要绑
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
COVERAGE_MIN ?= 72.5

dev: ## 本地启动 HTTP server
ifeq ($(filter migrate-%,$(MAKECMDGOALS)),)
	$(RUN) . server
else
	@:
endif

ifeq ($(filter migrate-%,$(MAKECMDGOALS)),)
run: dev ## 本地启动（dev别名）
lint: tool ## 校验锁定lint工具版本并执行lint
test: test-race ## 执行带race的测试
else
run lint test:
	@:
endif

include make/build.mk
include make/dev.mk
include make/quality.mk
include make/scaffold.mk
include make/migration.mk

check: check-architecture check-capability-inventory check-scaffold-isolation check-tidy check-package-vars check-doc-refs check-image-digests check-security-eval check-release-wiring check-appmeta check-hooks check-build-context check-make-entries tool vet check-modern check-coverage ## Run all required pre-submit checks

check-ci: check check-generator ## CI 质量门禁（含脚手架防腐冒烟）

include make/release.mk

## 查看占用服务端口(可用 make port PORT=xxxx 覆盖)的监听进程
port:
	@lsof -nP -iTCP:$(PORT) -sTCP:LISTEN || echo "端口 $(PORT) 未被占用"

## 结束占用服务端口(默认 8008)的监听进程
kill-port:
	@pids=$$(lsof -nP -iTCP:$(PORT) -sTCP:LISTEN -t); \
	if [ -z "$$pids" ]; then \
		echo "端口 $(PORT) 未被占用"; \
	else \
		echo "结束占用端口 $(PORT) 的进程: $$pids"; \
		kill $$pids; \
	fi

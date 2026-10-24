check-make-entries: ## 隔离验证常用入口、迁移占位与清理边界
	@ bash ./scripts/check-make-entries.sh

tool: ## Lint Go code with the installed golangci-lint
	## 版本必须与 security/toolchain.mk 声明一致。
	## 声明了却不执行等于没声明：工具版本漂移会让同一份代码在不同机器上得到
	## 不同的检查结果，而那种差异最先表现为"我这里是好的"。
	@ bash ./scripts/check-tool-version.sh golangci-lint "$(GOLANGCI_LINT_VERSION)"
	@ echo "▶️ golangci-lint run ($(GOLANGCI_LINT_VERSION))"
	golangci-lint run $(LINT_TARGETS)
	@ for test_file in scripts/*_test.go; do \
		[ -e "$$test_file" ] || continue; \
		source_file="$${test_file%_test.go}.go"; \
		if [ ! -f "$$source_file" ]; then \
			printf 'script test has no matching source: %s\n' "$$test_file" >&2; \
			exit 1; \
		fi; \
	done; \
	for source_file in scripts/*.go; do \
		[ -e "$$source_file" ] || continue; \
		case "$$source_file" in *_test.go) continue ;; esac; \
		set -- "$$source_file"; \
		test_file="$${source_file%.go}_test.go"; \
		if [ -f "$$test_file" ]; then \
			set -- "$$@" "$$test_file"; \
		fi; \
		golangci-lint run --enable-only=errcheck "$$@" || exit "$$?"; \
	done
	@ echo "✅ golangci-lint run"

vet: ## Run go vet
	go vet -tags $(GO_BUILD_TAGS) $(GO_TARGETS)

check-modern: ## 现代写法门禁：go fix 有改写建议即失败
	## 「go fix -diff 无输出」此前只是工程约定，没有任何目标执行它——声明了
	## 却不执行等于没声明，与 `tool` 对工具版本用的是同一条理由。实测后果：
	## errors.As 迁移到 errors.AsType 的工作漏了一处，而全绿的 `make check`
	## 不会提到它。
	##
	## -tags 必须与 vet / test 一致：不带 scaffold 的话 internal/projectinit
	## 会静默退出检查范围。
	##
	## 判据是「有输出即失败」。go fix -diff 对合规代码输出为空，退出码恒为 0，
	## 因此不能依赖退出码——必须看输出本身。
	## 失败信息用 ASCII：Makefile 本身是 UTF-8，但 Windows 上 make 经 GBK 代码页
	## 把配方行交给 sh 时会破坏非 ASCII 字节，连引号配对一起毁掉——那时报出来的
	## 是一个 shell 语法错误，而不是这条门禁想说的话。中文解释留在上面的注释里，
	## 注释不进 shell。
	@ echo "go fix -diff (modern Go rewrites)"
	@ out="$$(go fix -diff -tags $(GO_BUILD_TAGS) $(GO_TARGETS) 2>&1)"; \
	if [ -n "$$out" ]; then \
		echo "$$out" | grep '^--- ' | sed 's/^--- /  pending: /; s/ (old)$$//'; \
		echo "FAIL: the files above still carry pre-modern constructs" >&2; \
		echo "      apply: go fix -tags $(GO_BUILD_TAGS) $(GO_TARGETS)" >&2; \
		echo "      exception: the errorsastype rewriter emits uncompilable code for" >&2; \
		echo "      anonymous interface targets (the type parameter is constrained to" >&2; \
		echo "      error); write those by hand with error embedded in the interface" >&2; \
		exit 1; \
	fi
	@ echo "OK go fix -diff"

test-race: ## Run race-enabled tests
	go test -tags $(GO_BUILD_TAGS) -race -count=1 -timeout=5m $(GO_TARGETS)

## 集成测试包由「带 integration 构建标记的 _test.go」反查得出，**不手工列举**。
##
## 手工列举会腐烂，而且腐烂得很安静：在一个新包里加了集成测试，本目标跑不到它，
## 于是那条测试等于不在门禁里——而输出看起来一切正常。
INTEGRATION_PKGS = $(sort $(patsubst %,./%,$(dir $(shell grep -rl '^//go:build integration' --include='*_test.go' internal pkg cmd 2>/dev/null))))

test-integration: ## Run real-dependency integration tests from an isolated APP_ROOT (REQUIRE_DB_INTEGRATION=1 forbids skips)
	## APP_ROOT必须显式指向项目根之外的隔离运行配置；先验证元数据，再连接依赖。
	## MySQL 侧连的是该根config/env/env.yml的database.name，它必须与本仓迁移状态一致；
	## Redis 侧连 REDIS_ITEST_ADDR（默认 127.0.0.1:6379）的 REDIS_ITEST_DB（默认 15）。
	## 单开一个 MySQL 库更省事（建库 + 迁移 + 运行的完整命令见 docs/testing.md）：
	##   APP_ROOT=/path/to/isolated-runtime REQUIRE_DB_INTEGRATION=1 make test-integration
	## 少一张表时报的是 "Table 'xxx' doesn't exist"，那条错误与真实缺陷长得一模一样。
	##
	## 空集合即失败：本仓必然有集成测试，反查出 0 个只可能是枚举坏了，
	## 而那种坏法的表现是"全绿"。
	@ test -n "$(INTEGRATION_PKGS)" || { echo "FAIL: no integration-tagged test packages found; the discovery is broken"; exit 1; }
	PLUGIN_INTEGRATION=1 go test -tags integration -count=1 -v -run '^TestPluginIntegrationPrerequisites$$' ./cmd
	PLUGIN_INTEGRATION=1 go test -tags integration -race -count=1 -timeout=5m $(INTEGRATION_PKGS)

test-coverage: ## Run race-enabled tests and write an aggregate coverage profile
	ALLOW_NO_LOOPBACK=$(ALLOW_NO_LOOPBACK) GO_BUILD_TAGS=$(GO_BUILD_TAGS) ./scripts/test-coverage.sh $(COVERAGE_PROFILE) $(GO_TARGETS)

check-coverage: test-coverage ## Enforce the aggregate statement coverage baseline
	./scripts/check-coverage.sh $(COVERAGE_PROFILE) $(COVERAGE_MIN)

check-architecture: ## 校验分层架构边界
	@ ./scripts/check-architecture.sh

check-hooks: ## 校验钩子安装语义（临时仓库实跑，不碰本机 .git/hooks）
	@ bash ./scripts/check-hooks.sh

check-build-context: ## 校验主二进制依赖的源码目录都被 .dockerignore 放行
	## 零成本静态检查，因此进日常门禁：漏放行的后果是镜像构建编译失败，
	## 而那一步只在 security-release 里跑——只在发版时才告诉你的问题，
	## 等于让每次发版都变成一次排雷（pkg/ 就这么潜伏到了发版当天）。
	@ bash ./scripts/check-build-context.sh

security-source: ## 供应链门禁（日常）：发布候选快照的漏洞与密钥扫描
	@ bash ./scripts/security-gate.sh source

security-release: ## 供应链门禁（发版前）：追加全历史密钥扫描；禁止 SKIP_VULNCHECK
	@ bash ./scripts/security-gate.sh release

check-package-vars: ## 包级变量重绑定检查（每处必须删除或在登记表中认领）
	@ GOWORK=off go run ./scripts/check-package-vars.go internal cmd

check-doc-refs: ## 文档内引用必须解析到真实路径（每处解析不到要么修好，要么在登记表中认领）
## 先自检后实检：自检造一批已知的死引用与一批良性形态，断言前者被抓到、后者零误报。
## 缺了它，正则写坏或规则收得过紧时实检会全绿——判据失效恰恰不会有任何症状，
## 而 docs/ 是工程约定的单一来源，它的引用腐烂只会在有人照着文档找不到东西时才暴露。
	@ GOWORK=off go run ./scripts/check-doc-refs.go --self-test
	@ GOWORK=off go run ./scripts/check-doc-refs.go


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

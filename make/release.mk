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
	@ REQUIRE_DB_INTEGRATION=1 PLUGIN_INTEGRATION=1 $(MAKE) test-integration
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

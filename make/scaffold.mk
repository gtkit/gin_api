check-capability-inventory: ## 校验 SCAFFOLD.md 的预置能力清单穷尽
## 先自检后实检：自检为每个已声明的 scope 造一个未登记条目并断言判据失败，
## 再断言登记后通过。缺了它，一个 scope 的枚举实现写坏（返回空）时实检会全绿——
## 判据本身失效恰恰不会有任何症状，而那正是本次扩围要消除的失败模式。
	@ ./scripts/check-capability-inventory.sh --self-test
	@ ./scripts/check-capability-inventory.sh

## 单独跑一次**不带 tag** 的命令树断言。
##
## 必须单独一步：日常测试口径带 $(GO_BUILD_TAGS)，而在那个口径下 project
## 应当在场，"缺席"断言只可能在默认口径下成立。不单独跑，它永远不会在门禁里
## 执行——一个从不执行的断言不是保护，是装饰。
check-scaffold-isolation: ## 断言默认构建的命令树不含脚手架命令
	go test -count=1 -run TestDefaultBuildExcludesScaffoldCommands ./cmd/

## 项目实例化器由 `scaffold` 构建约束门控，不在服务二进制里。
project-init: ## Instantiate a new project from this scaffold mother (usage: make project-init NAME=your_project ARGS="--dry-run")
	@ test -n "$(NAME)" || { echo "usage: make project-init NAME=<project-name> [ARGS=\"--dry-run\"]" >&2; exit 2; }
	go run -tags $(GO_BUILD_TAGS) . project init --name $(NAME) $(ARGS)

gen-appmeta: ## 由 project.mk 生成 internal/appmeta 身份常量（改身份先改 project.mk）
	@ bash ./scripts/gen-appmeta.sh

check-appmeta: ## 校验 internal/appmeta 与 project.mk 零差异（防身份漂移）
	@ bash ./scripts/gen-appmeta.sh --check

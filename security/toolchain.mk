# security/toolchain.mk —— 安全扫描器的**固定版本与 digest**。
#
# 为什么钉 digest 而不是 tag：tag 可变。同一个 `:latest` 甚至 `:v8.29.0` 在不同时间
# 可能指向不同镜像，于是"昨天通过、今天失败"无从复现，也无法回答"这次扫描到底跑了什么"。
#
# 升级必须走独立 change，并同时更新基线用例。任何入口都不得使用 @latest 或浮动 tag。
#
# govulncheck 不在此处：它由 go.mod 的 tool 指令锁定精确版本，
# 经 `go tool govulncheck` 调用，不依赖 PATH 上的随机版本。

GITLEAKS_IMAGE = zricethezav/gitleaks@sha256:71d3ee5990f2176f763b438298453fc37e87b119122045e176ca9d44ff00b08b
TRIVY_IMAGE    = aquasec/trivy@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f

# golangci-lint 消除 PATH 漂移：本地安装版本必须与此处声明一致。
# 该版本无法经 go.mod tool 指令构建（依赖树与本模块冲突），因此改为声明 + 执行前校验。
GOLANGCI_LINT_VERSION = 2.11.4

# 扫描产物固定路径，全部在 .gitignore 排除的 .cache/security/ 下。
SECURITY_CACHE = .cache/security

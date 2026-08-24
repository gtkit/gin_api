# security/toolchain.mk —— 安全扫描器的**固定版本与 digest**。
#
# 为什么钉 digest 而不是 tag：tag 可变。同一个 `:latest` 甚至 `:v8.29.0` 在不同时间
# 可能指向不同镜像，于是"昨天通过、今天失败"无从复现，也无法回答"这次扫描到底跑了什么"。
#
# 升级必须走独立 change，并同时更新基线用例。任何入口都不得使用 @latest 或浮动 tag。
#
# govulncheck 不在此处：它由 go.mod 的 tool 指令锁定精确版本，
# 经 `go tool govulncheck` 调用，不依赖 PATH 上的随机版本。

# 基础镜像与开发依赖镜像的固定摘要。
#
# 与扫描器同一条理由，但**后果更重**：基础镜像进的是交付物本身，扫描器只是工具。
# 摘要取自多平台 OCI index，不是单平台 image digest——后者会让镜像只能在
# 一种架构上构建。同时保留可读 tag：只写摘要时没人能一眼看出这是哪个版本，
# 两者并存时 Docker 以摘要为准，tag 退化为注释。
#
# 升级流程：`docker buildx imagetools inspect <tag>` 取顶层 Digest，改这里，
# 再跑 `make check-image-digests`。绝不手抄或推测摘要。
GOLANG_IMAGE = golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc
ALPINE_IMAGE = alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
# 受限验证的测试运行器。
#
# 刻意用**自带 git 的完整镜像**而不是交付用的 alpine：受限环境靠 seccomp 拒绝
# bind(2)，而 DNS 客户端需要 bind 一个 UDP socket 才能收响应——于是容器内
# 无法联网装包。git 是 projectinit 测试的真实依赖，缺它会让那些测试以
# "git 不存在"失败，把真正的端口信号淹没在噪音里。
#
# 与交付镜像不一致是可以接受的：它只是测试运行器，不进任何交付物。
RESTRICTED_RUNNER_IMAGE = golang:1.27@sha256:65b6f280bf050ec5af12716857e8ea8439d694dbba8f31ceeb7630670071f2bb

MYSQL_IMAGE  = mysql:8.0@sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b
REDIS_IMAGE  = redis:7-alpine@sha256:e7723ff73d963f5cc6d9c4643ea3d989527a402a319239054e9472a7fb9219a2

GITLEAKS_IMAGE = zricethezav/gitleaks@sha256:71d3ee5990f2176f763b438298453fc37e87b119122045e176ca9d44ff00b08b
TRIVY_IMAGE    = aquasec/trivy@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f

# golangci-lint 消除 PATH 漂移：本地安装版本必须与此处声明一致。
# 该版本无法经 go.mod tool 指令构建（依赖树与本模块冲突），因此改为声明 + 执行前校验。
GOLANGCI_LINT_VERSION = 2.13.1

# 扫描产物固定路径，全部在 .gitignore 排除的 .cache/security/ 下。
SECURITY_CACHE = .cache/security

# 源码安全与私有依赖

CI 分为质量和源码安全两个 job。源码安全使用 go.mod 锁定的 govulncheck，以及 security/toolchain.mk 的 Gitleaks 版本；不使用 Docker，也不复制项目。漏洞库不可达、工具不存在、版本不符或报告格式错误都失败，不能当作没有问题。

`make security-source` 扫描当前跟踪文件和未忽略的新文件。Gitleaks 从内存流读取内容，结果映射回项目相对文件/行；报告不包含匹配正文或密钥。总输入上限 128 MiB，超限明确失败而不静默漏扫。输出为 `.cache/security/govulncheck.json` 和 `gitleaks.json`，不保存本机绝对目录。此入口不自动应用发布豁免，任何源码 finding 均需处置；发布全历史/镜像门禁仍使用原独立入口，不在本机自动执行。

checkout 不持久化默认 GitHub token，避免默认认证头覆盖私有依赖凭据。GitHub Actions 可设置只具备依赖仓库读取权限的 `PRIVATE_MODULES_TOKEN` secret；它只进入下载步骤的临时 Git credential helper，不写 URL、gitconfig 或后续步骤。`GO_MODULE_PROXY` 仓库变量可指定不含凭据的内部代理。Fork PR 不会收到 secret；无法取得私有依赖时下载失败，不能跳过后继续报告通过。需要外部贡献者运行时，应另提供经授权可访问的依赖分发方式。

Dockerfile 支持 BuildKit 的 `netrc` secret 挂载。使用方自行提供仅含依赖访问权限的私有 netrc 文件，并在获得构建授权后通过 `--secret id=netrc,src=<private-netrc>` 传入；不要把 token 放入 GOPROXY、build arg 或仓库文件。构建阶段安装 Git 以支持私有仓库直接下载，运行镜像仍不带 Git。

本地工具安装、远端 CI、镜像构建和漏洞库访问是独立动作。源文件与 job 已配置不等于它们已经执行，本次验收按实际记录区分。

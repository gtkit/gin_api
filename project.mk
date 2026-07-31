# project.mk —— 应用身份的唯一来源
#
# 本文件被 Makefile include，并由 scripts/gen-appmeta.sh 生成 internal/appmeta/meta.go。
# `make check` 的 check-appmeta 保证生成物与提交物零差异，因此身份不可能漂移。
#
# 取值约束（gen-appmeta.sh 在生成任何内容之前强制校验，不合法即失败）：
#   APP_NAME          ^[a-z][a-z0-9_]{0,62}$
#   APP_DISPLAY_NAME  ^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$
# 这两个值会分别进入 Make 表达式与 Go 字符串字面量，字符集必须受限。
#
# 本文件【不含】以下两项，它们不属于应用身份：
#   * Go module path —— 由 gonew 决定，与应用名相互独立，运行期经 debug.ReadBuildInfo() 解析。
#   * COVERAGE_MIN   —— 各仓库唯一的机器门禁，留在 Makefile，实例化时不重置。
#
# APP_NAME 同时决定宿主机构建产物名 bin/$(APP_NAME)。当前值与部署脚本的既有约定一致，
# 改它需要同步确认部署侧的约定（部署脚本是外部冻结契约，本仓不修改它）。

APP_NAME = gin_api
APP_DISPLAY_NAME = Gin API

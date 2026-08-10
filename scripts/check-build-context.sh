#!/usr/bin/env bash
# 校验每个含 Go 源码的顶层目录都被 .dockerignore 放行。
#
# .dockerignore 用 allowlist（`*` 排除一切，再逐项 `!` 放行）。这对本地产物
# 默认安全，但对**源码目录默认不安全**：新增一个顶层源码目录而忘了放行，
# 镜像构建会直接编译失败。
#
# 实测代价——pkg/ 一直没被放行，而 internal/middleware 依赖 pkg/ratelimit，
# 于是镜像构建从来就编译不过。它只在 security-release 里跑，问题一路潜伏到
# 发版当天才暴露，前面所有门禁全绿。
#
# 本检查零成本、纯静态，因此放进日常门禁而不是发布门禁：一个只在发版时才
# 告诉你的问题，等于让每次发版都变成一次排雷。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

IGNORE_FILE=".dockerignore"
[ -f "$IGNORE_FILE" ] || { echo "找不到 $IGNORE_FILE" >&2; exit 1; }

# allowlist 生效的前提是首行规则排除一切。前提不成立时本检查的判据也不成立，
# 与其给出一个看起来通过的结论，不如直接失败。
if ! grep -qE '^\*$' "$IGNORE_FILE"; then
	echo "FAIL: $IGNORE_FILE 不是 allowlist 形态（缺少排除一切的 '*' 规则），本检查的判据不成立" >&2
	exit 1
fi

failed=0

# 判据取**主二进制的 import 图**，不是"哪些目录有 .go 文件"。
#
# 后者会把 scripts/ 这类独立工具（用 go run ./scripts/xxx.go 单独跑、不被 main
# 依赖）也算进来，而它们本就不该进镜像——那种误报会让人给 .dockerignore 加上
# 不需要的放行，把上下文重新撑大，正好抵消 allowlist 的意义。
module="$(GOWORK=off go list -m)"
while IFS= read -r dir; do
	[ -n "$dir" ] || continue
	if ! grep -qE "^!${dir}(/|$)" "$IGNORE_FILE"; then
		echo "FAIL: 顶层源码目录 $dir/ 未在 $IGNORE_FILE 放行，镜像构建会因缺少它而编译失败" >&2
		echo "      修法：在 $IGNORE_FILE 的源码放行段加一行 '!${dir}'" >&2
		failed=1
	fi
done < <(
	GOWORK=off go list -deps . \
		| grep -E "^${module}/" \
		| sed -E "s|^${module}/||" \
		| cut -d/ -f1 \
		| sort -u
)

# 根目录下的 .go 文件同样要逐个放行（main.go / version.go）。
while IFS= read -r file; do
	[ -n "$file" ] || continue
	if ! grep -qE "^!${file}$" "$IGNORE_FILE"; then
		echo "FAIL: 根目录源码 $file 未在 $IGNORE_FILE 放行" >&2
		failed=1
	fi
done < <(find . -maxdepth 1 -name '*.go' -not -name '*_test.go' | cut -d/ -f2 | sort)

if [ "$failed" -ne 0 ]; then
	exit 1
fi
echo "build context check OK: 全部 Go 源码目录均已放行"

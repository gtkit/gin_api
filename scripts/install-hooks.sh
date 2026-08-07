#!/usr/bin/env bash
# 把 scripts/git-hooks/ 下的钩子装到本仓的 git 钩子目录。
#
# 复制而不是 symlink：symlink 在 Windows 与部分挂载环境下不可靠，而钩子装不上
# 的失败模式是**静默的**——git 只是不执行它，使用者以为门禁在跑。
#
# 也不用 core.hooksPath：那是仓库级设置，会一次性接管全部钩子，包括使用者自己
# 已有的那些。钩子是他们机器上的自动化，本仓无从判断它在做什么。
set -euo pipefail

# 本仓分发的钩子都带这行标记，据此区分"本仓装的（含旧版本）"与"别人的"。
readonly MARKER="gin-api-managed-hook"

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

SRC_DIR="$ROOT/scripts/git-hooks"
if [ ! -d "$SRC_DIR" ]; then
	echo "找不到钩子源目录 $SRC_DIR" >&2
	exit 1
fi

HOOK_DIR="$(git rev-parse --git-path hooks)"
mkdir -p "$HOOK_DIR"

installed=0
for src in "$SRC_DIR"/*; do
	[ -f "$src" ] || continue
	name="$(basename "$src")"
	dst="$HOOK_DIR/$name"

	# 目标已存在且不是本仓装的：报错中止，让使用者自己决定怎么合并。
	# 静默覆盖别人的自动化不可接受。
	if [ -e "$dst" ] && ! grep -q "$MARKER" "$dst" 2>/dev/null; then
		echo "已存在非本仓的 $name 钩子，未做修改：$dst" >&2
		echo "请自行合并后再重跑 make hooks。" >&2
		exit 1
	fi

	# 本仓装的（无论新旧）直接重装：幂等，便于随仓库更新。
	cp "$src" "$dst"
	chmod +x "$dst"
	echo "installed $dst"
	installed=$((installed + 1))
done

if [ "$installed" -eq 0 ]; then
	echo "$SRC_DIR 下没有可安装的钩子" >&2
	exit 1
fi

echo "git hooks OK: $installed installed"

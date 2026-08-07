#!/usr/bin/env bash
# 验证 make hooks 的安装语义与钩子本身的行为。
#
# 全程在临时 git 仓库里实跑，不碰本仓的 .git/hooks——一个会改使用者钩子的
# 测试，本身就是这套设施最不该有的行为。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

failures=0
fail() {
	echo "  ✗ $1" >&2
	failures=$((failures + 1))
}

# 造一个只含钩子设施的临时仓库：install-hooks.sh 用 git rev-parse 定位仓库根，
# 因此它在这里操作的是临时仓库，与本仓完全隔离。
setup_repo() {
	local repo="$1"
	mkdir -p "$repo/scripts/git-hooks"
	git -C "$repo" init -q
	cp "$ROOT/scripts/install-hooks.sh" "$repo/scripts/"
	cp "$ROOT"/scripts/git-hooks/* "$repo/scripts/git-hooks/"
}

echo "▶️ check-hooks"

# 场景 1：装完就位且可执行。
repo="$TMP/install"
setup_repo "$repo"
if ! (cd "$repo" && bash scripts/install-hooks.sh >/dev/null 2>&1); then
	fail "安装在干净仓库里失败"
elif [ ! -x "$repo/.git/hooks/pre-push" ]; then
	fail "安装后 .git/hooks/pre-push 不存在或不可执行"
fi

# 场景 2：目标已存在非本仓钩子时报错中止，且不动那个文件。
repo="$TMP/foreign"
setup_repo "$repo"
printf '#!/bin/sh\necho someone elses hook\n' > "$repo/.git/hooks/pre-push"
chmod +x "$repo/.git/hooks/pre-push"
if (cd "$repo" && bash scripts/install-hooks.sh >/dev/null 2>&1); then
	fail "存在非本仓钩子时安装仍然成功了"
fi
if ! grep -q "someone elses hook" "$repo/.git/hooks/pre-push"; then
	fail "非本仓的钩子被覆盖了"
fi

# 场景 3：已装本仓那一份时重装成功（幂等，便于随仓库更新）。
repo="$TMP/idempotent"
setup_repo "$repo"
(cd "$repo" && bash scripts/install-hooks.sh >/dev/null 2>&1)
if ! (cd "$repo" && bash scripts/install-hooks.sh >/dev/null 2>&1); then
	fail "重复安装失败（应当幂等）"
fi

# 场景 4：跳过开关放行且留痕——绕过本身不是问题，绕过了没人知道才是。
repo="$TMP/skip"
setup_repo "$repo"
(cd "$repo" && bash scripts/install-hooks.sh >/dev/null 2>&1)
skip_err="$TMP/skip.err"
if ! (cd "$repo" && SKIP_GATE=1 .git/hooks/pre-push >/dev/null 2>"$skip_err"); then
	fail "SKIP_GATE=1 时钩子没有放行"
elif ! grep -q "SKIP_GATE" "$skip_err"; then
	fail "跳过时没有在错误输出留痕"
fi

# 场景 5：门禁不过则拦下推送。用假 Makefile 让 check-ci 失败，
# 这样验的是钩子的判定，而不是真去跑一遍分钟级门禁。
repo="$TMP/gate-red"
setup_repo "$repo"
(cd "$repo" && bash scripts/install-hooks.sh >/dev/null 2>&1)
printf 'check-ci:\n\t@exit 1\n' > "$repo/Makefile"
if (cd "$repo" && .git/hooks/pre-push >/dev/null 2>&1); then
	fail "门禁失败时钩子仍然放行了"
fi

if [ "$failures" -ne 0 ]; then
	echo "check-hooks FAILED ($failures)" >&2
	exit 1
fi
echo "check-hooks OK"

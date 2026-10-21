#!/usr/bin/env bash
# 脚手架防腐冒烟：把「发布候选工作树」导出到临时目录，用模块生成器造一个临时模块，
# 验证产物能编译、过架构传感器、且 goimports 干净。用后即弃，不触碰当前工作区。
#
# 目的：堵死「providers.go 结构变了、生成器 marker/模板没跟上」这类脱节
# （姊妹项目曾因此让自动接线静默失效）。
#
# 为什么必须验工作树而不是 HEAD：旧实现用 `git archive HEAD`，只验证已提交内容。
# 于是在本地改了生成器或模板、还没提交时运行本门禁，验的仍是旧代码——门禁看似通过，
# 实际对当前改动毫无约束。凡以本脚本作为完成判据的改动都会被这一失真放过。
#
# 为什么导出集合必须含未跟踪文件：`git ls-files`（仅 --cached）只列进了 index 的路径，
# 新增但未 `git add` 的模板/marker/生成器源文件完全不进副本。而生成器改造往往正是
# 从新增文件开始的——门禁会在最需要约束的时刻失效。ignored 文件仍必须排除（本地日志、
# 外置配置、构建产物不属于发布候选内容）。
#
# 用法：
#   check-generator.sh                正常门禁
#   check-generator.sh --self-test    自检：证明门禁确实在验工作树（破坏副本中的锚点后必须失败）
#   check-generator.sh --verify-scope 自检：证明导出范围恰好覆盖四类边界行为
set -euo pipefail

probe="ci_probe"
# service_anchor 是生成器注入 service 构造所依赖的锚点，自检时故意破坏它。
service_anchor="// scaffold:module-services"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# export_worktree 把「发布候选工作树」复制到目标目录。
#
# 集合来源（$2 起，默认 --cached --others --exclude-standard）：
#   --cached           已跟踪文件
#   --others           未跟踪文件
#   --exclude-standard 应用 .gitignore/.git/info/exclude/全局 excludes，排除 ignored 文件
# 过滤 `-e "$f"`：ls-files 列的是 index 中的路径，已删除但未提交的文件仍会出现。
# 经 tar 传输：保留可执行位（scripts/*.sh 需要 +x，否则临时目录里跑不起来）。
# COPYFILE_DISABLE=1：macOS 的 bsdtar 默认把扩展属性打成 AppleDouble（._ 前缀）成员，
#   会对每个文件产出一行告警，把真实失败淹没在噪声里。
export_worktree() {
  local dest="$1"
  shift
  local -a ls_flags=("$@")
  if [ "${#ls_flags[@]}" -eq 0 ]; then
    ls_flags=(--cached --others --exclude-standard)
  fi
  git ls-files -z "${ls_flags[@]}" | while IFS= read -r -d '' f; do
    [ -e "$f" ] && printf '%s\0' "$f"
  done | COPYFILE_DISABLE=1 tar --null -T - -cf - | COPYFILE_DISABLE=1 tar -x -C "$dest"
}

# isolate_caches 把 Go 与 golangci-lint 的缓存指向门禁自有的临时路径。
# 原因：门禁在临时副本里构建，复用开发者默认缓存会产生缓存权限告警；且门禁不应
# 污染开发者的默认缓存。缓存目录位于 $tmp 内，随既有 trap 一并清理。
isolate_caches() {
  local tmp="$1"
  export GOCACHE="$tmp/.cache/go-build"
  export GOLANGCI_LINT_CACHE="$tmp/.cache/golangci-lint"
  mkdir -p "$GOCACHE" "$GOLANGCI_LINT_CACHE"
}

run_gate() {
  local tmp
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064  # 有意在注册时展开 $tmp，而非退出时。
  trap "rm -rf '$tmp'" EXIT

  isolate_caches "$tmp"

  echo "[check-generator] 导出发布候选工作树到临时目录 $tmp"
  export_worktree "$tmp"
  cd "$tmp"

  echo "[check-generator] 生成临时模块 $probe"
  go run -tags scaffold . module "$probe" --public --field title:string >/dev/null

  echo "[check-generator] migration format"
  bash scripts/check-migration-files.sh database/migrations

  echo "[check-generator] go build ./..."
  go build ./...

  echo "[check-generator] 架构传感器"
  bash scripts/check-architecture.sh

  echo "[check-generator] goimports / lint（生成代码 + 被注入的 providers.go）"
  golangci-lint run \
    ./internal/bootstrap/... \
    "./internal/module/${probe}/..." \
    "./internal/repository/${probe}/..." \
    "./internal/runtime/module/${probe}/..."

  echo "check-generator OK"
}

# run_self_test 证明本门禁真的在验证工作树：在**临时副本**里破坏生成器锚点后，
# 生成步骤必须失败。只改副本，绝不触碰真实工作区。
# 这条自检存在的原因见文件头——门禁曾因只验 HEAD 而对未提交改动完全失真。
run_self_test() {
  local tmp
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT

  isolate_caches "$tmp"

  echo "[self-test] 导出发布候选工作树到临时副本 $tmp"
  export_worktree "$tmp"

  local target="$tmp/internal/bootstrap/providers.go"
  if ! grep -qF "$service_anchor" "$target"; then
    echo "[self-test] FAIL: 副本中未找到锚点 $service_anchor（锚点被重命名？请同步更新本脚本）"
    exit 1
  fi

  echo "[self-test] 在副本中破坏锚点，生成步骤应当失败"
  # 用 perl 而非 sed -i：GNU/BSD sed 的 -i 语义不同，perl 跨平台一致。
  # 锚点经环境变量传入（$ENV{ANCHOR} + \Q..\E 按字面量匹配），不内联进脚本文本——
  # 锚点自身含 `//` 与空格，内联会破坏 s/// 的分隔符。分隔符用 `!`，两侧字符串都不含它。
  ANCHOR="$service_anchor" perl -pi -e 's!\Q$ENV{ANCHOR}\E!// scaffold-SELFTEST-BROKEN!' "$target"

  if grep -qF "$service_anchor" "$target"; then
    echo "[self-test] FAIL: 锚点替换未生效，自检本身失效"
    exit 1
  fi

  cd "$tmp"
  if go run -tags scaffold . module "$probe" --public --field title:string >/dev/null 2>&1; then
    echo "[self-test] FAIL: 锚点已破坏但生成仍成功——门禁没有真正验证工作树"
    exit 1
  fi

  echo "[self-test] OK：破坏未提交锚点后门禁如期失败"
}

# run_verify_scope 逐条断言 export_worktree 的导出范围恰好覆盖四类边界行为。
#
# 在**一次性的沙盒 git 仓库**里造出四种状态再导出，因此完全不触碰真实工作区：
# 既不修改已有文件，也不在项目根内留下探针。沙盒位于 mktemp 目录（项目根之外）。
run_verify_scope() {
  local sandbox
  sandbox="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$sandbox'" EXIT

  local src="$sandbox/src" dst="$sandbox/dst" narrow="$sandbox/narrow"
  mkdir -p "$src" "$dst" "$narrow"

  echo "[verify-scope] 构造沙盒仓库 $src"
  (
    cd "$src"
    git init -q .
    printf 'ignored-local.txt\n' > .gitignore
    printf 'committed content\n'  > tracked-modified.txt
    printf 'will be deleted\n'    > tracked-deleted.txt
    printf 'unchanged\n'          > tracked-intact.txt
    printf '#!/bin/sh\nexit 0\n'  > tracked-exec.sh
    chmod +x tracked-exec.sh
    git add -A
    git -c user.name=gate -c user.email=gate@local -c commit.gpgsign=false \
        commit -q -m "sandbox baseline"

    # 四类边界状态
    printf 'MODIFIED IN WORKTREE\n' > tracked-modified.txt  # ① 已跟踪 + 未提交修改
    printf 'brand new\n'            > untracked-new.txt     # ② 新增未跟踪且非 ignored
    rm -f tracked-deleted.txt                               # ③ 已跟踪 + 未提交删除
    printf 'local secret\n'         > ignored-local.txt     # ④ ignored 本地文件
  )

  echo "[verify-scope] 用生产导出集合导出到 $dst"
  ( cd "$src" && export_worktree "$dst" )

  local failed=0
  fail() { echo "[verify-scope] FAIL: $1"; failed=1; }

  # ① 内容取自工作树而非 index/HEAD
  if [ ! -f "$dst/tracked-modified.txt" ]; then
    fail "① 已跟踪文件缺失于副本"
  elif ! grep -qxF 'MODIFIED IN WORKTREE' "$dst/tracked-modified.txt"; then
    fail "① 副本中的内容不是工作树的未提交修改（取到了 index/HEAD 的旧内容）"
  fi

  # ② 新增未跟踪且非 ignored 文件必须进副本
  [ -f "$dst/untracked-new.txt" ] || \
    fail "② 新增未跟踪且非 ignored 文件未进副本（导出集合缺 --others）"

  # ③ 未提交删除的已跟踪文件必须缺失，且导出不因此报错
  [ ! -e "$dst/tracked-deleted.txt" ] || \
    fail "③ 已在工作树删除的已跟踪文件仍出现在副本中"

  # ④ ignored 本地文件必须排除
  [ ! -e "$dst/ignored-local.txt" ] || \
    fail "④ ignored 本地文件进了副本（导出集合缺 --exclude-standard）"

  # 附带：可执行位必须保留，否则副本里的 scripts/*.sh 跑不起来
  [ -x "$dst/tracked-exec.sh" ] || fail "可执行位未保留"

  # 负向证明：把导出集合收窄回仅 --cached 时，断言 ② 必须失效。
  # 没有这一步，断言 ② 可能因为别的原因恒真而失去鉴别力。
  echo "[verify-scope] 负向证明：收窄为仅 --cached 时断言 ② 必须失效"
  ( cd "$src" && export_worktree "$narrow" --cached )
  [ ! -e "$narrow/untracked-new.txt" ] || \
    fail "负向证明失效：仅 --cached 时新增未跟踪文件仍进了副本，断言 ② 无鉴别力"

  if [ "$failed" -ne 0 ]; then
    exit 1
  fi
  echo "[verify-scope] OK：四类导出范围行为均符合契约，且断言具备鉴别力"
}

case "${1:-}" in
  --self-test)    run_self_test ;;
  --verify-scope) run_verify_scope ;;
  "")             run_gate ;;
  *)              echo "用法: $(basename "$0") [--self-test|--verify-scope]" >&2; exit 2 ;;
esac

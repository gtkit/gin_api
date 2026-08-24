#!/usr/bin/env bash
# release.sh —— 发布流程：bump 版本 → 门禁 → annotated tag → 原子推送。
#
# **为什么不写在 Makefile 配方里**：GNU make 的 `-n`（dry-run）对包含 `$(MAKE)`
# 的命令行不生效，而发布配方必然要调用门禁。实测 `make -n gittag` 会真实执行
# 前置校验——工作区干净时它会一路改写版本文件、提交、打 tag 并推送。
# 发布是不可逆的外向操作，而 dry-run 正是使用者用来确认安全的手段。
#
# 移出来还有两个附带好处：流程可被单步调试，以及可以有真正的自检
# （见 --self-test：它观察门禁**被调用**这一事实，而不是只看退出码）。
#
# 用法：
#   release.sh              执行发布
#   release.sh --self-test  自检：在一次性沙盒里证明门禁确实被调用
set -euo pipefail

# GATE_COMMAND 是发布必须通过的门禁。
#
# 单独提出来，让自检可以断言"恰好调用了这三个"——写死在调用点的话，
# 自检只能验证退出码，而本次修复的缺陷正是"返回 0 但什么都没做"。
GATE_COMMAND=(make release-check)

# RELEASE_TAG_RE 只认无前导零的稳定 SemVer，与破坏性门禁的基线选择同源。
RELEASE_TAG_RE='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'

die() { echo "$*" >&2; exit 1; }

# resolve_remote 推导推送目标：用仓库里**实际存在**的那个 remote。
#
# 不假设它叫 origin。母版会被派生到各种仓库里，那里的 remote 叫什么由使用者
# 决定；写死一个名字的后果是发布跑完整个门禁、建好 release commit、打好 tag，
# 最后一步才推送失败——留下"有 release commit、无 tag"的半发布状态，
# 而那需要人工判断怎么回退。
#
# 优先级：
#   ① 当前分支的 upstream——它是使用者已经明确表达过的推送意图；
#   ② 仓库只有一个 remote 时用它——此时不存在歧义；
#   ③ 多个 remote 且无 upstream：**报错中止**，不猜。猜错等于把版本推到
#      另一个仓库，而那是不可撤销的外向操作。
resolve_remote() {
  local upstream remote
  if upstream="$(git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null)"; then
    remote="${upstream%%/*}"
    # upstream 指向本地分支时 remote 部分是 "."，那不是可推送的目标。
    if [ -n "$remote" ] && [ "$remote" != "." ] && [ "$remote" != "$upstream" ]; then
      printf '%s\n' "$remote"
      return 0
    fi
  fi

  if [ "$(git remote | grep -c .)" = "1" ]; then
    git remote
    return 0
  fi
  return 1
}

# preflight 在**产生任何副作用之前**做完全部前置校验。
#
# 顺序是硬要求：若在改写版本文件之后才发现 tag 已存在，会留下
# "版本已改但未发布"的中间状态，而那种状态需要人工判断如何回退。
preflight() {
  [ -z "$(git status --porcelain)" ] || {
    echo "工作区或索引不干净，发布前必须完全干净：" >&2
    git status --short >&2
    exit 1
  }

  branch="$(git symbolic-ref --short HEAD 2>/dev/null)" ||
    die "当前处于 detached HEAD，拒绝发布：无法确定要推送的分支"

  remote="$(resolve_remote)" ||
    die "无法确定推送目标：当前分支没有 upstream，且仓库有多个 remote：$(git remote | tr '\n' ' ')
请先 git branch --set-upstream-to=<remote>/$branch 指定，再重跑发布。"

  current="$(grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' version.go | head -n1 | tr -d 'v')"
  [ -n "$current" ] || die "version.go 中未找到版本号"
  echo "$current" | grep -qE "$RELEASE_TAG_RE" ||
    die "当前版本 $current 不是无前导零的稳定 SemVer"

  local maj min patch
  maj="${current%%.*}"
  patch="${current##*.}"
  min="${current#*.}"; min="${min%.*}"

  # 默认 PATCH 自增。破坏性变更必须走 MINOR：项目的 Go 模块版本策略规定
  # 破坏性变更留在现有 major 线以 **MINOR** 发布（且只允许 fail-closed 形态），
  # 而 PATCH 会让使用者以为可以无脑升级。
  #
  # 做成显式 flag 而不是自动推导：哪些改动算破坏性是人的判断，脚本无从得知。
  # 默认保持 PATCH，因此不传 flag 时行为与从前逐字一致。
  case "$BUMP_KIND" in
    patch) new="v${maj}.${min}.$((patch + 1))" ;;
    minor) new="v${maj}.$((min + 1)).0" ;;
    *)     die "未知的 bump 类型：$BUMP_KIND" ;;
  esac

  ! git rev-parse -q --verify "refs/tags/$new" >/dev/null ||
    die "tag $new 已存在，拒绝重复发布"
}

run_release() {
  local branch current new remote
  preflight

  printf 'Bump[%s]: v%s -> %s (branch %s, remote %s)\n' "$BUMP_KIND" "$current" "$new" "$branch" "$remote"

  # ① 先在本地生成 release commit——此时绝不推送、绝不打 tag。
  #    门禁必须跑在**最终 commit** 上，否则验证的是一份不会被发布的树。
  sed -E -i.bak 's/(const Version = ")([^"]+)(")/\1'"$new"'\3/' version.go
  rm -f version.go.bak
  git add version.go
  git commit -m "chore(release): $new"

  # ② 门禁失败时保留本地 commit、不建 tag、不碰远端。
  if ! "${GATE_COMMAND[@]}"; then
    echo ""
    echo "发布门禁失败。本地 release commit 已保留，未创建 tag、未推送任何内容。" >&2
    echo "修复后可 git reset --soft HEAD~1 撤销该 commit，或修好后重跑 make tag。" >&2
    exit 1
  fi

  # ③ 门禁全绿后才创建 annotated tag。
  git tag -a "$new" -m "release $new"

  # ④ 用显式 refspec 原子推送分支与 tag。
  #    不依赖 push.default 或远端 HEAD 推导；服务端不支持 atomic 或分支非
  #    fast-forward 时整体失败，绝不退化成两次 push——那会留下
  #    「有 release commit、无 tag」的半发布状态。
  git push --atomic "$remote" "HEAD:refs/heads/$branch" "refs/tags/$new:refs/tags/$new"
  printf 'Done: %s pushed to %s\n' "$new" "$branch"
}

# ---------- 自检 ----------

# run_self_test 证明门禁**确实被调用**，以及失败时确实不发布。
#
# 只验证退出码是不够的：本次修复的缺陷正是"命令返回 0 但什么都没做"，
# 那种缺陷会和只看退出码的自检一起通过。因此这里用 PATH 前置的 make stub
# 把调用记录下来，再断言记录的内容。
run_self_test() {
  local sandbox script upstream_branch
  script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  sandbox="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$sandbox'" EXIT

  local failed=0
  check() {
    if [ "$2" = "$3" ]; then
      echo "[self-test] OK: $1"
    else
      echo "[self-test] FAIL: $1 => got '$2', want '$3'"
      failed=1
    fi
  }

  # make stub：记录调用参数，按 GATE_EXIT 决定成败。
  mkdir -p "$sandbox/bin"
  cat > "$sandbox/bin/make" <<'STUB'
#!/usr/bin/env bash
echo "$*" >> "$GATE_LOG"
exit "${GATE_EXIT:-0}"
STUB
  chmod +x "$sandbox/bin/make"

  # 每个用例一个**独立的** bare 远端。
  #
  # 共用一个会产生串扰：先前用例推上去的 tag 会让"失败时什么都没推"这条
  # 断言看到别人的产物而误判。一个自己会串扰的自检，说明不了任何事。
  setup_repo() {
    local dir="$1" version="${2:-v1.2.3}"
    rm -rf "$dir" "$dir.remote.git"
    git init -q --bare "$dir.remote.git"
    mkdir -p "$dir"
    git init -q "$dir"
    git -C "$dir" config user.email release@local
    git -C "$dir" config user.name release
    git -C "$dir" config commit.gpgsign false
    printf 'package main\n\nconst Version = "%s"\n' "$version" > "$dir/version.go"
    git -C "$dir" add -A
    git -C "$dir" commit -qm init
    git -C "$dir" remote add origin "$dir.remote.git"
  }

  run_in() {
    local dir="$1" exit_code="$2"
    shift 2
    ( cd "$dir" && PATH="$sandbox/bin:$PATH" GATE_LOG="$sandbox/gate.log" \
        GATE_EXIT="$exit_code" bash "$script" "$@" >"$sandbox/out.txt" 2>&1 )
  }

  # --- 用例 1：门禁通过 → 建 tag + 推送 ---
  : > "$sandbox/gate.log"
  setup_repo "$sandbox/ok"
  run_in "$sandbox/ok" 0 && status=0 || status=$?

  check "a passing gate releases" "$status" "0"
  check "the gate is invoked exactly once" "$(wc -l < "$sandbox/gate.log" | tr -d ' ')" "1"
  check "the gate runs the aggregated release check" \
    "$(cat "$sandbox/gate.log")" "release-check"
  check "an annotated tag is created" \
    "$(git -C "$sandbox/ok" cat-file -t "$(git -C "$sandbox/ok" rev-parse v1.2.4)" 2>/dev/null)" "tag"
  check "the tag reaches the remote" \
    "$(git -C "$sandbox/ok.remote.git" tag -l v1.2.4)" "v1.2.4"
  # 分支名取自仓库本身：init.defaultBranch 因人而异，写死 master 会让
  # 这条断言在别人机器上失败，而那与被测行为无关。
  check "the branch reaches the remote" \
    "$(git -C "$sandbox/ok.remote.git" rev-parse --verify -q \
       "refs/heads/$(git -C "$sandbox/ok" symbolic-ref --short HEAD)" >/dev/null && echo yes || echo no)" "yes"
  check "the version file is bumped" \
    "$(grep -c 'v1.2.4' "$sandbox/ok/version.go")" "1"

  # --- 用例 1b：--minor 递增 MINOR 并把 PATCH 归零 ---
  #
  # 破坏性变更必须走 MINOR（项目的 Go 模块版本策略），而这条路径若没有判据，
  # 算错版本号的后果是对外契约错误：使用者按 PATCH 语义无脑升级，撞上
  # fail-closed 的破坏性变更。默认路径仍是 PATCH，因此两条都要断言。
  : > "$sandbox/gate.log"
  setup_repo "$sandbox/minor"
  run_in "$sandbox/minor" 0 --minor && status=0 || status=$?

  check "a minor release succeeds" "$status" "0"
  check "--minor bumps the minor and zeroes the patch" \
    "$(git -C "$sandbox/minor.remote.git" tag -l v1.3.0)" "v1.3.0"
  check "--minor does not create the patch tag" \
    "$(git -C "$sandbox/minor.remote.git" tag -l v1.2.4)" ""
  check "--minor bumps the version file to the minor version" \
    "$(grep -c 'v1.3.0' "$sandbox/minor/version.go")" "1"
  check "--minor still runs the aggregated release check" \
    "$(cat "$sandbox/gate.log")" "release-check"

  # --- 用例 2：门禁失败 → 无 tag、无推送、保留本地 commit ---
  : > "$sandbox/gate.log"
  setup_repo "$sandbox/fail"
  run_in "$sandbox/fail" 1 && status=0 || status=$?

  check "a failing gate blocks the release" "$status" "1"
  check "no tag is created on failure" \
    "$(git -C "$sandbox/fail" tag -l | wc -l | tr -d ' ')" "0"
  check "nothing is pushed on failure" \
    "$(git -C "$sandbox/fail.remote.git" for-each-ref | wc -l | tr -d ' ')" "0"
  check "the release commit is kept for retry" \
    "$(git -C "$sandbox/fail" log -1 --pretty=%s)" "chore(release): v1.2.4"

  # --- 用例 3：四种前置条件各自拒绝且零副作用 ---
  : > "$sandbox/gate.log"
  setup_repo "$sandbox/dirty"
  echo "stray" > "$sandbox/dirty/untracked.txt"
  run_in "$sandbox/dirty" 0 && status=0 || status=$?
  check "a dirty tree is rejected" "$status" "1"
  check "a dirty tree produces no commit" \
    "$(git -C "$sandbox/dirty" log --oneline | wc -l | tr -d ' ')" "1"
  check "a dirty tree never reaches the gate" \
    "$(wc -l < "$sandbox/gate.log" | tr -d ' ')" "0"

  setup_repo "$sandbox/detached"
  git -C "$sandbox/detached" checkout -q --detach HEAD
  run_in "$sandbox/detached" 0 && status=0 || status=$?
  check "a detached HEAD is rejected" "$status" "1"

  setup_repo "$sandbox/exists"
  git -C "$sandbox/exists" tag v1.2.4
  run_in "$sandbox/exists" 0 && status=0 || status=$?
  check "an existing tag is rejected" "$status" "1"
  check "an existing tag produces no new commit" \
    "$(git -C "$sandbox/exists" log --oneline | wc -l | tr -d ' ')" "1"

  setup_repo "$sandbox/badver" "v01.2.3"
  run_in "$sandbox/badver" 0 && status=0 || status=$?
  check "a non-canonical SemVer is rejected" "$status" "1"

  # --- 用例 4：推送目标取自仓库实际配置，不假设叫 origin ---
  #
  # 这是必须钉住的：写死 origin 的失败发生在**最后一步**——门禁跑完、
  # release commit 已建、tag 已打，然后推送失败，留下半发布状态。
  : > "$sandbox/gate.log"
  setup_repo "$sandbox/named"
  git -C "$sandbox/named" remote rename origin upstream
  run_in "$sandbox/named" 0 && status=0 || status=$?
  check "a remote not named origin still releases" "$status" "0"
  check "the tag reaches a differently named remote" \
    "$(git -C "$sandbox/named.remote.git" tag -l v1.2.4)" "v1.2.4"

  # upstream 指定了目标时按它走，即使还有其他 remote。
  #
  # 必须先把分支推上去再设 upstream：远端是空的 bare 仓库，
  # origin/<branch> 引用不存在时 --set-upstream-to 会失败，
  # 那样这个用例验的就不是"upstream 决定目标"而是"没有 upstream"。
  : > "$sandbox/gate.log"
  setup_repo "$sandbox/upstream"
  upstream_branch="$(git -C "$sandbox/upstream" symbolic-ref --short HEAD)"
  git init -q --bare "$sandbox/upstream.other.git"
  git -C "$sandbox/upstream" remote add other "$sandbox/upstream.other.git"
  git -C "$sandbox/upstream" push -q origin "HEAD:refs/heads/$upstream_branch"
  git -C "$sandbox/upstream" branch --set-upstream-to="origin/$upstream_branch" >/dev/null
  run_in "$sandbox/upstream" 0 && status=0 || status=$?
  check "an explicit upstream decides the target" "$status" "0"
  check "the upstream remote receives the tag" \
    "$(git -C "$sandbox/upstream.remote.git" tag -l v1.2.4)" "v1.2.4"
  check "the other remote is left untouched" \
    "$(git -C "$sandbox/upstream.other.git" for-each-ref | wc -l | tr -d ' ')" "0"

  # 多个 remote 且无 upstream：必须拒绝而不是猜。猜错等于把版本推到
  # 另一个仓库，那是不可撤销的外向操作。
  : > "$sandbox/gate.log"
  setup_repo "$sandbox/ambiguous"
  git -C "$sandbox/ambiguous" remote add second "$sandbox/ambiguous.remote.git"
  run_in "$sandbox/ambiguous" 0 && status=0 || status=$?
  check "an ambiguous remote set is rejected" "$status" "1"
  check "an ambiguous remote set never reaches the gate" \
    "$(wc -l < "$sandbox/gate.log" | tr -d ' ')" "0"
  check "an ambiguous remote set produces no commit" \
    "$(git -C "$sandbox/ambiguous" log --oneline | wc -l | tr -d ' ')" "1"

  if [ "$failed" -ne 0 ]; then
    exit 1
  fi
  echo "[self-test] OK: the release gate is provably invoked, and failure never publishes"
}

# BUMP_KIND 决定版本号如何递增；默认 patch，与本脚本此前的行为逐字一致。
BUMP_KIND=patch

case "${1:-}" in
  --self-test) run_self_test ;;
  "")          run_release ;;
  --minor)     BUMP_KIND=minor; run_release ;;
  *)           die "用法: $(basename "$0") [--self-test|--minor]" ;;
esac

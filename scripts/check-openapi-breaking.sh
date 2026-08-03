#!/usr/bin/env bash
# OpenAPI 破坏性变更门禁。
#
# 与「生成物一致性」是两件不同的检查：
#   一致性  —— 生成结果 == 已提交文件。不需要历史基线，**始终执行**。
#   破坏性  —— 当前契约 vs 上一个发布 tag 的契约。没有基线时**跳过并明说**。
#
# 用法：
#   check-openapi-breaking.sh              正常门禁
#   check-openapi-breaking.sh --self-test  自检：证明基线选择算法覆盖各类 tag 形态
set -euo pipefail

CONTRACT="api/openapi.json"
WAIVERS="api/breaking-waivers.yml"

# RELEASE_TAG_RE 只认无前导零的稳定 SemVer。
#
# 预发布（v1.2.3-rc1）、带前导零（v01.2.3）与任意普通 tag 都不参与基线：
# 拿一个预发布 tag 当基线，会把还没对外承诺过的契约当成已发布契约，
# 于是真正的破坏性变更反而被放过。
RELEASE_TAG_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'

# find_baseline_tag 沿 first-parent 找**上一个**发布 tag。
#
# 三条都必须写死，否则会以不同方式失效：
#
#   1. HEAD 自己已打发布 tag 时，起点改用 HEAD^1。
#      不这么做的话，基线就是当前版本自己——自己跟自己比，
#      本次发布引入的全部破坏性变更都被漏掉。
#   2. 沿 `--first-parent` 遍历，而不是全仓 tag 排序。
#      后者会取到不在当前分支祖先上的 tag（例如另一条发布线上更高的版本），
#      于是比较基线根本不是这条线的历史。
#   3. 同一 commit 有多个发布 tag 时取版本最高的那个。
find_baseline_tag() {
  local start="HEAD"

  if git tag --points-at HEAD 2>/dev/null | grep -qE "$RELEASE_TAG_RE"; then
    # HEAD 已是发布点：从它的第一父提交开始找。
    if ! git rev-parse --verify --quiet 'HEAD^1' >/dev/null; then
      # 根提交且已打 tag：没有更早的历史，进入“无基线”分支。
      return 0
    fi
    start="HEAD^1"
  fi

  local commit tag
  while IFS= read -r commit; do
    tag="$(git tag --points-at "$commit" 2>/dev/null | grep -E "$RELEASE_TAG_RE" | sort -V | tail -1)"
    if [ -n "$tag" ]; then
      printf '%s\n' "$tag"
      return 0
    fi
  done < <(git rev-list --first-parent "$start" 2>/dev/null)

  return 0
}

# run_diff 调用 Go 差分器。
#
# 用 Go 而不是 jq：`make check` 的契约是「不依赖网络、Docker 或额外工具」。
# 依赖 jq 会让门禁在没装它的机器上直接失败，而那类失败最终的处理方式
# 通常是把整条门禁跳过。
run_diff() {
  GOWORK=off go run scripts/openapi-diff.go "$1" "$2"
}

run_gate() {
  local baseline
  baseline="$(find_baseline_tag)"

  if [ -z "$baseline" ]; then
    # **明说跳过**，不是静默通过，也不是失败。
    # 母版要供 gonew 引用，迟早会打 tag；首个 tag 之后这条门禁自动生效。
    echo "openapi breaking check SKIPPED: no release tag found, so there is no baseline yet"
    return 0
  fi

  local tmp
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" EXIT

  if ! git show "$baseline:$CONTRACT" > "$tmp/baseline.json" 2>/dev/null; then
    echo "openapi breaking check SKIPPED: $baseline does not contain $CONTRACT"
    return 0
  fi

  echo "openapi breaking check against $baseline"

  local output
  if output="$(run_diff "$tmp/baseline.json" "$CONTRACT" 2>&1)"; then
    echo "openapi breaking check OK"
    return 0
  fi

  echo "BREAKING changes detected:"
  printf '%s\n' "$output" | sed 's/^/  /'

  if waived "$output"; then
    echo "openapi breaking check: all findings are waived"
    return 0
  fi
  echo "FAIL: breaking changes are not waived. Add entries to $WAIVERS or revert the change."
  return 1
}

# waived 判断全部 finding 是否都有有效 waiver。
#
# waiver 必须逐项声明，不提供"全局关闭"——一个能被整体关掉的门禁
# 在第一次挡路时就会被关掉，之后再也不会被打开。
waived() {
  local findings="$1"

  [ -f "$WAIVERS" ] || return 1

  # 过期 waiver 一律失败：一条永不过期的豁免与"删掉这条规则"没有区别。
  local today expired
  today="$(date -u +%Y-%m-%d)"
  expired="$(grep -E '^\s*expires:' "$WAIVERS" 2>/dev/null | awk '{print $2}' |
    while IFS= read -r d; do [ "$d" \< "$today" ] && echo "$d"; done)"
  if [ -n "$expired" ]; then
    echo "FAIL: expired waiver(s) in $WAIVERS: $expired"
    return 1
  fi

  # 每条 finding 都必须被某条 waiver 的 operation 命中。
  # 差分器每行输出 "METHOD /path|rule|detail"。
  local line op
  while IFS='|' read -r op _ _; do
    [ -n "$op" ] || continue
    if ! grep -qF "$op" "$WAIVERS"; then
      echo "FAIL: no waiver covers: $op"
      return 1
    fi
  done <<< "$findings"
  unset line

  return 0
}

# run_self_test 证明基线选择算法覆盖各类 tag 形态。
#
# 在一次性沙盒仓库里造出各种 tag 状态，因此完全不触碰真实仓库。
run_self_test() {
  local sandbox
  sandbox="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$sandbox'" EXIT

  cd "$sandbox"
  git init -q .
  git config user.name gate
  git config user.email gate@local
  git config commit.gpgsign false

  local failed=0
  assert() {
    if [ "$2" != "$3" ]; then
      echo "[self-test] FAIL: $1 => got '$2', want '$3'"
      failed=1
    else
      echo "[self-test] OK: $1 => '$2'"
    fi
  }

  echo a > f && git add -A && git commit -q -m c1
  git tag v1.0.0
  # 根提交已打 tag：没有更早历史 → 无基线
  assert "root commit with a tag has no earlier baseline" "$(find_baseline_tag)" ""

  echo b > f && git commit -q -am c2
  # HEAD 未打 tag → 取最近的祖先 tag
  assert "untagged HEAD finds the nearest ancestor tag" "$(find_baseline_tag)" "v1.0.0"

  # 畸形与预发布 tag 不参与基线
  git tag v01.2.3
  git tag v2.0.0-rc1
  assert "malformed and prerelease tags are ignored" "$(find_baseline_tag)" "v1.0.0"

  echo c > f && git commit -q -am c3
  git tag v1.1.0
  git tag v1.2.0
  # HEAD 已打发布 tag → 从 HEAD^1 找；同 commit 多 tag 不影响更早的基线
  assert "tagged HEAD searches from its parent" "$(find_baseline_tag)" "v1.0.0"

  echo d > f && git commit -q -am c4
  # 同一 commit 多个稳定 tag 取最高版本
  assert "multiple stable tags on one commit take the highest" "$(find_baseline_tag)" "v1.2.0"

  if [ "$failed" -ne 0 ]; then
    exit 1
  fi
  echo "[self-test] OK: baseline selection covers all tag shapes"
}

case "${1:-}" in
  --self-test) run_self_test ;;
  "")          run_gate ;;
  *)           echo "用法: $(basename "$0") [--self-test]" >&2; exit 2 ;;
esac

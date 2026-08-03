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

# ROOT_DIR 固定为仓库根。
#
# 自检会 cd 进一次性沙盒，之后再用相对路径调用 `go run scripts/...` 会落空——
# 而那种落空表现为"每条规则都没触发"，看起来和门禁本身失效一模一样。
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

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
#
# **stderr 必须丢掉**：`go run` 在子进程非零退出时会往 stderr 打印
# "exit status 1"，混进 finding 列表后会被当成一条 operation 去找 waiver，
# 于是门禁永远说"没有 waiver 覆盖 exit status 1"，真实结论被这条噪音盖住。
# 退出码承载全部语义：0 无破坏，1 有 finding（在 stdout），2 工具自身失败。
run_diff() {
  (cd "$ROOT_DIR" && GOWORK=off go run scripts/openapi-diff.go "$1" "$2" 2>/dev/null)
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

  local output status
  output="$(run_diff "$tmp/baseline.json" "$CONTRACT")" && status=0 || status=$?

  if [ "$status" -eq 0 ]; then
    echo "openapi breaking check OK"
    return 0
  fi
  if [ "$status" -ne 1 ]; then
    # 工具自身失败（读不了文件、JSON 解析不了）绝不能当成"没有破坏"。
    echo "FAIL: openapi-diff exited with $status; re-run it directly to see the error"
    return 1
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

  # 字段不全的 waiver 一律失败：缺 owner 就没人对这次破坏负责，
  # 缺 reason 就无从判断它是否仍然成立——那样的豁免等同于静默放行。
  local entries required missing
  entries="$(grep -cE '^\s*-\s+id:' "$WAIVERS" 2>/dev/null || true)"
  if [ "${entries:-0}" -gt 0 ]; then
    for required in operation rule owner reason expires; do
      missing="$(grep -cE "^\s+${required}:" "$WAIVERS" 2>/dev/null || true)"
      if [ "${missing:-0}" -lt "$entries" ]; then
        echo "FAIL: $WAIVERS has $entries waiver(s) but only ${missing:-0} '$required' field(s)"
        return 1
      fi
    done
  fi

  # 每条 finding 都必须被某条 waiver 的 operation **与规则**同时命中。
  # 只匹配 operation 会让一条针对 A 规则的豁免顺带放过同一接口上的 B 规则。
  # 差分器每行输出 "METHOD /path|rule|detail"。
  local op rule
  while IFS='|' read -r op rule _; do
    [ -n "$op" ] || continue
    if ! grep -qF "$op" "$WAIVERS"; then
      echo "FAIL: no waiver covers: $op"
      return 1
    fi
    if ! grep -qF "$rule" "$WAIVERS"; then
      echo "FAIL: no waiver covers rule $rule on: $op"
      return 1
    fi
  done <<< "$findings"

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

# run_diff_self_test 证明每条破坏性规则**真的会触发**。
#
# 没有负向证明的门禁等于没有门禁：一条永远不报警的规则与删掉它没有区别，
# 而它的存在会让人以为这一面已经被守住了。
run_diff_self_test() {
  local sandbox
  sandbox="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$sandbox'" RETURN

  # 基线：一个带请求体与响应体的操作。
  cat > "$sandbox/base.json" <<'JSON'
{"paths":{"/x":{"post":{"operationId":"doX",
  "requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Req"}}}},
  "responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Res"}}}}}}}},
 "components":{"schemas":{
   "Req":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},
   "Res":{"type":"object","properties":{"id":{"type":"integer","format":"int64"},
                                        "name":{"type":"string"},
                                        "note":{"type":"string"}},
          "required":["id","name","note"]}}}}
JSON

  # 当前：五种破坏各来一次。
  cat > "$sandbox/cur.json" <<'JSON'
{"paths":{"/x":{"post":{"operationId":"doX",
  "requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Req"}}}},
  "responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Res"}}}}}}}},
 "components":{"schemas":{
   "Req":{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"string"}},
          "required":["a","b"]},
   "Res":{"type":"object","properties":{"id":{"type":["integer","null"]},
                                        "name":{"type":"string"}},
          "required":["id"]}}}}
JSON

  local output failed=0
  output="$(run_diff "$sandbox/base.json" "$sandbox/cur.json" 2>&1)" && {
    echo "[self-test] FAIL: the diff reported no breaking change on a deliberately broken contract"
    return 1
  }

  local rule
  for rule in request_field_became_required request_field_type_narrowed \
              response_field_removed response_field_no_longer_guaranteed \
              response_field_type_widened; do
    if printf '%s' "$output" | grep -q "$rule"; then
      echo "[self-test] OK: $rule fires"
    else
      echo "[self-test] FAIL: $rule never fired"
      failed=1
    fi
  done

  # 反向：安全的变更绝不能报警。一条误报的门禁最终会被关掉。
  cat > "$sandbox/safe.json" <<'JSON'
{"paths":{"/x":{"post":{"operationId":"doX",
  "requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Req"}}}},
  "responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Res"}}}}}}}},
 "components":{"schemas":{
   "Req":{"type":"object","properties":{"a":{"type":"string"},"opt":{"type":"string"}},
          "required":["a"]},
   "Res":{"type":"object","properties":{"id":{"type":"integer","format":"int64"},
                                        "name":{"type":"string"},
                                        "note":{"type":"string"},
                                        "extra":{"type":"string"}},
          "required":["id","name","note","extra"]}}}}
JSON

  if run_diff "$sandbox/base.json" "$sandbox/safe.json" >/dev/null 2>&1; then
    echo "[self-test] OK: adding an optional request field and a new response field is not breaking"
  else
    echo "[self-test] FAIL: a backwards-compatible change was reported as breaking"
    failed=1
  fi

  # 第二组：参数、媒体类型、状态码与类型之外的约束。
  cat > "$sandbox/base2.json" <<'JSON'
{"paths":{"/y/{id}":{"post":{"operationId":"doY",
  "parameters":[{"name":"id","in":"path","required":true},
                {"name":"trace","in":"header","required":false},
                {"name":"drop","in":"query","required":true}],
  "requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Q"}},
                            "application/xml":{"schema":{"$ref":"#/components/schemas/Q"}}}},
  "responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/S"}}}}}}}},
 "components":{"schemas":{
   "Q":{"type":"object","additionalProperties":true,
        "properties":{"kind":{"type":"string","enum":["a","b","c"]},
                      "size":{"type":"integer","minimum":0,"maximum":100},
                      "note":{"type":"string","minLength":1,"maxLength":200},
                      "code":{"type":"string","pattern":"^[a-z]+$"},
                      "ts":{"type":"integer","format":"int64"}}},
   "S":{"type":"object","required":["state","score"],
        "properties":{"state":{"type":"string","enum":["ok"]},
                      "score":{"type":"integer","minimum":0,"maximum":10}}}}}}
JSON

  cat > "$sandbox/cur2.json" <<'JSON'
{"paths":{"/y/{id}":{"post":{"operationId":"doY",
  "parameters":[{"name":"id","in":"path","required":true},
                {"name":"trace","in":"header","required":true},
                {"name":"page","in":"query","required":true}],
  "requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Q"}}}},
  "responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/S"}}}},
               "201":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/S"}}}}}}}},
 "components":{"schemas":{
   "Q":{"type":"object","additionalProperties":false,
        "properties":{"kind":{"type":"string","enum":["a","b"]},
                      "size":{"type":"integer","minimum":1,"maximum":50},
                      "note":{"type":"string","minLength":5,"maxLength":100},
                      "code":{"type":"string","pattern":"^[A-Z]+$"},
                      "ts":{"type":"integer","format":"int32"}}},
   "S":{"type":"object","required":["state","score"],
        "properties":{"state":{"type":"string","enum":["ok","degraded"]},
                      "score":{"type":"integer","minimum":-5,"maximum":50}}}}}}
JSON

  output="$(run_diff "$sandbox/base2.json" "$sandbox/cur2.json" 2>&1)" && {
    echo "[self-test] FAIL: the diff reported no breaking change on the second broken contract"
    return 1
  }

  for rule in parameter_removed parameter_became_required parameter_added_required \
              request_media_type_removed success_status_added \
              request_field_format_changed request_field_enum_narrowed \
              request_field_minimum_raised request_field_maximum_lowered \
              request_field_min_length_raised request_field_max_length_lowered \
              request_field_pattern_changed request_field_additional_properties_closed \
              response_field_enum_widened response_field_minimum_lowered \
              response_field_maximum_raised; do
    if printf '%s' "$output" | grep -q "$rule"; then
      echo "[self-test] OK: $rule fires"
    else
      echo "[self-test] FAIL: $rule never fired"
      failed=1
    fi
  done

  # 响应媒体类型删除单独构造：上一组的 200 两侧都是 json。
  cat > "$sandbox/cur3.json" <<'JSON'
{"paths":{"/y/{id}":{"post":{"operationId":"doY",
  "parameters":[{"name":"id","in":"path","required":true},
                {"name":"trace","in":"header","required":false},
                {"name":"drop","in":"query","required":true}],
  "requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Q"}},
                            "application/xml":{"schema":{"$ref":"#/components/schemas/Q"}}}},
  "responses":{"200":{"content":{}}}}}},
 "components":{"schemas":{"Q":{"type":"object"}}}}
JSON
  output="$(run_diff "$sandbox/base2.json" "$sandbox/cur3.json" 2>&1)" || true
  if printf '%s' "$output" | grep -q response_media_type_removed; then
    echo "[self-test] OK: response_media_type_removed fires"
  else
    echo "[self-test] FAIL: response_media_type_removed never fired"
    failed=1
  fi

  # 反向：补齐一条原本缺失的约束不该被当成放宽。
  # 只在**两侧都声明**时才比较 format，否则"补上精度信息"会被误报。
  cat > "$sandbox/safe2.json" <<'JSON'
{"paths":{"/y/{id}":{"post":{"operationId":"doY",
  "parameters":[{"name":"id","in":"path","required":true},
                {"name":"trace","in":"header","required":false},
                {"name":"drop","in":"query","required":true},
                {"name":"opt","in":"query","required":false}],
  "requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Q"}},
                            "application/xml":{"schema":{"$ref":"#/components/schemas/Q"}}}},
  "responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/S"}}}},
               "409":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/S"}}}}}}}},
 "components":{"schemas":{
   "Q":{"type":"object","additionalProperties":true,
        "properties":{"kind":{"type":"string","enum":["a","b","c","d"]},
                      "size":{"type":"integer","minimum":0,"maximum":200},
                      "note":{"type":"string","minLength":1,"maxLength":200},
                      "code":{"type":"string","pattern":"^[a-z]+$"},
                      "ts":{"type":"integer","format":"int64"}}},
   "S":{"type":"object","required":["state","score"],
        "properties":{"state":{"type":"string","enum":["ok"]},
                      "score":{"type":"integer","minimum":1,"maximum":9}}}}}}
JSON
  if run_diff "$sandbox/base2.json" "$sandbox/safe2.json" >/dev/null 2>&1; then
    echo "[self-test] OK: optional params, new error codes and relaxed request bounds are not breaking"
  else
    echo "[self-test] FAIL: a backwards-compatible change was reported as breaking:"
    run_diff "$sandbox/base2.json" "$sandbox/safe2.json" 2>&1 | sed 's/^/    /'
    failed=1
  fi

  return "$failed"
}

case "${1:-}" in
  --self-test) run_self_test && run_diff_self_test ;;
  "")          run_gate ;;
  *)           echo "用法: $(basename "$0") [--self-test]" >&2; exit 2 ;;
esac

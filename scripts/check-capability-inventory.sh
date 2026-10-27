#!/usr/bin/env bash
# check-capability-inventory.sh — 校验 SCAFFOLD.md 的预置能力清单是否穷尽。
#
# 为什么需要它：清单的价值全在"完整"。使用者不会去找一个他不知道存在的能力，
# 因此漏项是最安静的一类缺陷——比"标注错了"更难被发现。当漏掉的能力具备对外
# 副作用时（本仓的告警扇出会真实发送短信与邮件），代价由不知情的派生项目在
# 生产上承担。
#
# 用法：
#   check-capability-inventory.sh                 校验清单
#   check-capability-inventory.sh --print-scope   打印覆盖范围
#   check-capability-inventory.sh --self-test     自检：证明每个 scope 真的在扫
#
# ── 覆盖范围（由 SCOPES 定义，与扫描行为同源）─────────────────────────
# 判据覆盖四处，缺一即失败：
#   internal/pkg/*/            内部共享包
#   pkg/*/                     对外可复用包
#   internal/middleware/*.go   中间件（按文件登记）
#   config-sections            Config 结构体的顶层 mapstructure 段
#
# 这份声明不是注释里的一段话——`SCOPES` 既是本注释描述的对象，也是主扫描循环
# 遍历的对象，还是 `--self-test` 逐个构造缺失条目的依据。三者同源，
# 因此"声明覆盖 A、实际只扫 B"在结构上不可能发生。
#
# 曾经可能：扩围前判据只扫 internal/pkg 与配置段，而注释没有声明这一点，
# 读者据此以为它是全仓生效的。代价是 `internal/middleware/file.go` 那个
# 零接线且 fail-open 的请求校验中间件长期无人知晓——没有任何机制要求它被登记，
# 于是没写说明的东西不会被发现。
#
# 覆盖范围**不含** internal/ 下的其它目录（bootstrap/router/repository/models 等）：
# 它们是装配与业务实现，不是"派生项目需要知道其存在才能使用"的预置能力。
# 把它们纳入会让清单退化成文件列表，而一份列出全部文件的清单等于没有清单。
#
# ── 能力边界（必须如实声明）────────────────────────────────────────────
# 这是一条**存在性**判据。它检测不到：
#   * 能力被提及，但未归入"默认开启 / 需接线 / 示例可删"三类之一；
#   * 归错了类（把需接线的写成默认开启）；
#   * 说明与实际行为不符（写着默认关闭，实际默认开启）。
#
# 扩大覆盖范围不等于提高检查强度，两者不能混为一谈：扫得更全，仍然只是在问
# "这个名字出现过吗"。
#
# 两类条目的匹配精度也不同：路径型条目（前三个 scope）按完整路径带边界匹配，
# 不会误命中；配置段按裸名匹配，短名（`env`、`log`）可能因无关文本而通过。
# 收紧配置段需要约定清单里的书写形式，收益不抵约束成本——但这个弱点必须写在这里，
# 而不是让读者以为两边一样严。
#
# 因此 MUST NOT 把它表述为"清单正确性已被自动化保证"。本仓对「重新绑定检查」
# 有同样的要求——一个被当成完整保证的部分检查，比没有检查更容易让人放松：
# 它会让审查者跳过那些它根本没看的维度。
# ─────────────────────────────────────────────────────────────────────
set -euo pipefail

# SCOPES 是覆盖范围的**唯一定义**。新增一项必须同时在 scope_describe /
# scope_entries / scope_match_kind / scope_fixture 四处实现，否则 --self-test 失败。
SCOPES=(internal/pkg pkg internal/middleware config-sections)

INVENTORY_NAME="SCAFFOLD.md"
CONFIG_TYPES_REL="internal/platform/config/types.go"

# scope_describe 给出该 scope 的枚举口径，供 --print-scope 输出。
scope_describe() {
  case "$1" in
    internal/pkg) echo "internal/pkg/*/            → 条目 internal/pkg/<name>（路径匹配）" ;;
    pkg) echo "pkg/*/                     → 条目 pkg/<name>（路径匹配）" ;;
    internal/middleware) echo "internal/middleware/*.go   → 条目 internal/middleware/<file>.go（路径匹配，跳过 _test.go）" ;;
    config-sections) echo "Config 顶层 mapstructure   → 条目 <section>（裸名匹配，精度较低，见能力边界）" ;;
    *)
      echo "FAIL: scope_describe 未实现 scope '$1'" >&2
      return 1
      ;;
  esac
}

# scope_entries 枚举该 scope 下的全部条目，每行一个。
# 输出的字符串就是要在清单中查找的文本。
scope_entries() {
  case "$1" in
    internal/pkg)
      for dir in "$ROOT_DIR"/internal/pkg/*/; do
        [ -d "$dir" ] || continue
        echo "internal/pkg/$(basename "$dir")"
      done
      ;;
    pkg)
      for dir in "$ROOT_DIR"/pkg/*/; do
        [ -d "$dir" ] || continue
        echo "pkg/$(basename "$dir")"
      done
      ;;
    internal/middleware)
      for f in "$ROOT_DIR"/internal/middleware/*.go; do
        [ -f "$f" ] || continue
        base="$(basename "$f")"
        case "$base" in
          *_test.go) continue ;;
        esac
        echo "internal/middleware/${base}"
      done
      ;;
    config-sections)
      # 只取 Config 结构体自身的字段：从 `type Config struct {` 到第一个 `}` 为止。
      # 取全文件会把嵌套结构体的字段一并算进来，那些不是顶层配置段。
      local types_file="$ROOT_DIR/$CONFIG_TYPES_REL"
      if [ ! -f "$types_file" ]; then
        echo "FAIL: $CONFIG_TYPES_REL not found; cannot enumerate config sections" >&2
        return 1
      fi
      local sections
      sections="$(
        sed -n '/^type Config struct {/,/^}/p' "$types_file" |
          sed -n -E 's/.*mapstructure:"([a-z_]+)".*/\1/p'
      )"
      if [ -z "$sections" ]; then
        echo "FAIL: cannot enumerate top-level config sections from $CONFIG_TYPES_REL" >&2
        echo "      (the Config struct shape changed; fix this script rather than dropping the check)" >&2
        return 1
      fi
      echo "$sections"
      ;;
    *)
      echo "FAIL: scope_entries 未实现 scope '$1'" >&2
      return 1
      ;;
  esac
}

# scope_match_kind 决定条目用哪种匹配：path（带边界的完整路径）或 bare（裸名子串）。
scope_match_kind() {
  case "$1" in
    internal/pkg | pkg | internal/middleware) echo "path" ;;
    config-sections) echo "bare" ;;
    *)
      echo "FAIL: scope_match_kind 未实现 scope '$1'" >&2
      return 1
      ;;
  esac
}

# entry_registered 判断条目是否已登记进清单。
#
# path 型必须带边界，不能用 grep -F 直接找子串：`pkg/cache` 会命中清单里的
# `internal/pkg/cache`。即便当前没有根 pkg/ 公共包，也必须保留完整路径边界，
# 避免不同层级出现同名包时静默误通过。
#
# 后置边界同理排除前缀误命中：`internal/pkg/ratelimit` 不应被清单里的 `internal/pkg/ratelimiter` 满足。
entry_registered() {
  local entry="$1" kind="$2" inventory="$3"
  case "$kind" in
    path)
      # 合法路径只含字母数字与 . / _ -，其中唯一的 ERE 元字符是 `.`，只需转义它。
      local escaped
      escaped="$(printf '%s' "$entry" | sed 's/\./\\./g')"
      grep -qE "(^|[^/[:alnum:]_.-])${escaped}([^/[:alnum:]_.-]|\$)" "$inventory"
      ;;
    bare)
      grep -qF "$entry" "$inventory"
      ;;
    *)
      echo "FAIL: unknown match kind '$kind'" >&2
      return 1
      ;;
  esac
}

# run_check 执行一次完整校验。ROOT_DIR 由调用方设置。
run_check() {
  local inventory="$ROOT_DIR/$INVENTORY_NAME"
  if [ ! -f "$inventory" ]; then
    echo "FAIL: capability inventory $INVENTORY_NAME not found under $ROOT_DIR" >&2
    return 1
  fi

  local fail=0 missing="" scope kind entry
  for scope in "${SCOPES[@]}"; do
    kind="$(scope_match_kind "$scope")"
    while IFS= read -r entry; do
      [ -n "$entry" ] || continue
      if ! entry_registered "$entry" "$kind" "$inventory"; then
        missing="${missing}  [${scope}] ${entry}"$'\n'
        fail=1
      fi
    done <<<"$(scope_entries "$scope")"
  done

  if [ "$fail" -ne 0 ]; then
    echo "FAIL: preinstalled capabilities missing from $INVENTORY_NAME:"
    printf '%s' "$missing"
    echo ""
    echo "每一项都必须被登记：或归入「默认开启 / 需要接线 / 示例可删」三类能力之一，"
    echo "或明示为不构成能力的内部支撑包。"
    echo "具备对外副作用的能力（向外部通道发送消息、写入外部系统）还须写明误配置时的后果。"
    return 1
  fi

  echo "capability inventory check OK"
}

# ── --self-test ───────────────────────────────────────────────────────
#
# 自检遍历 --print-scope 的输出，而不是硬编码 scope 列表：新增 scope 时自检
# 自动覆盖它。硬编码的话，"有一条自检"会被误当成"每个 scope 都被验证过"——
# 与本脚本要消除的那类虚假保证同形。
#
# 每个 scope 两个方向都断言：造一个未登记条目必须失败，登记后必须通过。
# 只断言前者不足以证明判据有效——一条永远失败的判据也能通过那个断言。

# scope_fixture 在临时工作树里为该 scope 造一个名为 probe 的条目。
scope_fixture() {
  local scope="$1" root="$2"
  case "$scope" in
    internal/pkg) mkdir -p "$root/internal/pkg/probe" ;;
    pkg) mkdir -p "$root/pkg/probe" ;;
    internal/middleware)
      mkdir -p "$root/internal/middleware"
      echo "package middleware" >"$root/internal/middleware/probe.go"
      # 同时造一个 _test.go，验证它被跳过（否则自检会因它未登记而误报失败）。
      echo "package middleware" >"$root/internal/middleware/probe_test.go"
      ;;
    config-sections)
      mkdir -p "$root/$(dirname "$CONFIG_TYPES_REL")"
      cat >"$root/$CONFIG_TYPES_REL" <<'EOF'
package config

type Config struct {
	Probe ProbeConfig `mapstructure:"probe"`
}
EOF
      ;;
    *)
      echo "FAIL: scope_fixture 未实现 scope '$1'" >&2
      return 1
      ;;
  esac
}

# scope_probe_entry 给出该 scope 下 probe 条目在清单中应有的写法。
scope_probe_entry() {
  case "$1" in
    internal/pkg) echo "internal/pkg/probe" ;;
    pkg) echo "pkg/probe" ;;
    internal/middleware) echo "internal/middleware/probe.go" ;;
    config-sections) echo "probe" ;;
    *)
      echo "FAIL: scope_probe_entry 未实现 scope '$1'" >&2
      return 1
      ;;
  esac
}

self_test_baseline() {
  # 每个临时工作树都要有一个合法的 types.go：config-sections 枚举不到内容时
  # 判据会直接报错退出，那会让其它 scope 的断言无法区分"扫到了缺失"与"脚本崩了"。
  local root="$1"
  mkdir -p "$root/$(dirname "$CONFIG_TYPES_REL")"
  cat >"$root/$CONFIG_TYPES_REL" <<'EOF'
package config

type Config struct {
	Baseline BaselineConfig `mapstructure:"baseline"`
}
EOF
  echo "baseline" >"$root/$INVENTORY_NAME"
}

self_test() {
  local script_path="$1"
  local scope tmp probe rc failures=0

  for scope in "${SCOPES[@]}"; do
    probe="$(scope_probe_entry "$scope")"

    # 方向一：未登记必须失败。
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    self_test_baseline "$tmp"
    scope_fixture "$scope" "$tmp"
    rc=0
    INVENTORY_ROOT="$tmp" "$script_path" >/dev/null 2>&1 || rc=$?
    if [ "$rc" -eq 0 ]; then
      echo "FAIL: self-test scope '$scope' — 未登记的条目 '$probe' 竟然通过了判据" >&2
      echo "      说明该 scope 没有被真正扫描（检查 scope_entries 的实现）" >&2
      failures=1
    fi

    # 方向二：登记后必须通过。
    echo "$probe" >>"$tmp/$INVENTORY_NAME"
    rc=0
    INVENTORY_ROOT="$tmp" "$script_path" >/dev/null 2>&1 || rc=$?
    if [ "$rc" -ne 0 ]; then
      echo "FAIL: self-test scope '$scope' — 已登记的条目 '$probe' 仍被判为缺失" >&2
      echo "      说明清单写法与匹配式对不上（检查 entry_registered）" >&2
      failures=1
    fi

    rm -rf "$tmp"
    trap - EXIT
  done

  # 决策 2 的反证：根 pkg/ 的条目不得被 internal/pkg/ 的同名条目满足。
  # 这是扩围引入的新失败模式，必须有一个"若边界匹配写错就会失败"的断言。
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  self_test_baseline "$tmp"
  mkdir -p "$tmp/pkg/probe"
  echo "internal/pkg/probe" >>"$tmp/$INVENTORY_NAME"
  rc=0
  INVENTORY_ROOT="$tmp" "$script_path" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: self-test 边界 — 清单里只有 'internal/pkg/probe' 时，'pkg/probe' 竟被判为已登记" >&2
    echo "      说明路径匹配缺少前置边界，根 pkg/ 的条目会被 internal/pkg/ 的同名条目静默满足" >&2
    failures=1
  fi
  rm -rf "$tmp"
  trap - EXIT

  if [ "$failures" -ne 0 ]; then
    return 1
  fi
  echo "capability inventory self-test OK (${#SCOPES[@]} scopes + boundary assertion)"
}

# ── 入口 ──────────────────────────────────────────────────────────────
#
# INVENTORY_ROOT 供 --self-test 的子调用指向临时工作树；日常使用不设置它。
if [ -n "${INVENTORY_ROOT:-}" ]; then
  ROOT_DIR="$INVENTORY_ROOT"
else
  ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
fi

case "${1:-}" in
  --print-scope)
    echo "capability inventory scopes (${#SCOPES[@]}):"
    for s in "${SCOPES[@]}"; do
      printf '  %-22s %s\n' "$s" "$(scope_describe "$s")"
    done
    ;;
  --self-test)
    self_test "$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
    ;;
  "")
    run_check
    ;;
  *)
    echo "usage: $(basename "$0") [--print-scope | --self-test]" >&2
    exit 2
    ;;
esac

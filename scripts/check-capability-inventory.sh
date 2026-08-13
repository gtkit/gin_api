#!/usr/bin/env bash
# check-capability-inventory.sh — 校验 SCAFFOLD.md 的预置能力清单是否穷尽。
#
# 为什么需要它：清单的价值全在"完整"。使用者不会去找一个他不知道存在的能力，
# 因此漏项是最安静的一类缺陷——比"标注错了"更难被发现。当漏掉的能力具备对外
# 副作用时（本仓的告警扇出会真实发送短信与邮件），代价由不知情的派生项目在
# 生产上承担。
#
# 判据：internal/pkg 下的每个包名、配置的每个顶层 mapstructure 段名，都必须在
# SCAFFOLD.md 中出现。
#
# ── 能力边界（必须如实声明）────────────────────────────────────────────
# 这是一条**存在性**判据。它检测不到：
#   * 能力被提及，但未归入"默认开启 / 需接线 / 示例可删"三类之一；
#   * 归错了类（把需接线的写成默认开启）；
#   * 说明与实际行为不符（写着默认关闭，实际默认开启）。
#
# 两类条目的匹配精度也不同：包按完整路径 `internal/pkg/<name>` 匹配，不会误命中；
# 配置段按裸名匹配，短名（`env`、`log`）可能因无关文本而通过。收紧配置段需要
# 约定清单里的书写形式，收益不抵约束成本——但这个弱点必须写在这里，
# 而不是让读者以为两边一样严。
#
# 因此 MUST NOT 把它表述为"清单正确性已被自动化保证"。本仓对「重新绑定检查」
# 有同样的要求——一个被当成完整保证的部分检查，比没有检查更容易让人放松：
# 它会让审查者跳过那些它根本没看的维度。
# ─────────────────────────────────────────────────────────────────────
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

INVENTORY="SCAFFOLD.md"
CONFIG_TYPES="internal/platform/config/types.go"

if [ ! -f "$INVENTORY" ]; then
  echo "FAIL: capability inventory $INVENTORY not found" >&2
  exit 1
fi
if [ ! -f "$CONFIG_TYPES" ]; then
  echo "FAIL: $CONFIG_TYPES not found; cannot enumerate config sections" >&2
  exit 1
fi

fail=0
missing=""

# 1) internal/pkg 下的每个包。
#
# 匹配**完整路径** `internal/pkg/<name>` 而不是裸包名：裸包名会意外命中。
# 实测过一例——`internal/pkg/log` 在登记之前就"通过"了，因为清单里别处出现过
# `internal/middleware/logger.go`。一条会因无关文本而通过的判据，给出的是
# 虚假的合规保证。
for dir in internal/pkg/*/; do
  [ -d "$dir" ] || continue
  name="$(basename "$dir")"
  if ! grep -qF "internal/pkg/${name}" "$INVENTORY"; then
    missing="${missing}  internal/pkg/${name}"$'\n'
    fail=1
  fi
done

# 2) 配置的顶层 mapstructure 段。
#
# 只取 Config 结构体自身的字段：从 `type Config struct {` 到第一个 `}` 为止。
# 取全文件会把嵌套结构体的字段一并算进来，那些不是顶层配置段。
config_sections="$(
  sed -n '/^type Config struct {/,/^}/p' "$CONFIG_TYPES" |
    sed -n -E 's/.*mapstructure:"([a-z_]+)".*/\1/p'
)"

if [ -z "$config_sections" ]; then
  echo "FAIL: cannot enumerate top-level config sections from $CONFIG_TYPES" >&2
  echo "      (the Config struct shape changed; fix this script rather than dropping the check)" >&2
  exit 1
fi

for section in $config_sections; do
  if ! grep -qF "$section" "$INVENTORY"; then
    missing="${missing}  config section: ${section}"$'\n'
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "FAIL: preinstalled capabilities missing from $INVENTORY:"
  printf '%s' "$missing"
  echo ""
  echo "每一项都必须被登记：或归入「默认开启 / 需要接线 / 示例可删」三类能力之一，"
  echo "或明示为不构成能力的内部支撑包。"
  echo "具备对外副作用的能力（向外部通道发送消息、写入外部系统）还须写明误配置时的后果。"
  exit 1
fi

echo "capability inventory check OK"

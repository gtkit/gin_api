#!/usr/bin/env bash
# =============================================================================
# pgo-merge.sh —— PGO（性能引导优化）profile 采集 / 合并 / 校验
#
# PGO 需要一份能代表线上真实负载的 CPU profile，命名为 default.pgo 放在
# main 包目录（本项目即项目根目录），go build 会自动启用优化。
#
# 子命令：
#   collect <次数> <每次秒数>   从 PPROF_ADDR 采集多份 CPU profile（分散采样更具代表性）
#                               默认：3 次，每次 30 秒，存到 ../profiles/pgo/
#   merge                       合并 ../profiles/pgo/ 下所有 *.pprof 为 项目根/default.pgo
#   verify                      校验 default.pgo 是否有效、构建是否真正启用了 PGO
#   clean                       清空 ../profiles/pgo/ 下临时 profile，不删除 default.pgo
#
# 环境变量：
#   PPROF_ADDR  pprof 端点地址，默认 127.0.0.1:6060（pprof 在业务端口则改 127.0.0.1:8008 之类）
#
# 典型流程：
#   1) 让服务承载代表性流量
#   2) ./pgo-merge.sh collect 3 30
#   3) ./pgo-merge.sh merge
#   4) ./pgo-merge.sh verify
#   5) 提交 default.pgo 到仓库；之后 make build 自动带 PGO
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
PGO_DIR="${SCRIPT_DIR}/../profiles/pgo"
DEFAULT_PGO="${PROJECT_ROOT}/default.pgo"

PPROF_ADDR="${PPROF_ADDR:-127.0.0.1:6060}"
PROFILE_URL="http://${PPROF_ADDR}/debug/pprof/profile"

validate_positive_int() {
  local value="$1"
  local label="$2"

  if ! [[ "${value}" =~ ^[1-9][0-9]*$ ]]; then
    echo "✗ ${label} 必须是正整数，当前=${value}"
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# collect：采集多份 CPU profile
# ---------------------------------------------------------------------------
cmd_collect() {
  local times="${1:-3}"     # 采集份数
  local secs="${2:-30}"     # 每份秒数
  validate_positive_int "${times}" "采集次数"
  validate_positive_int "${secs}" "每次秒数"
  mkdir -p "${PGO_DIR}"

  echo "=== 采集 PGO profile | 目标=${PPROF_ADDR} | ${times} 份 × ${secs} 秒 ==="
  echo "    提示：采集期间务必让服务处于真实/压测负载，否则采到的是 idle。"

  for i in $(seq 1 "${times}"); do
    local ts
    ts="$(date +%Y%m%d-%H%M%S)"
    local out="${PGO_DIR}/cpu-${ts}-${i}.pprof"
    echo "▶ [${i}/${times}] 采集 ${secs} 秒 -> ${out}"
    if curl -fsS --max-time $((secs + 30)) -o "${out}" "${PROFILE_URL}?seconds=${secs}"; then
      echo "  ✓ ${out}"
    else
      echo "  ✗ 采集失败，检查服务与 PPROF_ADDR=${PPROF_ADDR}"
      exit 1
    fi
  done
  echo "=== 采集完成，下一步：${0} merge ==="
}

# ---------------------------------------------------------------------------
# merge：合并为 default.pgo
# ---------------------------------------------------------------------------
cmd_merge() {
  shopt -s nullglob
  local profiles=("${PGO_DIR}"/*.pprof)
  shopt -u nullglob

  if [ "${#profiles[@]}" -eq 0 ]; then
    echo "✗ ${PGO_DIR} 下没有 *.pprof，先执行：${0} collect"
    exit 1
  fi

  echo "=== 合并以下 profile 为 default.pgo ==="
  printf '%s\n' "${profiles[@]}"

  # go tool pprof -proto 把多份 profile 累加合并并输出标准 protobuf 格式到 stdout。
  # 多份合并能平滑单次采样的偶然偏差，得到更稳定的热点画像。
  # 先写临时文件再 mv，避免合并失败时留下半截 default.pgo。
  local tmp
  tmp="$(mktemp "${DEFAULT_PGO}.tmp.XXXXXX")"
  go tool pprof -proto "${profiles[@]}" > "${tmp}"
  mv "${tmp}" "${DEFAULT_PGO}"

  echo "✓ 已生成：${DEFAULT_PGO}"
  echo "  下一步：${0} verify"
}

# ---------------------------------------------------------------------------
# verify：校验 default.pgo 与 PGO 是否生效
# ---------------------------------------------------------------------------
cmd_verify() {
  if [ ! -f "${DEFAULT_PGO}" ]; then
    echo "✗ 未找到 ${DEFAULT_PGO}，先执行 collect + merge"
    exit 1
  fi

  echo "=== 1) 校验 default.pgo 是有效 profile（应能列出热点函数）==="
  go tool pprof -top -nodecount=10 "${DEFAULT_PGO}"

  echo ""
  echo "=== 2) 校验 go build 是否真正把 PGO 传给编译器 ==="
  echo "    （在编译日志里查找 -pgoprofile 标志；有输出即说明 PGO 生效）"
  cd "${PROJECT_ROOT}"
  # -x 打印底层命令；-a 强制重编避免命中缓存看不到编译命令；只编 main 包即可。
  # 2>&1 合并 stderr；grep 命中即代表编译器收到了 PGO profile。
  if go build -a -x -tags="sonic" -o /dev/null . 2>&1 | grep -m1 "pgoprofile"; then
    echo "✓ PGO 已启用（编译命令包含 -pgoprofile）"
  else
    echo "⚠ 未在编译日志中发现 -pgoprofile。可能原因："
    echo "   - default.pgo 不在 main 包目录（应在项目根：${PROJECT_ROOT}）"
    echo "   - Go 版本过低（PGO 需 Go 1.21+，本项目 go.mod 为 1.26，正常满足）"
  fi
}

# ---------------------------------------------------------------------------
# clean：清理临时采集目录
# ---------------------------------------------------------------------------
cmd_clean() {
  if [ ! -d "${PGO_DIR}" ]; then
    echo "✓ ${PGO_DIR} 不存在，无需清理"
    return
  fi

  find "${PGO_DIR}" -type f -name '*.pprof' -delete
  echo "✓ 已清理 ${PGO_DIR} 下的临时 *.pprof；${DEFAULT_PGO} 未受影响"
}

# ---------------------------------------------------------------------------
# 入口
# ---------------------------------------------------------------------------
case "${1:-}" in
  collect) shift; cmd_collect "$@" ;;
  merge)   cmd_merge ;;
  verify)  cmd_verify ;;
  clean)   cmd_clean ;;
  *)
    echo "用法：$0 {collect <次数> <秒数> | merge | verify | clean}"
    echo "示例：$0 collect 3 30 && $0 merge && $0 verify"
    exit 1
    ;;
esac

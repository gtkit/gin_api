#!/usr/bin/env bash
# =============================================================================
# collect.sh —— 一键采集 pprof profile（CPU / heap / goroutine / allocs / block / mutex / trace）
#
# 用法：
#   ./collect.sh <类型> [秒数]
#
#   <类型>  必填，可选值：
#           cpu        采集 CPU profile（默认 30 秒，可被第 2 个参数覆盖）
#           heap       采集当前堆内存快照（存活对象）
#           allocs     采集累计内存分配（排查 GC 压力）
#           goroutine  采集 goroutine 全栈（排查泄漏，附带人类可读文本）
#           block      采集阻塞 profile（需服务端已设 BlockProfileRate>0）
#           mutex      采集锁竞争 profile（需服务端已设 MutexProfileFraction>0）
#           trace      采集执行跟踪（默认 5 秒，可被第 2 个参数覆盖）
#           all        采集 cpu+heap+goroutine+allocs+block+mutex（trace 因开销大不含在内）
#   [秒数]  可选，对 cpu / trace / all 生效，单位秒；必须是正整数
#
# 环境变量：
#   PPROF_ADDR  pprof 端点地址，默认 127.0.0.1:6060
#               若你的 pprof 挂在业务端口（例如 8008），执行前先：
#                 export PPROF_ADDR=127.0.0.1:8008
#
# 产物：带时间戳保存到 ../profiles/ 目录，便于归档与前后对比。
# =============================================================================

set -euo pipefail

# --- 解析脚本所在目录，定位产物输出目录（与脚本相对，跟在哪里执行无关）---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="${SCRIPT_DIR}/../profiles"

# --- 可配置项：pprof 端点地址，允许用环境变量覆盖 ---
PPROF_ADDR="${PPROF_ADDR:-127.0.0.1:6060}"
BASE_URL="http://${PPROF_ADDR}/debug/pprof"

# --- 时间戳，用于产物文件命名 ---
TS="$(date +%Y%m%d-%H%M%S)"

# --- 打印用法 ---
usage() {
  grep '^#' "$0" | sed 's/^# \{0,1\}//' | sed -n '2,30p'
  exit 1
}

# --- 下载单个 profile 的通用函数 ---
#   $1 = 端点路径（相对 /debug/pprof，如 heap、profile?seconds=30）
#   $2 = 输出文件名
fetch() {
  local path="$1"
  local out="$2"
  local max_time="${3:-120}"
  echo "▶ 采集 ${path}  ->  ${out}"
  # -f 失败时返回非 0；-S 显示错误；--max-time 给足时间（采集时长 + 余量）。
  # CPU/trace 是长连接采集，max_time 不能写死太短，否则 seconds 较大时会被 curl 掐断。
  if curl -fsS --max-time "${max_time}" -o "${out}" "${BASE_URL}/${path}"; then
    echo "  ✓ 完成：${out}"
  else
    echo "  ✗ 失败：无法访问 ${BASE_URL}/${path}"
    echo "    请确认：1) 服务已启动且 pprof.enabled=true  2) PPROF_ADDR 是否正确（当前=${PPROF_ADDR}）"
    return 1
  fi
}

# --- 秒数参数校验：避免把空字符串、负数、小数直接拼进 URL ---
validate_seconds() {
  local value="$1"
  local label="$2"

  if ! [[ "${value}" =~ ^[1-9][0-9]*$ ]]; then
    echo "✗ ${label} 必须是正整数秒，当前=${value}"
    exit 1
  fi
}

# --- all 模式允许 block/mutex 这类可选 profile 失败，但不能悄悄全失败 ---
try_fetch() {
  local path="$1"
  local out="$2"
  local max_time="${3:-120}"

  if fetch "${path}" "${out}" "${max_time}"; then
    return 0
  fi

  echo "  ⚠ 跳过：${path}"
  return 1
}

# --- 参数校验 ---
[ $# -lt 1 ] && usage
KIND="$1"
SECONDS_ARG="${2:-}"

mkdir -p "${OUT_DIR}"

echo "=== pprof 采集开始 | 目标=${PPROF_ADDR} | 输出=${OUT_DIR} ==="

case "${KIND}" in
  cpu)
    secs="${SECONDS_ARG:-30}"  # CPU 默认采 30 秒
    validate_seconds "${secs}" "cpu 秒数"
    fetch "profile?seconds=${secs}" "${OUT_DIR}/cpu-${TS}.pprof" "$((secs + 30))"
    echo "=== 采集结束。用以下命令分析（火焰图）==="
    echo "    ${SCRIPT_DIR}/analyze.sh ${OUT_DIR}/cpu-${TS}.pprof"
    ;;
  heap)
    fetch "heap" "${OUT_DIR}/heap-${TS}.pprof"
    echo "=== 采集结束。用以下命令分析（火焰图）==="
    echo "    ${SCRIPT_DIR}/analyze.sh ${OUT_DIR}/heap-${TS}.pprof"
    ;;
  allocs)
    fetch "allocs" "${OUT_DIR}/allocs-${TS}.pprof"
    echo "=== 采集结束。用以下命令分析（火焰图）==="
    echo "    ${SCRIPT_DIR}/analyze.sh ${OUT_DIR}/allocs-${TS}.pprof"
    ;;
  goroutine)
    # 同时下载二进制 profile（供 go tool pprof）和 debug=2 文本（直接可读）
    fetch "goroutine" "${OUT_DIR}/goroutine-${TS}.pprof"
    fetch "goroutine?debug=2" "${OUT_DIR}/goroutine-${TS}.txt"
    echo "=== 采集结束。查看命令：==="
    echo "    ${SCRIPT_DIR}/analyze.sh ${OUT_DIR}/goroutine-${TS}.pprof"
    echo "    less ${OUT_DIR}/goroutine-${TS}.txt"
    ;;
  block)
    fetch "block" "${OUT_DIR}/block-${TS}.pprof"
    echo "=== 采集结束。用以下命令分析（火焰图）==="
    echo "    ${SCRIPT_DIR}/analyze.sh ${OUT_DIR}/block-${TS}.pprof"
    ;;
  mutex)
    fetch "mutex" "${OUT_DIR}/mutex-${TS}.pprof"
    echo "=== 采集结束。用以下命令分析（火焰图）==="
    echo "    ${SCRIPT_DIR}/analyze.sh ${OUT_DIR}/mutex-${TS}.pprof"
    ;;
  trace)
    secs="${SECONDS_ARG:-5}"   # trace 默认采 5 秒（开销大，别采太久）
    validate_seconds "${secs}" "trace 秒数"
    fetch "trace?seconds=${secs}" "${OUT_DIR}/trace-${TS}.out" "$((secs + 30))"
    echo "=== 采集结束。查看命令：==="
    echo "  提示：用 go tool trace ${OUT_DIR}/trace-${TS}.out 查看"
    ;;
  all)
    cpu_secs="${SECONDS_ARG:-30}"
    validate_seconds "${cpu_secs}" "all 模式 CPU 秒数"
    failures=0
    try_fetch "profile?seconds=${cpu_secs}" "${OUT_DIR}/cpu-${TS}.pprof" "$((cpu_secs + 30))" || failures=$((failures + 1))
    try_fetch "heap" "${OUT_DIR}/heap-${TS}.pprof" || failures=$((failures + 1))
    try_fetch "goroutine" "${OUT_DIR}/goroutine-${TS}.pprof" || failures=$((failures + 1))
    try_fetch "goroutine?debug=2" "${OUT_DIR}/goroutine-${TS}.txt" || failures=$((failures + 1))
    try_fetch "allocs" "${OUT_DIR}/allocs-${TS}.pprof" || failures=$((failures + 1))
    try_fetch "block" "${OUT_DIR}/block-${TS}.pprof" || failures=$((failures + 1))
    try_fetch "mutex" "${OUT_DIR}/mutex-${TS}.pprof" || failures=$((failures + 1))
    if [ "${failures}" -ge 7 ]; then
      echo "✗ all 模式没有成功采集任何 profile，请检查服务与 PPROF_ADDR=${PPROF_ADDR}"
      exit 1
    fi
    echo "=== 采集结束。==="
    if [ -f "${OUT_DIR}/cpu-${TS}.pprof" ]; then
      echo "优先分析 CPU："
      echo "    ${SCRIPT_DIR}/analyze.sh ${OUT_DIR}/cpu-${TS}.pprof"
    else
      echo "CPU profile 未采集成功，请查看上方失败原因；可先分析已成功生成的 heap/allocs/goroutine 文件。"
    fi
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    echo "✗ 未知类型：${KIND}"
    usage
    ;;
esac

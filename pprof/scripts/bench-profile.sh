#!/usr/bin/env bash
# =============================================================================
# bench-profile.sh —— 用 Go benchmark 生成 CPU / 内存 profile，做离线剖析
#
# 适用场景：不依赖运行中的服务，对单个包 / 函数做精准性能剖析，
#           也可作为 PGO 之外固化性能、防止回归的手段。
#
# 用法：
#   ./bench-profile.sh <包路径> [bench正则]
#
# 示例：
#   ./bench-profile.sh ./internal/pkg/response                  # 跑该包全部 benchmark
#   ./bench-profile.sh ./internal/pkg/response BenchmarkRender  # 只跑名字匹配的 benchmark
#
# 产物：保存到 ../profiles/bench-<包名>-<时间戳>/ 下，包含 cpu.pprof / mem.pprof。
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# --- 参数 ---
PKG="${1:-./...}"          # 目标包，默认全部（不建议，benchmark 太多）
BENCH_RE="${2:-.}"          # benchmark 名称正则，默认全部
TS="$(date +%Y%m%d-%H%M%S)"

# --- 用包名生成可读的输出子目录名 ---
PKG_TAG="$(echo "${PKG}" | sed 's#[./]#_#g' | sed 's#^_*##')"
OUT_DIR="${SCRIPT_DIR}/../profiles/bench-${PKG_TAG}-${TS}"
mkdir -p "${OUT_DIR}"

echo "=== benchmark 剖析 | 包=${PKG} | 用例=${BENCH_RE} | 输出=${OUT_DIR} ==="

# 在项目根执行，确保模块路径解析正确。
cd "${PROJECT_ROOT}"

# -run=^$        关闭普通单元测试，只跑 benchmark
# -bench=<正则>  指定要跑的 benchmark
# -benchmem      报告每次操作的内存分配（B/op、allocs/op）
# -cpuprofile    输出 CPU profile
# -memprofile    输出内存 profile
# -tags=sonic    与本项目构建标签保持一致（项目用 sonic 加速 JSON）
go test -tags="sonic" \
  -run='^$' \
  -bench="${BENCH_RE}" \
  -benchmem \
  -cpuprofile="${OUT_DIR}/cpu.pprof" \
  -memprofile="${OUT_DIR}/mem.pprof" \
  "${PKG}" | tee "${OUT_DIR}/bench.txt"

echo ""
echo "=== 完成。分析命令：==="
echo "  go tool pprof -http=:8080 ${OUT_DIR}/cpu.pprof   # CPU 火焰图"
echo "  go tool pprof -http=:8080 ${OUT_DIR}/mem.pprof   # 内存火焰图"
echo "  原始结果：${OUT_DIR}/bench.txt"

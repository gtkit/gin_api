#!/usr/bin/env bash
# =============================================================================
# analyze.sh —— 启动 pprof Web UI（火焰图 / 调用图 / 源码标注）分析 profile
#
# 用法：
#   ./analyze.sh <profile文件路径 | 在线端点URL> [UI端口]
#
# 示例：
#   ./analyze.sh ../profiles/cpu-20260625-1530.pprof          # 分析本地文件
#   ./analyze.sh ../profiles/cpu-20260625-1530.pprof 8090     # 指定 UI 端口为 8090
#   ./analyze.sh http://127.0.0.1:6060/debug/pprof/heap       # 直接分析在线端点（自动采集）
#
# 说明：
#   - 本质是 go tool pprof -http=localhost:<端口> <目标>，浏览器会自动打开。
#   - Web UI 里的「Flame Graph（火焰图）」内置、无需 graphviz；
#     「Graph（调用图）」视图需要 graphviz（brew install graphviz）。
# =============================================================================

set -euo pipefail

# --- 参数校验 ---
if [ $# -lt 1 ]; then
  echo "用法：$0 <profile文件路径 | 在线端点URL> [UI端口]"
  echo "示例：$0 ../profiles/cpu-20260625-1530.pprof 8080"
  exit 1
fi

TARGET="$1"
UI_PORT="${2:-8080}"   # Web UI 端口，默认 8080

# --- 若目标看起来是本地文件路径（非 http 开头），校验其存在性，避免无意义启动 ---
case "${TARGET}" in
  http://*|https://*)
    echo "▶ 分析在线端点：${TARGET}"
    ;;
  *)
    if [ ! -f "${TARGET}" ]; then
      echo "✗ 找不到 profile 文件：${TARGET}"
      exit 1
    fi
    echo "▶ 分析本地文件：${TARGET}"
    ;;
esac

echo "▶ Web UI 将只监听本机 localhost:${UI_PORT}，浏览器会自动打开 http://localhost:${UI_PORT}"
echo "  （按 Ctrl+C 结束）"

# -http=localhost:端口 只绑定本机回环地址，避免把 pprof Web UI 暴露给局域网。
# 如确需远程访问，优先用 SSH 端口转发，不要直接监听 0.0.0.0。
exec go tool pprof -http="localhost:${UI_PORT}" "${TARGET}"

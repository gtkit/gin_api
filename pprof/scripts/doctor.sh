#!/usr/bin/env bash
# =============================================================================
# doctor.sh —— pprof / PGO 本地落地自检脚本
#
# 用途：
#   1. 检查当前机器是否具备 pprof 分析所需的基础命令；
#   2. 检查 pprof 端点是否已经接入并可访问；
#   3. 检查本目录脚本语法、可执行权限、default.pgo 状态；
#   4. 给出明确的下一步提示，避免直接跑 collect/pgo 时才发现环境缺口。
#
# 用法：
#   ./pprof/scripts/doctor.sh
#   PPROF_ADDR=127.0.0.1:16060 ./pprof/scripts/doctor.sh
#
# 环境变量：
#   PPROF_ADDR              pprof 端点地址，默认 127.0.0.1:6060
#   PPROF_DOCTOR_PROFILE    设为 1 时额外采集一份 3 秒 CPU profile 做深度验证
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
PPROF_ADDR="${PPROF_ADDR:-127.0.0.1:6060}"
BASE_URL="http://${PPROF_ADDR}/debug/pprof"
PROFILE_CHECK="${PPROF_DOCTOR_PROFILE:-0}"

FAILURES=0
WARNINGS=0

ok() {
  echo "✓ $*"
}

warn() {
  WARNINGS=$((WARNINGS + 1))
  echo "⚠ $*"
}

fail() {
  FAILURES=$((FAILURES + 1))
  echo "✗ $*"
}

need_cmd() {
  local name="$1"
  if command -v "${name}" >/dev/null 2>&1; then
    ok "命令可用：${name}"
  else
    fail "缺少必需命令：${name}"
  fi
}

optional_cmd() {
  local name="$1"
  local purpose="$2"
  if command -v "${name}" >/dev/null 2>&1; then
    ok "可选命令可用：${name}（${purpose}）"
  else
    warn "可选命令缺失：${name}（${purpose}）"
  fi
}

echo "=== pprof/PGO doctor | 项目=${PROJECT_ROOT} | 端点=${PPROF_ADDR} ==="

echo ""
echo "== 1. 基础命令 =="
need_cmd go
need_cmd curl
optional_cmd dot "Graph 视图需要 graphviz；Flame Graph 不需要"
optional_cmd hey "本地压测可用；也可用 wrk/ab 替代"
optional_cmd wrk "本地压测可用；也可用 hey/ab 替代"
optional_cmd benchstat "PGO/benchmark 前后对比更方便"

if command -v go >/dev/null 2>&1; then
  go version
fi

echo ""
echo "== 2. pprof 脚本状态 =="
for script in "${SCRIPT_DIR}"/*.sh; do
  [ -e "${script}" ] || continue

  if bash -n "${script}"; then
    ok "语法通过：${script}"
  else
    fail "语法失败：${script}"
  fi

  if [ -x "${script}" ]; then
    ok "可执行：${script}"
  else
    warn "不可执行：${script}；可运行 chmod +x ${script}"
  fi
done

echo ""
echo "== 3. pprof 端点连通性 =="
index_file="$(mktemp /tmp/pprof-doctor-index.XXXXXX)"
error_file="$(mktemp /tmp/pprof-doctor-error.XXXXXX)"
if curl -fsS --max-time 3 "${BASE_URL}/" >"${index_file}" 2>"${error_file}"; then
  if grep -qi "profile" "${index_file}"; then
    ok "端点可访问：${BASE_URL}/"
  else
    warn "端点返回了内容，但不像 pprof 索引页：${BASE_URL}/"
  fi
else
  warn "端点不可访问：${BASE_URL}/；当前项目源码尚未接入时这是预期现象，先按 01 文档第 3 节接入并启动服务"
fi
rm -f "${index_file}" "${error_file}"

if [ "${PROFILE_CHECK}" = "1" ]; then
  echo ""
  echo "== 4. CPU profile 深度验证 =="
  tmp_profile="$(mktemp /tmp/pprof-doctor-cpu.XXXXXX.pprof)"
  if curl -fsS --max-time 10 -o "${tmp_profile}" "${BASE_URL}/profile?seconds=3"; then
    if go tool pprof -top -nodecount=5 "${tmp_profile}" >/dev/null; then
      ok "3 秒 CPU profile 可采集且可解析：${tmp_profile}"
    else
      fail "CPU profile 已下载但 go tool pprof 无法解析：${tmp_profile}"
    fi
  else
    warn "3 秒 CPU profile 采集失败：${BASE_URL}/profile?seconds=3"
  fi
  rm -f "${tmp_profile}"
fi

echo ""
echo "== 5. PGO 状态 =="
DEFAULT_PGO="${PROJECT_ROOT}/default.pgo"
if [ -f "${DEFAULT_PGO}" ]; then
  if go tool pprof -top -nodecount=3 "${DEFAULT_PGO}" >/dev/null 2>&1; then
    ok "default.pgo 存在且可解析：${DEFAULT_PGO}"
  else
    fail "default.pgo 存在但不可解析：${DEFAULT_PGO}"
  fi
else
  warn "尚未生成 default.pgo；需要 PGO 时执行 ./pprof/scripts/pgo-merge.sh collect/merge/verify"
fi

echo ""
echo "== 6. 版本控制提示 =="
if [ -f "${PROJECT_ROOT}/.gitignore" ]; then
  if grep -Fxq '*.md' "${PROJECT_ROOT}/.gitignore" && ! grep -Fxq '!pprof/**/*.md' "${PROJECT_ROOT}/.gitignore"; then
    warn "根 .gitignore 忽略了 *.md，且未放行 pprof/**/*.md；这些文档默认不会被 git add 跟踪"
  else
    ok ".gitignore 未阻止 pprof Markdown 文档跟踪，或已设置例外"
  fi
fi

echo ""
echo "=== doctor 完成：${FAILURES} 个错误，${WARNINGS} 个警告 ==="
if [ "${FAILURES}" -gt 0 ]; then
  exit 1
fi

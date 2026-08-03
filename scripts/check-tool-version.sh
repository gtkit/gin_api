#!/usr/bin/env bash
# check-tool-version.sh —— 工具实际版本必须与集中声明一致。
#
# 声明了却不执行等于没声明：工具版本漂移会让同一份代码在不同机器上得到不同的
# 检查结果，而那种差异最先表现为"我这里是好的"——最难查的一类问题。
#
# 用法：check-tool-version.sh <tool> <declared-version> [--self-test]
set -euo pipefail

# 注意：错误信息里的变量一律写成 ${var}。
# 全角括号等字符不是 bash 的分隔符，`$declared）` 会被解析成变量名 `declared）`，
# 于是在 `set -u` 下报 unbound variable——**只在错误分支才触发**，
# 正常路径完全看不出来。
#
# extract_version 从工具输出里取出 x.y.z。
#
# 各工具的 version 子命令输出格式不一，统一按语义化版本模式提取，
# 避免为每个工具写一套解析。
extract_version() {
  grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1
}

run_check() {
  local tool="$1" declared="$2" actual=""

  if [ -z "$declared" ]; then
    echo "FAIL: ${tool} 没有声明版本——请在 security/toolchain.mk 中声明" >&2
    exit 1
  fi
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: ${tool} 未安装（声明版本 ${declared}）" >&2
    exit 1
  fi

  actual="$("$tool" version 2>/dev/null | extract_version || true)"
  [ -n "$actual" ] || actual="$("$tool" --version 2>/dev/null | extract_version || true)"

  if [ -z "$actual" ]; then
    echo "FAIL: 无法取得 ${tool} 的版本（声明版本 ${declared}）" >&2
    exit 1
  fi
  if [ "$actual" != "$declared" ]; then
    echo "FAIL: ${tool} 版本漂移——声明 ${declared}，实际 ${actual}" >&2
    echo "      安装声明版本后重试，否则本机的检查结果与 CI 不可比。" >&2
    exit 1
  fi
}

run_self_test() {
  local failed=0
  expect() {
    if [ "$2" = "$3" ]; then echo "[self-test] OK: $1"
    else echo "[self-test] FAIL: $1 => got '$2', want '$3'"; failed=1; fi
  }

  expect "a golangci-lint style banner yields its version" \
    "$(printf 'golangci-lint has version 2.11.4 built with go1.26\n' | extract_version)" "2.11.4"
  expect "a bare version line is parsed" \
    "$(printf 'v1.2.3\n' | extract_version)" "1.2.3"
  expect "the first version wins when several appear" \
    "$(printf 'tool 2.11.4 (go1.26.0)\n' | extract_version)" "2.11.4"
  expect "output without a version yields nothing" \
    "$(printf 'no version here\n' | extract_version)" ""

  [ "$failed" -eq 0 ] || exit 1
  echo "[self-test] OK: version extraction handles the common banner shapes"
}

case "${3:-}" in
  --self-test) run_self_test ;;
  *)           run_check "${1:-}" "${2:-}" ;;
esac

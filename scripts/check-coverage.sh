#!/usr/bin/env sh
# check-coverage.sh — 覆盖率门禁：校验 coverage profile 的总覆盖率不低于给定下限。
#
# 用法：check-coverage.sh <coverage-profile> <minimum-percent>
# 行为：用 go tool cover 读出 profile 的 total 覆盖率，与 minimum 比较；
#   低于下限、profile 缺失/为空、或 minimum 非数值时以非零码退出。供 make check-coverage 调用。
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <coverage-profile> <minimum-percent>" >&2
  exit 2
fi

profile="$1"
minimum="$2"

if [ ! -s "$profile" ]; then
  echo "coverage profile is missing or empty: $profile" >&2
  exit 1
fi

actual="$(go tool cover -func="$profile" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')"
if [ -z "$actual" ]; then
  echo "unable to read total coverage from: $profile" >&2
  exit 1
fi

if ! awk -v value="$minimum" 'BEGIN { exit !(value ~ /^[0-9]+([.][0-9]+)?$/) }'; then
  echo "minimum coverage must be numeric: $minimum" >&2
  exit 2
fi

if ! awk -v actual="$actual" -v minimum="$minimum" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
  echo "coverage check failed: ${actual}% < ${minimum}%" >&2
  exit 1
fi

echo "coverage check OK: ${actual}% >= ${minimum}%"

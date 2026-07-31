#!/usr/bin/env sh
# test-coverage.sh — 跑 race 测试并生成聚合 coverage profile（与 check-coverage.sh 配套）。
#
# 用法：test-coverage.sh <coverage-profile> [go-package ...]（缺省包为 ./...）
# 行为：默认对目标包整体跑 go test -race -covermode=atomic 输出到 profile。
#   受限沙箱下（ALLOW_NO_LOOPBACK=1，禁止绑定回环端口）：先跑其余包，再对依赖本地 listener 的
#   internal/middleware 只跑显式列出的沙箱安全用例，
#   最后把各段 profile 合并成一个 atomic profile。正常开发与 CI 不设该变量，始终跑完整测试。
set -eu

if [ "$#" -lt 1 ]; then
  echo "usage: $0 <coverage-profile> [go-package ...]" >&2
  exit 2
fi

profile="$1"
shift
if [ "$#" -eq 0 ]; then
  set -- ./...
fi

mkdir -p "$(dirname "$profile")"

if [ "${ALLOW_NO_LOOPBACK:-0}" != "1" ]; then
  go test -race -count=1 -timeout=5m -covermode=atomic -coverprofile="$profile" "$@"
  exit 0
fi

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/go-service-coverage.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT INT TERM

all_packages="$tmpdir/all-packages.txt"
regular_packages="$tmpdir/regular-packages.txt"
go list "$@" > "$all_packages"

while IFS= read -r package; do
  case "$package" in
    */internal/middleware)
      ;;
    *)
      printf '%s\n' "$package" >> "$regular_packages"
      ;;
  esac
done < "$all_packages"

parts=""
if [ -s "$regular_packages" ]; then
  regular_profile="$tmpdir/regular.out"
  xargs go test -race -count=1 -timeout=5m -covermode=atomic -coverprofile="$regular_profile" < "$regular_packages"
  parts="$regular_profile"
fi

run_selected_package() {
  package="$1"
  exclude_pattern="$2"
  output="$3"
  tests="$(go test -list '^Test' "$package" | grep '^Test' | grep -Ev "$exclude_pattern" | paste -sd '|' -)"
  if [ -z "$tests" ]; then
    echo "no sandbox-safe tests found for $package" >&2
    exit 1
  fi

  go test -race -count=1 -timeout=5m -covermode=atomic -coverprofile="$output" -run "^(${tests})$" "$package"
  parts="$parts $output"
}

run_selected_package ./internal/middleware \
  '^TestFallbackWithGincache_(NormalFlow|RedisDownDBDown|GincacheHitBypassesFallback)$' \
  "$tmpdir/middleware.out"

printf '%s\n' 'mode: atomic' > "$profile"
for part in $parts; do
  sed '1{/^mode: /d;}' "$part" >> "$profile"
done

echo "coverage profile created with restricted loopback exclusions: $profile"

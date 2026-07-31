#!/usr/bin/env bash
# check-architecture.sh — enforce the project's documented layering rules.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# 模块路径从 go.mod 读取，绝不硬编码。
#
# 原因是派生项目：gonew 只重写 .go 文件里的 **import 路径与根包名**（parser.ImportsOnly），
# 不碰 shell 脚本。硬编码母版路径的正则在派生项目里永远匹配不到任何东西，
# 传感器会静默全通过——那比没有传感器更糟，因为它给出的是虚假的合规保证。
MODULE_PATH="$(sed -n -E 's/^module[[:space:]]+([^[:space:]]+).*$/\1/p' go.mod | head -n 1)"
if [ -z "$MODULE_PATH" ]; then
  echo "FAIL: cannot read the module path from go.mod" >&2
  exit 1
fi
# 先限定字符集再转义，而不是试图通用地转义任意字符：
# 合法 module path 只可能含字母、数字与 . / _ - ~，其中唯一的 ERE 元字符是 `.`。
# 限定字符集后只需转义 `.`，既避免了跨平台 sed 字符类差异，也杜绝了把元字符
# 意外带进正则。出现字符集之外的内容说明 go.mod 有异常，直接失败而不是猜。
if ! printf '%s' "$MODULE_PATH" | grep -qE '^[A-Za-z0-9._~/-]+$'; then
  echo "FAIL: unexpected characters in module path: $MODULE_PATH" >&2
  exit 1
fi
MOD="$(printf '%s' "$MODULE_PATH" | sed 's/\./\\./g')"

fail=0

check_no_match() {
  local label="$1"
  local pattern="$2"
  shift 2

  local tmp
  tmp="$(mktemp)"

  for path in "$@"; do
    [ -d "$path" ] || continue
    find "$path" -type f -name '*.go' ! -name '*_test.go' -print0 |
      xargs -0 grep -nE "$pattern" >>"$tmp" 2>/dev/null || true
  done

  if [ -s "$tmp" ]; then
    echo "FAIL: $label"
    cat "$tmp"
    fail=1
  fi

  rm -f "$tmp"
}

check_no_match \
  "application imports gin / gorm / repository" \
  "gorm\.io/gorm|${MOD}/internal/repository/|github\.com/gin-gonic/gin" \
  internal/module/*/application

check_no_match \
  "transport/http imports gorm / repository" \
  "${MOD}/internal/repository/|gorm\.io/gorm" \
  internal/module/*/transport/http

check_no_match \
  "repository imports gin / module" \
  "github\.com/gin-gonic/gin|${MOD}/internal/module/" \
  internal/repository

check_no_match \
  "internal/pkg imports module" \
  "${MOD}/internal/module/" \
  internal/pkg

check_no_match \
  "worker imports gorm / repository" \
  "${MOD}/internal/repository/|gorm\.io/gorm" \
  internal/module/*/worker

check_no_match \
  "application / dto / models import ormx (GORM infrastructure)" \
  'github\.com/gtkit/ormx' \
  internal/module/*/application internal/module/*/dto internal/models

check_no_match \
  "application / dto / models import logger (use port injection)" \
  'github\.com/gtkit/logger' \
  internal/module/*/application internal/module/*/dto internal/models

while IFS= read -r -d '' file; do
  if grep -qE 'func .*mdbCtx' "$file" && ! grep -q 'dbtx.DB(ctx, r.db)' "$file"; then
    echo "FAIL: $file mdbCtx does not call dbtx.DB(ctx, r.db)"
    fail=1
  fi
done < <(find internal/repository -type f -name '*.go' ! -name '*_test.go' -print0)

if [ "$fail" -ne 0 ]; then
  exit 1
fi

echo "architecture check OK"

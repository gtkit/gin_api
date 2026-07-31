#!/usr/bin/env bash
# check-architecture.sh — enforce the project's documented layering rules.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

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
  'gorm\.io/gorm|github\.com/gtkit/gin-api/internal/repository/|github\.com/gin-gonic/gin' \
  internal/module/*/application

check_no_match \
  "transport/http imports gorm / repository" \
  'github\.com/gtkit/gin-api/internal/repository/|gorm\.io/gorm' \
  internal/module/*/transport/http

check_no_match \
  "repository imports gin / module" \
  'github\.com/gin-gonic/gin|github\.com/gtkit/gin-api/internal/module/' \
  internal/repository

check_no_match \
  "internal/pkg imports module" \
  'github\.com/gtkit/gin-api/internal/module/' \
  internal/pkg

check_no_match \
  "worker imports gorm / repository" \
  'github\.com/gtkit/gin-api/internal/repository/|gorm\.io/gorm' \
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

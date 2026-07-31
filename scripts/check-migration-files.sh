#!/usr/bin/env bash
# check-migration-files.sh —— 迁移文件完整性的**离线**断言（不连库）。
#
# 用途有二：本地 `make check-migration-files`，以及镜像门禁的第一步
# （在容器内对 /app/database/migrations 执行，证明迁移文件确实随镜像投递）。
#
# 为什么必须是文件级而不是跑 `migrate status`：
#   migratex 的命令集在 db == nil 时直接返回错误，而所有非 help 子命令都会先建立
#   数据库连接——包括 status/pretend/lint 这些"读类"命令。
#   migratex 文档说读类命令"只读、不取锁"指的是**不写**，不是**不连**。
#   构建门禁的机器上通常没有数据库，靠 `migrate status` 的门禁会因连不上库而失败，
#   看起来却像是迁移文件有问题。
#
# 只依赖 POSIX 工具，能在最小化的运行镜像里执行。
set -eu

DIR="${1:-database/migrations}"

fail() { echo "[check-migration-files] FAIL: $*" >&2; exit 1; }

[ -d "$DIR" ] || fail "迁移目录不存在：$DIR"

count=0
for f in "$DIR"/*.sql; do
  # glob 无匹配时 shell 会原样保留模式串，用存在性判断挡掉。
  [ -e "$f" ] || fail "迁移目录中没有任何 .sql 文件：$DIR"
  count=$((count + 1))

  base="$(basename "$f")"
  # goose 命名：<纯数字版本>_<名称>.sql
  case "$base" in
    *[!0-9]*_*.sql | *_*.sql) ;;
    *) fail "文件名不符合 <版本>_<名称>.sql：$base" ;;
  esac
  version="${base%%_*}"
  case "$version" in
    '' | *[!0-9]*) fail "文件名的版本段必须是纯数字：$base" ;;
  esac

  grep -q -- '-- +goose Up' "$f" || fail "缺少 '-- +goose Up' 区段：$base"
  grep -q -- '-- +goose Down' "$f" || fail "缺少 '-- +goose Down' 区段：$base"
done

[ "$count" -gt 0 ] || fail "迁移目录为空：$DIR"

echo "check-migration-files OK: ${count} 个迁移文件，命名与区段均合规（${DIR}）"

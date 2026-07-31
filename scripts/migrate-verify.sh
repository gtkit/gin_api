#!/usr/bin/env bash
# migrate-verify.sh —— 迁移的连库级验证：空库往返 + 实际 schema 比对。
#
# 两件事都必须做，缺一不可：
#   * 往返（up → down → up）只证明 SQL 可执行且可逆；
#   * 它**证明不了** schema 与模型一致——列默认值、collation、索引属性、外键
#     全部不在往返的检查范围内，repository 测试同样覆盖不到。
#   因此额外把实际 information_schema 与提交的快照逐行比对。
#
# 依赖自带：本脚本自行拉起固定版本 MySQL 容器并在 trap 中销毁，
# 不依赖任何开发编排（那属于更后面的批次，门禁不能反向依赖后续批次）。
# 也支持接收外部测试库：设 MIGRATE_VERIFY_DSN_* 变量即可跳过容器管理。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# 固定版本：它同时兼作"最低支持版本"的实证——门禁跑通即证明该版本可用。
MYSQL_IMAGE="${MYSQL_IMAGE:-mysql:8.0}"
CONTAINER="${MIGRATE_VERIFY_CONTAINER:-gin-api-migrate-verify}"
DB_NAME="${MIGRATE_VERIFY_DB:-migrate_verify}"
DB_USER="${MIGRATE_VERIFY_USER:-root}"
DB_PASS="${MIGRATE_VERIFY_PASSWORD:-verifyroot}"
DB_HOST="${MIGRATE_VERIFY_HOST:-127.0.0.1}"
DB_PORT="${MIGRATE_VERIFY_PORT:-13307}"
SNAPSHOT="database/schema/baseline.snapshot"

USE_EXTERNAL_DB="${MIGRATE_VERIFY_EXTERNAL:-0}"

workdir=""
cleanup() {
  local rc=$?
  if [ "$USE_EXTERNAL_DB" != "1" ]; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  fi
  [ -n "$workdir" ] && rm -rf "$workdir"
  exit "$rc"
}
trap cleanup EXIT INT TERM

mysql_exec() {
  if [ "$USE_EXTERNAL_DB" = "1" ]; then
    mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" -N -B "$@"
  else
    docker exec "$CONTAINER" mysql -u "$DB_USER" -p"$DB_PASS" -N -B "$@" 2>/dev/null
  fi
}

start_mysql() {
  echo "[migrate-verify] 启动 ${MYSQL_IMAGE}（容器 ${CONTAINER}，端口 ${DB_PORT}）"
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  docker run -d --name "$CONTAINER" \
    -e MYSQL_ROOT_PASSWORD="$DB_PASS" -e MYSQL_DATABASE="$DB_NAME" \
    -p "${DB_PORT}:3306" "$MYSQL_IMAGE" \
    --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci >/dev/null

  # 必须等"真正就绪"，不能只 ping：MySQL 镜像初始化期会先起一个临时服务器，
  # 那时 mysqladmin ping 就已经成功——那是假就绪信号，随后临时服务器会被关掉。
  echo "[migrate-verify] 等待数据库就绪"
  for _ in $(seq 1 120); do
    if docker logs "$CONTAINER" 2>&1 | grep -q "ready for connections. Version.*port: 3306"; then
      return 0
    fi
    sleep 1
  done
  echo "[migrate-verify] FAIL: 数据库在预期时间内未就绪" >&2
  docker logs "$CONTAINER" 2>&1 | tail -20 >&2
  return 1
}

# write_config 生成一份指向测试库的临时配置，放在项目根之外。
write_config() {
  workdir="$(mktemp -d)"
  mkdir -p "$workdir/config/env" "$workdir/config/pem" "$workdir/logs"
  python3 - "$workdir/config/env/env.yml" "$DB_HOST" "$DB_PORT" "$DB_NAME" "$DB_USER" "$DB_PASS" <<'PY'
import pathlib, re, sys
out, host, port, name, user, password = sys.argv[1:7]
src = pathlib.Path("config/env.yml.example").read_text(encoding="utf-8")
def setkv(block, key, value):
    global src
    pattern = re.compile(rf'(\n{block}:\n(?:[ \t]+.*\n|\n)*?[ \t]+{key}:)[^\n]*')
    src = pattern.sub(lambda m: m.group(1) + " " + value, src, count=1)
setkv("database", "host", host)
setkv("database", "port", f'"{port}"')
setkv("database", "name", name)
setkv("database", "username", user)
setkv("database", "password", f'"{password}"')
pathlib.Path(out).write_text(src, encoding="utf-8")
PY
}

run_migrate() {
  APP_ROOT="$workdir" GOWORK=off go run . "$@"
}

dump_schema() {
  {
    mysql_exec -e "SELECT CONCAT_WS('|','TABLE',TABLE_NAME,ENGINE,TABLE_COLLATION)
      FROM information_schema.TABLES
      WHERE TABLE_SCHEMA='$DB_NAME' AND TABLE_NAME<>'goose_db_version' ORDER BY TABLE_NAME;"
    mysql_exec -e "SELECT CONCAT_WS('|','COLUMN',TABLE_NAME,ORDINAL_POSITION,COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,
      IFNULL(COLUMN_DEFAULT,'<null>'),EXTRA,IFNULL(COLLATION_NAME,'-'))
      FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA='$DB_NAME' AND TABLE_NAME<>'goose_db_version'
      ORDER BY TABLE_NAME, ORDINAL_POSITION;"
    mysql_exec -e "SELECT CONCAT_WS('|','INDEX',TABLE_NAME,INDEX_NAME,SEQ_IN_INDEX,COLUMN_NAME,NON_UNIQUE,IFNULL(SUB_PART,'-'))
      FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA='$DB_NAME' AND TABLE_NAME<>'goose_db_version'
      ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX;"
    mysql_exec -e "SELECT CONCAT_WS('|','FK',TABLE_NAME,CONSTRAINT_NAME,COLUMN_NAME,REFERENCED_TABLE_NAME,REFERENCED_COLUMN_NAME)
      FROM information_schema.KEY_COLUMN_USAGE
      WHERE TABLE_SCHEMA='$DB_NAME' AND REFERENCED_TABLE_NAME IS NOT NULL
      ORDER BY TABLE_NAME, CONSTRAINT_NAME;"
  }
}

[ "$USE_EXTERNAL_DB" = "1" ] || start_mysql
write_config

echo "[migrate-verify] lint"
run_migrate migrate lint >/dev/null

echo "[migrate-verify] 空库 up"
run_migrate migrate up >/dev/null
dump_schema > "$workdir/after-first-up.snapshot"

echo "[migrate-verify] 比对实际 schema 与提交的快照"
if ! diff -u "$SNAPSHOT" "$workdir/after-first-up.snapshot"; then
  echo "[migrate-verify] FAIL: 实际 schema 与 $SNAPSHOT 不一致" >&2
  echo "  基线迁移改动后需同步快照：见 database/README.md" >&2
  exit 1
fi

echo "[migrate-verify] down"
run_migrate migrate down --force >/dev/null
remaining="$(mysql_exec -e "SELECT COUNT(*) FROM information_schema.TABLES
  WHERE TABLE_SCHEMA='$DB_NAME' AND TABLE_NAME<>'goose_db_version';")"
if [ "$(echo "$remaining" | tr -d '[:space:]')" != "0" ]; then
  echo "[migrate-verify] FAIL: down 之后仍残留 $remaining 张受管表" >&2
  exit 1
fi

echo "[migrate-verify] 再次 up"
run_migrate migrate up >/dev/null
dump_schema > "$workdir/after-second-up.snapshot"

# 往返必须收敛到同一 schema：不一致说明 down 没有真正还原，
# 后续迁移会在一个与预期不同的基线上执行。
if ! diff -u "$workdir/after-first-up.snapshot" "$workdir/after-second-up.snapshot"; then
  echo "[migrate-verify] FAIL: 往返后的 schema 与首次迁移结果不一致" >&2
  exit 1
fi

echo "[migrate-verify] 重复 up 应幂等"
run_migrate migrate up >/dev/null

echo "migrate-verify OK"

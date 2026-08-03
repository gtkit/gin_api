#!/usr/bin/env bash
# check-image-digests.sh —— 所有镜像引用必须钉不可变摘要。
#
# 为什么需要它：只改一次而不装防线，下一次升级基础镜像时摘要就会被丢掉——
# 而那种丢失在 code review 里看起来只是一行版本号变更，几乎不可能被注意到。
#
# 覆盖：Dockerfile 的 FROM、compose 的 image、脚本与 Makefile 里的镜像常量。
#
# **能力边界**：它检查的是"引用形态里有没有 @sha256:"，不验证摘要是否真实存在、
# 也不验证它是否指向声称的那个 tag。伪造一个格式合法但不存在的摘要能骗过它，
# 但那会在第一次 pull 时立即失败——而漏掉摘要是静默的，两者的危险程度不同。
#
# 用法：
#   check-image-digests.sh              检查仓库
#   check-image-digests.sh --self-test  自检：证明规则会触发也不会误报
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# DIGEST_RE 是不可变摘要的形态：64 位十六进制。
DIGEST_RE='@sha256:[0-9a-f]{64}'

# scan_dockerfile 检查每一个 FROM。
#
# 跳过 `FROM <stage> AS` 形式的阶段引用（如 `FROM builder`）：那不是外部镜像，
# 要求它带摘要毫无意义，而无意义的报错会让人给整条规则加豁免。
scan_dockerfile() {
  local file="$1" line no=0 image
  while IFS= read -r line; do
    no=$((no + 1))
    case "$line" in
      FROM\ *|from\ *) ;;
      *) continue ;;
    esac

    image="$(printf '%s\n' "$line" | awk '{print $2}')"
    # 阶段引用不含斜杠、冒号与点，且必然在本文件先被定义过。
    case "$image" in
      *:*|*/*|*.*) ;;
      *) continue ;;
    esac

    if ! printf '%s' "$image" | grep -qE "$DIGEST_RE"; then
      printf '%s:%d FROM %s has no immutable digest\n' "$file" "$no" "$image"
    fi
  done < "$file"
}

# scan_yaml 检查 compose 的 image 字段。
scan_yaml() {
  local file="$1" line no=0 image
  while IFS= read -r line; do
    no=$((no + 1))
    case "$line" in
      *image:*) ;;
      *) continue ;;
    esac
    # 注释行不算。
    case "${line#"${line%%[![:space:]]*}"}" in
      \#*) continue ;;
    esac

    image="$(printf '%s\n' "$line" | sed -E 's/.*image:[[:space:]]*//; s/[[:space:]]*$//; s/^["'"'"']//; s/["'"'"']$//')"
    [ -n "$image" ] || continue

    if ! printf '%s' "$image" | grep -qE "$DIGEST_RE"; then
      printf '%s:%d image %s has no immutable digest\n' "$file" "$no" "$image"
    fi
  done < "$file"
}

# scan_make_vars 检查 toolchain.mk 里声明的镜像常量。
#
# 集中声明处漏掉摘要，等于所有引用它的地方一起漏掉。
scan_make_vars() {
  local file="$1" line no=0 value
  while IFS= read -r line; do
    no=$((no + 1))
    case "$line" in
      *_IMAGE[[:space:]]*=*|*_IMAGE=*) ;;
      *) continue ;;
    esac
    case "${line#"${line%%[![:space:]]*}"}" in
      \#*) continue ;;
    esac

    value="$(printf '%s\n' "$line" | sed -E 's/^[^=]*=[[:space:]]*//; s/[[:space:]]*$//')"
    [ -n "$value" ] || continue

    # 命令替换与变量引用不是字面镜像——它们是**读取**集中声明的写法，
    # 要求它们"带摘要"是误报，而误报会让整条规则被加豁免绕过。
    case "$value" in
      *'$('*|*'${'*|*'`'*) continue ;;
    esac

    if ! printf '%s' "$value" | grep -qE "$DIGEST_RE"; then
      printf '%s:%d %s has no immutable digest\n' "$file" "$no" "$value"
    fi
  done < "$file"
}

# declared_image 从集中声明处取出某个镜像。
declared_image() {
  sed -n -E "s/^$1[[:space:]]*=[[:space:]]*(.+)$/\\1/p" security/toolchain.mk
}

# check_dockerfile_matches_declaration 比对 Dockerfile 与集中声明。
#
# 光有"都带摘要"不够：两处可以各带一个**不同的**摘要，于是构建用的镜像与
# 门禁声明的镜像不是同一个，而两边看起来都合规。
check_dockerfile_matches_declaration() {
  [ -f Dockerfile ] || return 0

  local var declared
  for var in GOLANG_IMAGE ALPINE_IMAGE; do
    declared="$(declared_image "$var")"
    if [ -z "$declared" ]; then
      printf 'security/toolchain.mk is missing %s\n' "$var"
      continue
    fi
    if ! grep -qF "$declared" Dockerfile; then
      printf 'Dockerfile does not use the declared %s (%s)\n' "$var" "$declared"
    fi
  done
}

run_check() {
  cd "$ROOT_DIR"

  local findings=""
  [ -f Dockerfile ] && findings+="$(scan_dockerfile Dockerfile)"$'\n'
  [ -f compose.yaml ] && findings+="$(scan_yaml compose.yaml)"$'\n'
  [ -f security/toolchain.mk ] && findings+="$(scan_make_vars security/toolchain.mk)"$'\n'

  # Dockerfile 的摘要必须与集中声明一致：两处各写各的必然漂移，
  # 而漂移的表现是"构建用的镜像与门禁扫描的镜像不是同一个"。
  findings+="$(check_dockerfile_matches_declaration)"$'\n'

  findings="$(printf '%s' "$findings" | grep -v '^$' || true)"

  if [ -z "$findings" ]; then
    echo "image digest check OK"
    return 0
  fi

  echo "FAIL: these image references are mutable tags:" >&2
  printf '%s\n' "$findings" | sed 's/^/  /' >&2
  echo "" >&2
  echo "Pin them with: docker buildx imagetools inspect <tag>  (take the top-level Digest)" >&2
  echo "A moving tag makes 'passed yesterday, failed today' impossible to reproduce." >&2
  return 1
}

# run_self_test 证明规则**真的会触发**，也**真的不会误报**。
run_self_test() {
  local sandbox
  sandbox="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$sandbox'" EXIT

  local failed=0
  expect() {
    local name="$1" got="$2" want="$3"
    if [ "$got" = "$want" ]; then
      echo "[self-test] OK: $name"
    else
      echo "[self-test] FAIL: $name => got '$got', want '$want'"
      failed=1
    fi
  }

  local pinned='@sha256:0000000000000000000000000000000000000000000000000000000000000000'

  printf 'FROM golang:1.26-alpine\n' > "$sandbox/Dockerfile.bad"
  expect "a floating FROM is caught" \
    "$(scan_dockerfile "$sandbox/Dockerfile.bad" | wc -l | tr -d ' ')" "1"

  printf 'FROM golang:1.26-alpine%s AS builder\nFROM builder\n' "$pinned" > "$sandbox/Dockerfile.ok"
  expect "a pinned FROM and a stage reference are not flagged" \
    "$(scan_dockerfile "$sandbox/Dockerfile.ok" | wc -l | tr -d ' ')" "0"

  printf '    image: mysql:8.0\n' > "$sandbox/bad.yaml"
  expect "a floating compose image is caught" \
    "$(scan_yaml "$sandbox/bad.yaml" | wc -l | tr -d ' ')" "1"

  printf '    image: mysql:8.0%s\n    # image: redis:7\n' "$pinned" > "$sandbox/ok.yaml"
  expect "a pinned image and a commented one are not flagged" \
    "$(scan_yaml "$sandbox/ok.yaml" | wc -l | tr -d ' ')" "0"

  printf 'TRIVY_IMAGE = aquasec/trivy:0.50\n' > "$sandbox/bad.mk"
  expect "a floating image variable is caught" \
    "$(scan_make_vars "$sandbox/bad.mk" | wc -l | tr -d ' ')" "1"

  printf 'TRIVY_IMAGE = aquasec/trivy%s\n# OLD_IMAGE = x:1\n' "$pinned" > "$sandbox/ok.mk"
  expect "a pinned variable and a commented one are not flagged" \
    "$(scan_make_vars "$sandbox/ok.mk" | wc -l | tr -d ' ')" "0"

  # 命令替换不是字面镜像：脚本从集中声明处读取时不该被要求"带摘要"。
  # 这条曾经是真误报，而误报会让整条规则被加豁免绕过。
  printf 'GITLEAKS_IMAGE="$(sed -n s/x/y/p security/toolchain.mk)"\n' > "$sandbox/read.mk"
  expect "reading from the declaration is not flagged" \
    "$(scan_make_vars "$sandbox/read.mk" | wc -l | tr -d ' ')" "0"

  [ "$failed" -eq 0 ] || exit 1
  echo "[self-test] OK: the rule fires on floating tags and stays quiet on pinned ones"
}

case "${1:-}" in
  --self-test) run_self_test ;;
  "")          run_check ;;
  *)           echo "用法: $(basename "$0") [--self-test]" >&2; exit 2 ;;
esac

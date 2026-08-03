#!/usr/bin/env bash
# security-gate.sh —— 统一安全门禁：源码漏洞 + 密钥扫描（+ 发布期的镜像扫描）。
#
# 两个入口，刻意分层：
#   security-source   日常/提交前：当前发布候选快照的漏洞与密钥扫描
#   security-release   打 tag 之前：追加全历史密钥扫描、镜像构建与镜像漏洞扫描、SBOM
#
# **不塞进 `make check`**：`make check` 的契约是"架构传感器 + lint + vet + race/coverage"，
# 且要求受限环境可复现。安全扫描依赖网络（漏洞库）与 Docker（扫描器镜像），
# 塞进去会让 `make check` 在离线或无 Docker 的环境下失败——那会逼人绕过整个门禁。
#
# 一个贯穿全文的原则：**扫描器执行/解析失败 ≠ 没有漏洞**。前者永远 fail-closed
# 且不可豁免；只有真正的 finding 才走 waiver 匹配。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

CACHE_DIR=".cache/security"
MODE="${1:-source}"

# 扫描器版本与 digest 的单一来源。
GITLEAKS_IMAGE="$(sed -n -E 's/^GITLEAKS_IMAGE[[:space:]]*=[[:space:]]*(.+)$/\1/p' security/toolchain.mk)"
TRIVY_IMAGE="$(sed -n -E 's/^TRIVY_IMAGE[[:space:]]*=[[:space:]]*(.+)$/\1/p' security/toolchain.mk)"

fail() { echo "[security-gate] FAIL: $*" >&2; exit 1; }
info() { echo "[security-gate] $*"; }

[ -n "$GITLEAKS_IMAGE" ] || fail "security/toolchain.mk 缺少 GITLEAKS_IMAGE"
[ -n "$TRIVY_IMAGE" ] || fail "security/toolchain.mk 缺少 TRIVY_IMAGE"
case "$GITLEAKS_IMAGE$TRIVY_IMAGE" in
  *:latest* | *@sha256:*) ;;
esac
# 浮动 tag 会让"昨天通过、今天失败"无从复现，也无法回答本次到底跑了什么。
printf '%s\n%s\n' "$GITLEAKS_IMAGE" "$TRIVY_IMAGE" | grep -qv '@sha256:' &&
  fail "扫描器镜像必须钉 digest（当前：$GITLEAKS_IMAGE / $TRIVY_IMAGE）"

workdir=""
cleanup() {
  local rc=$?
  [ -n "$workdir" ] && rm -rf "$workdir"
  exit "$rc"
}
trap cleanup EXIT INT TERM

mkdir -p "$CACHE_DIR"

# ---------------------------------------------------------------------------
# 发布候选快照
# ---------------------------------------------------------------------------
# 扫描对象**不是**仓库目录本身，而是导出到项目根之外的发布候选快照。
#
# 直接 `gitleaks dir .` 会把本地 ignored 的真实 .env / pem 当成发布 finding——
# 那是每次都报、且永远无法归零的假阳性，最终会让人整体关掉密钥扫描。
# 而只扫 HEAD 又会漏掉"已跟踪但已修改"与"新增未跟踪"的候选内容。
#
# 正确集合 = git ls-files --cached --others --exclude-standard 对应的当前工作树内容。
export_release_candidate() {
  workdir="$(mktemp -d "$(dirname "$ROOT_DIR")/.security-scan-XXXXXX")"
  chmod 0700 "$workdir"
  git ls-files -z --cached --others --exclude-standard | while IFS= read -r -d '' f; do
    [ -e "$f" ] && printf '%s\0' "$f"
  done | COPYFILE_DISABLE=1 tar --null -T - -cf - | COPYFILE_DISABLE=1 tar -x -C "$workdir"
  info "发布候选快照已导出到 ${workdir}（$(find "$workdir" -type f | wc -l | tr -d ' ') 个文件）"
}

# ---------------------------------------------------------------------------
# Go 源码漏洞
# ---------------------------------------------------------------------------
run_govulncheck() {
  local out="${CACHE_DIR}/govulncheck.json"
  rm -f "$out"

  if [ "${SKIP_VULNCHECK:-0}" = "1" ]; then
    if [ "$MODE" = "release" ]; then
      fail "security-release 禁止 SKIP_VULNCHECK——发布前必须真正扫过"
    fi
    echo "############################################################"
    echo "###  SKIPPED: govulncheck (SKIP_VULNCHECK=1)             ###"
    echo "###  本次未做源码漏洞扫描。仅供离线本地使用。            ###"
    echo "############################################################"
    return 0
  fi

  info "govulncheck（漏洞库不可达时 fail-closed）"
  local tmp="${out}.tmp"
  # 漏洞库需要联网。取不到库时**必须失败**而不是静默通过——
  # 静默通过意味着离线时门禁等于不存在，而它看起来仍然是绿的。
  if ! GOWORK=off go tool govulncheck -format json ./... > "$tmp" 2>"${tmp}.err"; then
    if grep -qiE 'no such host|connection refused|timeout|dial tcp' "${tmp}.err" 2>/dev/null; then
      rm -f "$tmp" "${tmp}.err"
      fail "无法获取漏洞库（离线？）。本地可用 SKIP_VULNCHECK=1 显式跳过并留下醒目标记；发布门禁不允许跳过。"
    fi
    # 非网络原因的非零退出通常意味着"发现了漏洞"，交给下面的 finding 解析。
    :
  fi
  mv "$tmp" "$out"
  rm -f "${tmp}.err"
  info "govulncheck 结果 → $out"
}

# ---------------------------------------------------------------------------
# 密钥扫描
# ---------------------------------------------------------------------------
run_gitleaks_snapshot() {
  local out="${CACHE_DIR}/gitleaks.json"
  rm -f "$out"
  info "gitleaks（发布候选快照）"
  docker run --rm -v "${workdir}:/scan:ro" -v "${ROOT_DIR}/${CACHE_DIR}:/out" \
    "$GITLEAKS_IMAGE" dir /scan --report-format json --report-path /out/gitleaks.json --exit-code 0 \
    >/dev/null 2>&1 || fail "gitleaks 执行失败（扫描器错误不可豁免）"
  info "gitleaks 结果 → $out"
}

run_gitleaks_history() {
  local out="${CACHE_DIR}/gitleaks-history.json"
  rm -f "$out"
  info "gitleaks（全历史；仅发布节点执行，耗时较长）"
  docker run --rm -v "${ROOT_DIR}:/repo:ro" -v "${ROOT_DIR}/${CACHE_DIR}:/out" \
    "$GITLEAKS_IMAGE" git /repo --report-format json --report-path /out/gitleaks-history.json --exit-code 0 \
    >/dev/null 2>&1 || fail "gitleaks 全历史扫描执行失败（扫描器错误不可豁免）"
  info "全历史结果 → $out"
}

# ---------------------------------------------------------------------------
# 统一判定
# ---------------------------------------------------------------------------
evaluate() {
  info "统一判定 finding 与 waiver"
  GOWORK=off go run ./scripts/security-eval.go \
    --cache "$CACHE_DIR" \
    --waivers security/waivers.yml \
    --secret-allowlist security/secret-allowlist.yml \
    --mode "$MODE"
}

case "$MODE" in
  source)
    export_release_candidate
    run_govulncheck
    run_gitleaks_snapshot
    evaluate
    info "security-source OK"
    ;;
  release)
    export_release_candidate
    run_govulncheck
    run_gitleaks_snapshot
    run_gitleaks_history
    evaluate
    info "security-release OK"
    ;;
  *)
    fail "未知模式 ${MODE}（可选：source | release）"
    ;;
esac

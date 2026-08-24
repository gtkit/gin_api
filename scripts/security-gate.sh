#!/usr/bin/env bash
# security-gate.sh —— 统一安全门禁：源码漏洞 + 密钥扫描（+ 发布期的镜像扫描）。
#
# 两个入口，刻意分层：
#   security-source   日常/提交前：当前发布候选快照的漏洞与密钥扫描
#   security-release   打 tag 之前：追加全历史密钥扫描、镜像构建与镜像漏洞扫描、SBOM、manifest
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

# APP_NAME 取自应用身份的唯一来源，与 `make image` 用同一个值——
# 两处各写各的会让门禁扫的镜像和实际交付的镜像不是同一个。
APP_NAME="$(sed -n -E 's/^APP_NAME[[:space:]]*=[[:space:]]*(.+)$/\1/p' project.mk)"
[ -n "$APP_NAME" ] || fail "project.mk 缺少 APP_NAME"

# IMAGE_DIGEST 由 build_release_image 填充，manifest 据此绑定被扫镜像。
IMAGE_DIGEST=""

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
# 交付镜像：构建 → 摘要 → 漏洞扫描 → SBOM
# ---------------------------------------------------------------------------
# 只扫源码依赖时，基础镜像带入的系统包漏洞完全不可见——而那些包与应用代码
# 一同交付、一同运行。此前 TRIVY_IMAGE 被读取、被校验"必须钉 digest"，
# 然后**从不使用**，于是 `security-release OK` 是一句关于镜像的空话。

# image_ref 是本次发布候选镜像的本地标签。
image_ref() {
  printf '%s:%s' "$APP_NAME" "$(git rev-parse --short HEAD)"
}

build_release_image() {
  local ref
  ref="$(image_ref)"
  info "构建交付镜像 ${ref}"
  docker build \
    --build-arg APP_NAME="$APP_NAME" \
    --build-arg GOPROXY="$(go env GOPROXY)" \
    -t "$ref" "$ROOT_DIR" >/dev/null ||
    fail "交付镜像构建失败（构建错误不可豁免）"

  # 本地 image ID 而不是 registry digest：镜像尚未推送，registry digest 此刻
  # 并不存在。用 ID 同样能唯一标识"这次扫的是哪个镜像"，且不需要先推送——
  # 先推后扫会让未经扫描的镜像出现在 registry 上。
  IMAGE_DIGEST="$(docker image inspect "$ref" --format '{{.Id}}')" ||
    fail "无法取得交付镜像摘要"
  info "交付镜像摘要 ${IMAGE_DIGEST}"
}

# scan_image 用 Trivy 扫描导出的镜像归档。
#
# **不挂载 docker socket**：把宿主 socket 交给扫描器容器等于给它宿主的完全
# 控制权，而扫描器只需要读一个镜像。供应链门禁本身成为供应链风险不可接受。
scan_image() {
  local ref archive out sbom
  ref="$(image_ref)"
  archive="${workdir}/image.tar"
  out="${CACHE_DIR}/trivy-image.json"
  sbom="${CACHE_DIR}/sbom.cdx.json"
  rm -f "$out" "$sbom"

  info "导出镜像归档供扫描"
  docker save "$ref" -o "$archive" || fail "镜像导出失败（扫描器错误不可豁免）"

  # 两次 trivy 调用**共享缓存卷**。
  #
  # 不共享的后果是每次发版要做两次完整的冷启动：扫描与 SBOM 各自重新下载整个
  # 漏洞库（容器是 --rm，库存在容器内）。实测在网络较慢时这会让发版随机失败在
  # 其中一步上——三次尝试分别死在拉 manifest、库下载超时、SBOM 分析超时。
  #
  # 共享缓存**不削弱门禁**：扫描器镜像、漏洞库来源与判定标准都不变，省掉的只是
  # 同一次发版内的重复下载；库是否过期由 trivy 自己判断并更新，新鲜度不受影响。
  local trivy_cache="gin-api-trivy-cache"
  local trivy_common=(--rm
    -v "${workdir}:/scan:ro" -v "${ROOT_DIR}/${CACHE_DIR}:/out"
    -v "${trivy_cache}:/root/.cache/trivy")

  info "trivy 镜像漏洞扫描"
  docker run "${trivy_common[@]}" \
    "$TRIVY_IMAGE" image --input /scan/image.tar \
    --format json --output /out/trivy-image.json --exit-code 0 --quiet ||
    fail "trivy 镜像扫描执行失败（扫描器错误不可豁免）"
  info "镜像扫描结果 → $out"

  info "生成 CycloneDX SBOM"
  docker run "${trivy_common[@]}" \
    "$TRIVY_IMAGE" image --input /scan/image.tar \
    --format cyclonedx --output /out/sbom.cdx.json --quiet ||
    fail "SBOM 生成失败（扫描器错误不可豁免）"
  info "SBOM → $sbom"
}

# write_manifest 把本次门禁的全部产物绑定到 commit 与镜像摘要。
#
# 没有它，一堆 JSON 文件无法证明属于同一次发布——而发布事故的复盘恰恰要回答
# "这次扫的是哪个镜像、跑了哪些工具"。
#
# 写入必须**原子**：中途失败留下的半份 manifest 会被当成有效记录。
write_manifest() {
  local out="${CACHE_DIR}/manifest.json" tmp="${CACHE_DIR}/.manifest.json.tmp"
  local artifacts="" name path first=1

  for name in govulncheck.json gitleaks.json gitleaks-history.json trivy-image.json sbom.cdx.json; do
    path="${CACHE_DIR}/${name}"
    [ -f "$path" ] || continue
    [ "$first" -eq 1 ] || artifacts="${artifacts},"
    first=0
    artifacts="${artifacts}
    {\"name\": \"${name}\", \"sha256\": \"$(shasum -a 256 "$path" | awk '{print $1}')\"}"
  done

  cat > "$tmp" <<JSON
{
  "mode": "${MODE}",
  "commit": "$(git rev-parse HEAD)",
  "image_digest": "${IMAGE_DIGEST:-}",
  "scanners": {
    "gitleaks": "${GITLEAKS_IMAGE}",
    "trivy": "${TRIVY_IMAGE}"
  },
  "artifacts": [${artifacts}
  ]
}
JSON
  mv -f "$tmp" "$out"
  info "manifest → $out"
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
    write_manifest
    info "security-source OK"
    ;;
  release)
    export_release_candidate
    run_govulncheck
    run_gitleaks_snapshot
    run_gitleaks_history
    build_release_image
    scan_image
    evaluate
    write_manifest
    info "security-release OK"
    ;;
  *)
    fail "未知模式 ${MODE}（可选：source | release）"
    ;;
esac

#!/usr/bin/env bash
# check-restricted.sh —— 在**真正禁止绑定端口**的容器里实跑测试。
#
# 为什么用实跑取代静态传感器：
#
# 此前用 AST 传感器猜"哪些测试会绑端口"。第一版只查函数体内直接出现的
# net.Listen，漏掉了全部经生产函数间接绑定的用例——实测 11 个测试在受限环境
# 失败而传感器显示通过。改成跨函数调用图传播后，漏报变成了 37 报 11 中，
# **26 个误报**。
#
# 误报的代价不是噪音，是**真实的覆盖损失**：被误报的测试会被加上
# RequireLoopback，于是它在受限环境里被跳过——而它本来完全能跑。
# 用一个 70% 误报的近似去替代一个可得的事实，没有道理。
#
# 事实判定就在这里：seccomp 让 bind(2) 返回 EPERM，测试真的绑不上端口，
# 于是"哪些测试依赖端口"不再需要猜。
#
# 代价是需要容器运行时，因此它不在 `make check` 里（那条门禁刻意不依赖
# Docker，以保证任何环境都能复现），而在 `make release-check` 里。
#
# 用法：
#   check-restricted.sh              实跑
#   check-restricted.sh --self-test  自检：证明 seccomp 确实拒绝了 bind
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CACHE_DIR="${ROOT_DIR}/.cache/restricted"

# 测试运行器取自集中声明。
#
# 用自带 git 的完整镜像而不是交付用的 alpine：seccomp 拒绝 bind(2) 会**连带
# 禁掉 DNS**（DNS 客户端要 bind 一个 UDP socket 收响应），于是容器内装不了包。
# git 是 projectinit 测试的真实依赖，缺它那些测试会以"git 不存在"失败，
# 把真正的端口信号淹没在噪音里。
RUNNER_IMAGE="$(sed -n -E 's/^RESTRICTED_RUNNER_IMAGE[[:space:]]*=[[:space:]]*(.+)$/\1/p' "${ROOT_DIR}/security/toolchain.mk")"
[ -n "$RUNNER_IMAGE" ] || { echo "security/toolchain.mk 缺少 RESTRICTED_RUNNER_IMAGE" >&2; exit 1; }

# 只拒绝 bind：网络其余能力保持可用，因此失败必然来自"要监听端口"，
# 而不是来自 DNS 或出站连接——那样才能精确定位到端口依赖。
write_profile() {
  mkdir -p "$CACHE_DIR"
  cat > "${CACHE_DIR}/no-bind.json" <<'JSON'
{
  "defaultAction": "SCMP_ACT_ALLOW",
  "syscalls": [
    { "names": ["bind"], "action": "SCMP_ACT_ERRNO", "errnoRet": 1 }
  ]
}
JSON
}

run_in_container() {
  local script="$1"
  docker run --rm \
    --security-opt seccomp="${CACHE_DIR}/no-bind.json" \
    -v "${ROOT_DIR}:/src" \
    -v "$(go env GOMODCACHE):/gomod" \
    -v "${CACHE_DIR}:/out" \
    -e GOCACHE=/out/gocache -e GOMODCACHE=/gomod -e GOFLAGS=-mod=mod \
    -e GOWORK=off -e ALLOW_NO_LOOPBACK=1 \
    -w /src "$RUNNER_IMAGE" \
    sh -c "$script"
}

run_check() {
  command -v docker >/dev/null 2>&1 || {
    echo "FAIL: 需要容器运行时来构造受限环境" >&2
    exit 1
  }
  write_profile

  echo "[check-restricted] 在禁止 bind(2) 的容器里实跑测试"

  # **用 go test 的退出码判定，不靠 grep**。
  #
  # 曾经写成 `if run_in_container ... | grep FAIL; then 失败`，结果在
  # `set -o pipefail` 下，go test 的非零退出让整条管道返回非零，if 条件为假，
  # 于是有 FAIL 行也直接落到成功分支——门禁把失败报成了通过。
  local status=0
  # 不在容器内装包：seccomp 拒绝 bind 会连带禁掉 DNS，装不了。
  # 运行器镜像必须**自带** git，否则 projectinit 的测试会以"git 不存在"失败。
  run_in_container 'git --version >/dev/null 2>&1 || {
      echo "runner image lacks git: port failures cannot be told apart from missing deps"; exit 2; }
    go test ./... 2>&1' \
    > "${CACHE_DIR}/run.log" 2>&1 || status=$?

  if [ "$status" -ne 0 ]; then
    grep -E '^(--- FAIL|FAIL[[:space:]])' "${CACHE_DIR}/run.log" | sed 's/^/  /' >&2
    echo "" >&2
    echo "FAIL: 上列测试在禁止绑定端口的环境里失败。" >&2
    echo "真正需要端口的测试应调用 testutil.RequireLoopback(t) 以便跳过；" >&2
    echo "不需要端口却失败的，说明它意外依赖了监听——那是被测代码的问题。" >&2
    echo "完整输出：${CACHE_DIR}/run.log" >&2
    exit 1
  fi

  echo "[check-restricted] OK：受限环境下全部测试通过或按声明跳过"
}

# run_self_test 证明这套受限环境**真的受限**。
#
# 没有这一步，一个 seccomp profile 写错的容器会让全部测试照常通过，
# 而门禁会报告"受限环境验证通过"——那正是它要防止的假绿。
run_self_test() {
  command -v docker >/dev/null 2>&1 || {
    echo "[self-test] SKIP: 无容器运行时"
    return 0
  }
  write_profile

  local out
  out="$(run_in_container 'cat > /tmp/p.go <<EOF
package main

import ("fmt";"net")

func main() {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { fmt.Println("BIND FAILED"); return }
	l.Close()
	fmt.Println("BIND OK")
}
EOF
go run /tmp/p.go' 2>&1 | tail -1)"

  if [ "$out" = "BIND FAILED" ]; then
    echo "[self-test] OK: 容器内确实无法绑定回环端口"
  else
    echo "[self-test] FAIL: 容器内仍能绑定端口（得到 '$out'）——受限验证是假的" >&2
    exit 1
  fi
}

case "${1:-}" in
  --self-test) run_self_test ;;
  "")          run_self_test && run_check ;;
  *)           echo "用法: $(basename "$0") [--self-test]" >&2; exit 2 ;;
esac

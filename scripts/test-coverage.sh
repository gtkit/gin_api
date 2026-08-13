#!/usr/bin/env sh
# test-coverage.sh — 跑 race 测试并生成 coverage profile（与 check-coverage.sh 配套）。
#
# 用法：test-coverage.sh <coverage-profile> [go-package ...]（缺省包为 ./...）
#
# **一次全量 go test，不按包分组**。哪些测试需要绑定回环端口，由测试自己经
# `testutil.RequireLoopback` 声明，受限环境下它们自行跳过。
#
# 此前这里维护一份"哪些包、哪些测试名会绑端口"的黑名单。那是一处**必然漂移的
# 重复来源**，而实测证明它已经漂移到完全失效：名单里的三个测试名早已随代码删除
# 而不复存在，`grep -Ev` 因此一个测试都没排除——middleware 在"受限路径"下仍是
# 77 个全跑，internal/bootstrap 整包更是从未被处理。
#
# 后果比漏配更糟：那段代码制造了"受限环境已被特殊处理"的假象，于是
# "受限环境实测 62.5%" 这个门禁依据，实际上一直是正常环境的数字。
#
# 现在这件事只记录在测试自己身上，并由 scripts/check-loopback-tests.go 强制。
#
# ALLOW_NO_LOOPBACK=1 经环境直接传给 go test，让受限路径在**任意机器上都可复现**——
# 覆盖率门禁的下界因此第一次可以被验证，而不是只能被声称。
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

# GO_BUILD_TAGS 让验证口径覆盖被构建约束门控的代码（当前是 `scaffold`）。
#
# 必须带上：模块生成器与项目实例化器的测试锁定着 module-scaffold-wiring 与
# project-instantiation 两组需求。不带 tag 跑，这些测试根本不会被编译，两组需求
# 会在无人察觉的情况下集体失去门禁保护——而 `make check` 依然全绿。
#
# 分工是「构建口径排除脚手架，验证口径包含脚手架」：go build 与 Dockerfile 不带
# tag（服务二进制里不该有生成器），测试/vet/lint 带 tag。
if [ -n "${GO_BUILD_TAGS:-}" ]; then
  go test -tags "$GO_BUILD_TAGS" -race -count=1 -timeout=5m -covermode=atomic -coverprofile="$profile" "$@"
else
  go test -race -count=1 -timeout=5m -covermode=atomic -coverprofile="$profile" "$@"
fi

if [ "${ALLOW_NO_LOOPBACK:-0}" = "1" ]; then
  echo "coverage profile created on the restricted path (loopback tests skipped): $profile"
else
  echo "coverage profile created: $profile"
fi

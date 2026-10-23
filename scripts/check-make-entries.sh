#!/usr/bin/env bash
# Exercise Make entry wiring in an isolated fixture. Go/lint stubs record the
# actual invoked commands. The cleaner and tiny integration fixtures use the
# real local Go tool; fixture tests run without vet, race or coverage checks.
set -euo pipefail
# The fixture owns its module and output paths. Parent command-line overrides
# (notably external coverage output) must not redirect the fixture's cleaner.
unset MAKEFLAGS MFLAGS MAKELEVEL MAKEOVERRIDES MAKEFILES GOFLAGS COVERAGE_PROFILE GO_TARGETS GO_BUILD_TAGS
export GOWORK=off
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
sandbox="$(mktemp -d "${TMPDIR:-/tmp}/gin-api-make-entries.XXXXXX")" || exit 1
cleanup(){
  local rc=$?
  if ! rm -rf -- "${sandbox:?}"; then
    echo "FAIL: could not remove fixture ${sandbox}" >&2
    rc=1
  fi
  exit "$rc"
}
trap cleanup EXIT
WIRING_GO="$(command -v go)" || { echo "go is required" >&2; exit 1; }
WIRING_MAKE="$(command -v make)" || { echo "make is required" >&2; exit 1; }
export WIRING_GO
WIRING_LINT_VERSION="$(awk '/^GOLANGCI_LINT_VERSION[[:space:]]*=/ { print $3; exit }' "$ROOT/security/toolchain.mk")"
[ -n "$WIRING_LINT_VERSION" ] || { echo "missing declared lint version" >&2; exit 1; }
export WIRING_LINT_VERSION
fixture="$sandbox/work area"
mkdir -p "$fixture/scripts" "$fixture/security" "$fixture/bin" "$fixture/.cache" "$sandbox/tools" "$sandbox/outside"
cp "$ROOT/Makefile" "$fixture/Makefile"
cp -R "$ROOT/make" "$fixture/make"
cp "$ROOT/security/toolchain.mk" "$fixture/security/toolchain.mk"
cp "$ROOT/scripts/clean.go" "$ROOT/scripts/check-tool-version.sh" "$fixture/scripts/"
printf 'APP_NAME = fixture\nAPP_DISPLAY_NAME = Fixture\n' > "$fixture/project.mk"
printf 'module example.com/fixture\n\ngo 1.27.0\n' > "$fixture/go.mod"
MAKE_WIRING_TRACE="$sandbox/trace"
export MAKE_WIRING_TRACE
LINT_WIRING_FAIL=''
export LINT_WIRING_FAIL
: > "$MAKE_WIRING_TRACE"
cat > "$sandbox/tools/go" <<'STUB'
#!/usr/bin/env bash
set -eu
printf 'GO %s\n' "$*" >> "$MAKE_WIRING_TRACE"
if [ "${1:-}" = run ] && [ "${2:-}" = ./scripts/clean.go ]; then
  exec "$WIRING_GO" "$@"
fi
if [ "${1:-}" = test ] && [ "${INTEGRATION_WIRING_RUN:-0}" = 1 ]; then
  [ "${PLUGIN_INTEGRATION:-0}" = 1 ] || { echo 'missing plugin opt-in' >&2; exit 1; }
  # 上面的 GO 记录保留 Make 原始参数，用于验证生产目标的接线。
  # 真正执行的只有本脚本生成的微型夹具；固定关闭 vet 并移除 race，
  # 不因验证接线而顺带运行调用者尚未授权的检查。
  shift
  ordinary_args=(-vet=off)
  for arg in "$@"; do
    case "$arg" in
      -race|-race=true|-race=false|-vet=off) ;;
      -vet=*|-cover*) echo 'fixture refuses vet or coverage checks' >&2; exit 1 ;;
      *) ordinary_args+=("$arg") ;;
    esac
  done
  printf 'GO_EXEC test %s\n' "${ordinary_args[*]}" >> "$MAKE_WIRING_TRACE"
  printf '[fixture execution] go test %s\n' "${ordinary_args[*]}"
  exec "$WIRING_GO" test "${ordinary_args[@]}"
fi
STUB
cat > "$sandbox/tools/golangci-lint" <<'STUB'
#!/usr/bin/env bash
set -eu
printf 'LINT %s\n' "$*" >> "$MAKE_WIRING_TRACE"
if [ "${1:-}" = version ]; then printf 'golangci-lint version %s\n' "$WIRING_LINT_VERSION"; fi
if [ "${1:-}" = run ]; then
  if [ "$LINT_WIRING_FAIL" = main ] && [ "$2" != --enable-only=errcheck ]; then exit 23; fi
  if [ "$LINT_WIRING_FAIL" = scripts ] && [ "$2" = --enable-only=errcheck ]; then exit 24; fi
fi
STUB
cat > "$fixture/bin/fixture" <<'STUB'
#!/usr/bin/env bash
set -eu
printf 'BINARY %s\n' "$*" >> "$MAKE_WIRING_TRACE"
STUB
chmod +x "$sandbox/tools/go" "$sandbox/tools/golangci-lint" "$fixture/bin/fixture"
export PATH="$sandbox/tools:$PATH"
cd "$fixture"
make --no-print-directory BUILD_COMMIT=fixture BUILD_TIME=unknown
[ "$(wc -l < "$MAKE_WIRING_TRACE" | tr -d ' ')" = 1 ]
grep -Fxq 'GO run -tags=sonic . server' "$MAKE_WIRING_TRACE"
: > "$MAKE_WIRING_TRACE"
make --no-print-directory run BUILD_COMMIT=fixture BUILD_TIME=unknown
make --no-print-directory lint BUILD_COMMIT=fixture BUILD_TIME=unknown
make --no-print-directory test GO_TARGETS=./fixture BUILD_COMMIT=fixture BUILD_TIME=unknown
grep -Fq 'GO run -tags=sonic . server' "$MAKE_WIRING_TRACE"
grep -Fq 'LINT run ./...' "$MAKE_WIRING_TRACE"
grep -Fq 'GO test -tags scaffold -race -count=1 -timeout=5m ./fixture' "$MAKE_WIRING_TRACE"
: > "$MAKE_WIRING_TRACE"
make --no-print-directory migrate-status test BUILD_COMMIT=fixture BUILD_TIME=unknown
grep -Fq 'BINARY migrate status' "$MAKE_WIRING_TRACE"
if grep -q '^GO test\|server' "$MAKE_WIRING_TRACE"; then echo 'FAIL: migration environment ran tests/server' >&2; exit 1; fi
make --no-print-directory migrate-status dev BUILD_COMMIT=fixture BUILD_TIME=unknown
grep -Fq 'GO run -tags=sonic . migrate status' "$MAKE_WIRING_TRACE"

# A second -f file exposes values without invoking any application target.
# The expected assignments are the pre-split compatibility contract, including
# recursive expansion and the conditional migration name.
cat > "$sandbox/expected-vars.mk" <<'MAKE'
include project.mk
include security/toolchain.mk
PORT ?= 8008
ENV = $(firstword $(filter-out $@,$(MAKECMDGOALS)))
BIN_DIR := ./bin
Linux := CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN := go run -tags="sonic"
BUILD_COMMIT ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ || printf unknown)
BUILD_FLAGS := -tags="sonic" -ldflags "-s -w -X main.BuildCommit=$(BUILD_COMMIT) -X main.BuildTime=$(BUILD_TIME)"
COMPOSE_ENV_FILE ?= .env
COMPOSE_PROJECT ?= $(APP_NAME)
APP_COMPOSE = docker compose --project-name "$(COMPOSE_PROJECT)" --env-file "$(COMPOSE_ENV_FILE)" --profile app
APP_COMPOSE_ENV = DEV_APP_NAME="$(APP_NAME)" DEV_BUILD_COMMIT="$(BUILD_COMMIT)" DEV_BUILD_TIME="$(BUILD_TIME)"
GO_TARGETS ?= ./...
LINT_TARGETS ?= ./...
GO_BUILD_TAGS ?= scaffold
COVERAGE_PROFILE ?= .cache/coverage.out
COVERAGE_MIN ?= 72.5
INTEGRATION_PKGS = $(sort $(patsubst %,./%,$(dir $(shell grep -rl '^//go:build integration' --include='*_test.go' internal pkg cmd 2>/dev/null))))
MIGRATE = $(if $(filter dev,$(ENV)),$(RUN) .,$(BIN_DIR)/$(APP_NAME))
ifneq ($(filter migrate-migration,$(MAKECMDGOALS)),)
MIGRATION_NAME := $(firstword $(filter-out migrate-migration dev test prod,$(MAKECMDGOALS)))
endif
MAKE
cat > "$sandbox/inspect.mk" <<'MAKE'
.PHONY: __inspect
__inspect:
	@$(foreach name,PORT ENV BIN_DIR Linux RUN BUILD_COMMIT BUILD_TIME BUILD_FLAGS COMPOSE_ENV_FILE COMPOSE_PROJECT APP_COMPOSE APP_COMPOSE_ENV GO_TARGETS LINT_TARGETS GO_BUILD_TAGS COVERAGE_PROFILE COVERAGE_MIN INTEGRATION_PKGS MIGRATE MIGRATION_NAME,$(info $(name)|$(origin $(name))|$(flavor $(name))|$(value $(name))|$($(name)))) :
MAKE
compare_variables() {
  make --no-print-directory -s -f Makefile -f "$sandbox/inspect.mk" __inspect BUILD_COMMIT=fixture BUILD_TIME=unknown "$@" > "$sandbox/vars.actual"
  make --no-print-directory -s -f "$sandbox/expected-vars.mk" -f "$sandbox/inspect.mk" __inspect BUILD_COMMIT=fixture BUILD_TIME=unknown "$@" > "$sandbox/vars.expected"
  if ! cmp -s "$sandbox/vars.expected" "$sandbox/vars.actual"; then
    echo 'FAIL: Make variable origin/flavor/raw/expanded value changed' >&2
    diff -u "$sandbox/vars.expected" "$sandbox/vars.actual" >&2 || true
    exit 1
  fi
}
compare_variables
PORT=9011 BIN_DIR=env-bin RUN='go run -tags=env' LINT_TARGETS=./env compare_variables
PORT=9012 BIN_DIR=env-bin RUN='go run -tags=env' compare_variables -e
compare_variables PORT=9020 BIN_DIR=custom-bin Linux='CUSTOM=1' RUN='go run -tags=custom' BUILD_COMMIT=custom BUILD_TIME=fixed COMPOSE_ENV_FILE=custom.env COMPOSE_PROJECT=custom GO_TARGETS=./custom LINT_TARGETS=./custom GO_BUILD_TAGS=custom COVERAGE_PROFILE=custom.out COVERAGE_MIN=88 INTEGRATION_PKGS=./custom MIGRATION_NAME=custom
compare_variables PORT= BIN_DIR= RUN= GO_TARGETS= LINT_TARGETS= GO_BUILD_TAGS= COVERAGE_PROFILE= COVERAGE_MIN=
compare_variables MAKECMDGOALS='migrate-migration create_fixture_table dev'
compare_variables MAKECMDGOALS='migrate-status dev'
compare_variables MAKECMDGOALS='migrate-status test'

# Restrict PATH so an UPX installed on the host cannot affect the absent case.
build_tools="$sandbox/build-tools"
mkdir -p "$build_tools"
ln -s "$(command -v mkdir)" "$build_tools/mkdir"
cat > "$build_tools/go" <<'STUB'
#!/bin/sh
set -eu
printf 'BUILD env=%s/%s/%s args=%s\n' "${CGO_ENABLED:-}" "${GOOS:-}" "${GOARCH:-}" "$*" >> "$MAKE_WIRING_TRACE"
[ "${BUILD_WIRING_FAIL:-0}" = 0 ] || exit 21
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then
    shift
    printf 'compiled fixture\n' > "$1"
    exit 0
  fi
  shift
done
exit 22
STUB
chmod +x "$build_tools/go"
: > "$MAKE_WIRING_TRACE"
PATH="$build_tools" "$WIRING_MAKE" --no-print-directory build BIN_DIR=./build-bin BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/build-no-upx.log" 2>&1
grep -Fq 'UPX not found' "$sandbox/build-no-upx.log"
grep -Fq 'BUILD env=0/linux/amd64 args=build -trimpath -tags=sonic' "$MAKE_WIRING_TRACE"
[ "$(cat build-bin/fixture)" = 'compiled fixture' ]
cat > "$build_tools/upx" <<'STUB'
#!/bin/sh
set -eu
printf 'UPX %s\n' "$*" >> "$MAKE_WIRING_TRACE"
[ "${UPX_WIRING_FAIL:-0}" = 0 ] || exit 23
STUB
chmod +x "$build_tools/upx"
: > "$MAKE_WIRING_TRACE"
PATH="$build_tools" "$WIRING_MAKE" --no-print-directory build BIN_DIR=./build-bin BUILD_COMMIT=fixture BUILD_TIME=unknown Linux='CGO_ENABLED=1 GOOS=darwin GOARCH=arm64' BUILD_FLAGS=-tags=custom
grep -Fxq 'BUILD env=1/darwin/arm64 args=build -trimpath -tags=custom -o ./build-bin/fixture .' "$MAKE_WIRING_TRACE"
grep -Fxq 'UPX -9 ./build-bin/fixture' "$MAKE_WIRING_TRACE"
if PATH="$build_tools" UPX_WIRING_FAIL=1 "$WIRING_MAKE" --no-print-directory build BIN_DIR=./build-bin BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/build-upx-failure.log" 2>&1; then
  echo 'FAIL: UPX failure escaped the build target' >&2; exit 1
fi
: > "$MAKE_WIRING_TRACE"
if PATH="$build_tools" BUILD_WIRING_FAIL=1 "$WIRING_MAKE" --no-print-directory build BIN_DIR=./build-bin BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/build-compile-failure.log" 2>&1; then
  echo 'FAIL: compiler failure escaped the build target' >&2; exit 1
fi
if grep -q '^UPX ' "$MAKE_WIRING_TRACE"; then echo 'FAIL: UPX ran after compiler failure' >&2; exit 1; fi

# Override the metadata variable only; the sole executed goal is __inspect.
# In particular, do not use -n/-q on release-check or other recursive recipes.
target_metadata() {
  make --no-print-directory -p -f Makefile -f "$sandbox/inspect.mk" __inspect BUILD_COMMIT=fixture BUILD_TIME=unknown "MAKECMDGOALS=$1" > "$sandbox/make.database"
  awk -v target="$2" '$0 ~ "^" target ":" { found=1; print; next } found && /^\t/ { print; next } found && /^$/ { exit }' "$sandbox/make.database"
}

# Make 允许同一目标多次声明依赖，因此不能按源码中的目标行计数。
# 由 Make 自身解析 include、行尾注释和依赖合并，再检查实际配方及覆盖诊断。
# 始终只执行无副作用的 __inspect，port/kill-port 和它们的依赖不会运行。
# 返回 1 专指发现重复配方；解析错误、缺失配方或工具失败返回 2，防止反例
# 因为无关失败也被判为通过。条件调用会关闭 set -e，关键步骤须显式检查。
check_port_recipes() {
  local rules="$1"
  local database="$sandbox/port.database"
  local diagnostics="$sandbox/port.diagnostics"
  local warning_status=0
  local target
  if ! LC_ALL=C "$WIRING_MAKE" --no-print-directory -p -f Makefile -f "$sandbox/inspect.mk" -f "$rules" __inspect BUILD_COMMIT=fixture BUILD_TIME=unknown > "$database" 2> "$diagnostics"; then
    cat "$diagnostics" >&2
    return 2
  fi
  # GNU Make 旧版使用 commands，新版使用 recipe；引号形式也随版本不同。
  grep -Eq "warning: (overriding|ignoring old) (commands|recipe) for target .(port|kill-port).$" "$diagnostics" || warning_status=$?
  case "$warning_status" in
    0) cat "$diagnostics" >&2; return 1 ;;
    1) ;;
    *) echo 'FAIL: could not inspect Make recipe diagnostics' >&2; return 2 ;;
  esac
  for target in port kill-port; do
    if ! awk -v target="$target" '
      $0 ~ "^" target ":" { found=1; next }
      found && /^\t/ { recipe=1; exit }
      found && /^$/ { exit }
      END { exit !recipe }
    ' "$database"; then
      echo "FAIL: $target has no ordinary recipe" >&2
      return 2
    fi
  done
  return 0
}

port_rules="$sandbox/port-rules.mk"
: > "$port_rules"
check_port_recipes "$port_rules" || { echo 'FAIL: baseline port recipes' >&2; exit 1; }
printf 'port: ## dependency-only comment\nkill-port: ## dependency-only comment\n' > "$port_rules"
check_port_recipes "$port_rules" || { echo 'FAIL: commented target declarations' >&2; exit 1; }
printf '.PHONY: __port_dependency\n__port_dependency:\n\t@:\nport kill-port: __port_dependency\n' > "$port_rules"
check_port_recipes "$port_rules" || { echo 'FAIL: legal dependency merge' >&2; exit 1; }
for target in port kill-port; do
  for form in comment dependency; do
    if [ "$form" = comment ]; then
      printf '%s: ## duplicate recipe\n\t@:\n' "$target" > "$port_rules"
    else
      printf '.PHONY: __port_dependency\n__port_dependency:\n\t@:\n%s: __port_dependency\n\t@:\n' "$target" > "$port_rules"
    fi
    if check_port_recipes "$port_rules" > "$sandbox/port-check.log" 2>&1; then
      echo "FAIL: accepted duplicate $target recipe with $form" >&2
      exit 1
    else
      port_status=$?
    fi
    if [ "$port_status" -ne 1 ]; then
      cat "$sandbox/port-check.log" >&2
      echo "FAIL: $target/$form failed without detecting a duplicate recipe" >&2
      exit 1
    fi
  done
done
echo 'OK: port recipes accept comments/dependency merges and reject four duplicate fixtures'

target_metadata 'migrate-migration build dev' build > "$sandbox/build.target"
grep -Fq '@:' "$sandbox/build.target"
if grep -Fq 'go build' "$sandbox/build.target"; then echo 'FAIL: migration build placeholder lost' >&2; exit 1; fi
target_metadata 'migrate-migration port dev' port > "$sandbox/port.target"
grep -Fq 'lsof -nP -iTCP:$(PORT) -sTCP:LISTEN || echo' "$sandbox/port.target"
target_metadata 'migrate-migration check dev' check > "$sandbox/check.target"
grep -Fq 'check: check-architecture check-capability-inventory check-scaffold-isolation check-tidy check-package-vars check-doc-refs check-image-digests check-security-eval check-release-wiring check-appmeta check-hooks check-build-context check-make-entries tool vet check-modern check-coverage' "$sandbox/check.target"
grep -Fq '@:' "$sandbox/check.target"
target_metadata 'migrate-migration release-check dev' release-check > "$sandbox/release.target"
[ "$(grep -Fc '$(MAKE)' "$sandbox/release.target")" = 6 ]
grep -Fq 'REQUIRE_DB_INTEGRATION=1 PLUGIN_INTEGRATION=1 $(MAKE) test-integration' "$sandbox/release.target"

: > "$MAKE_WIRING_TRACE"
make --no-print-directory migrate-migration create_fixture_table dev BUILD_COMMIT=fixture BUILD_TIME=unknown
grep -Fxq 'GO run -tags=sonic . make migration create_fixture_table --from-model' "$MAKE_WIRING_TRACE"
: > "$MAKE_WIRING_TRACE"
if make --no-print-directory migrate-migration dev BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/missing-name.log" 2>&1; then
  echo 'FAIL: accepted an empty migration name' >&2; exit 1
fi
grep -Fq '用法: make migrate-migration create_<table>_table dev' "$sandbox/missing-name.log"
[ ! -s "$MAKE_WIRING_TRACE" ]
make --no-print-directory migrate-status prod BUILD_COMMIT=fixture BUILD_TIME=unknown
grep -Fxq 'BINARY migrate status' "$MAKE_WIRING_TRACE"
: > "$MAKE_WIRING_TRACE"
make --no-print-directory migrate-status run lint BUILD_COMMIT=fixture BUILD_TIME=unknown
grep -Fxq 'BINARY migrate status' "$MAKE_WIRING_TRACE"
if grep -q 'server\|^GO test\|^LINT' "$MAKE_WIRING_TRACE"; then echo 'FAIL: migration aliases ran ordinary commands' >&2; exit 1; fi

missing_fixture="$sandbox/missing include"
cp -R "$fixture" "$missing_fixture"
rm "$missing_fixture/make/quality.mk"
: > "$MAKE_WIRING_TRACE"
if make --no-print-directory -C "$missing_fixture" run BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/missing-include.log" 2>&1; then
  echo 'FAIL: accepted a missing required Make include' >&2; exit 1
fi
grep -Fq 'make/quality.mk' "$sandbox/missing-include.log"
[ ! -s "$MAKE_WIRING_TRACE" ]

: > "$MAKE_WIRING_TRACE"
if LINT_WIRING_FAIL=main make --no-print-directory lint BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/lint-main.log" 2>&1; then
  echo 'FAIL: main lint failure escaped the Make gate' >&2; exit 1
fi
grep -Fxq 'LINT run ./...' "$MAKE_WIRING_TRACE"
if grep -Fq -- '--enable-only=errcheck' "$MAKE_WIRING_TRACE"; then echo 'FAIL: scripts ran after main lint failure' >&2; exit 1; fi
if grep -Fq '✅ golangci-lint run' "$sandbox/lint-main.log"; then echo 'FAIL: lint failure reported success' >&2; exit 1; fi
: > "$MAKE_WIRING_TRACE"
if LINT_WIRING_FAIL=scripts make --no-print-directory lint BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/lint-scripts.log" 2>&1; then
  echo 'FAIL: script lint failure escaped the Make gate' >&2; exit 1
fi
grep -Fq 'LINT run --enable-only=errcheck scripts/clean.go' "$MAKE_WIRING_TRACE"
if grep -Fq '✅ golangci-lint run' "$sandbox/lint-scripts.log"; then echo 'FAIL: script lint failure reported success' >&2; exit 1; fi

printf 'sentinel' > bin/sentinel
printf 'coverage' > .cache/coverage.out
printf 'sentinel' > .cache/sentinel
make --no-print-directory clean BUILD_COMMIT=fixture BUILD_TIME=unknown
[ ! -e bin/fixture ] && [ ! -e .cache/coverage.out ]
[ "$(cat bin/sentinel)" = sentinel ] && [ "$(cat .cache/sentinel)" = sentinel ]
printf 'managed' > bin/fixture
printf 'outside sentinel' > "$sandbox/outside/coverage.out"
mv .cache .cache.saved
ln -s "$sandbox/outside" .cache
if make --no-print-directory clean BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/symlink.log" 2>&1; then echo 'FAIL: accepted symlink' >&2; exit 1; fi
[ -f bin/fixture ] && [ "$(cat "$sandbox/outside/coverage.out")" = 'outside sentinel' ]
rm .cache
mv .cache.saved .cache
if make --no-print-directory clean BIN_DIR=/ BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/outside.log" 2>&1; then echo 'FAIL: accepted unsafe bin directory' >&2; exit 1; fi
[ -f bin/fixture ]
mkdir -p cmd internal/fixture pkg
cat > cmd/prerequisites_test.go <<'FIXTURE'
package cmd
import ("os"; "testing")
func TestPluginIntegrationPrerequisites(t *testing.T) {
  if os.Getenv("PLUGIN_INTEGRATION") != "1" { t.Fatal("missing plugin opt-in") }
}
FIXTURE
cat > cmd/integration_test.go <<'FIXTURE'
//go:build integration
package cmd
import ("os"; "testing")
func TestIntegrationWiringFixture(t *testing.T) {
  if os.Getenv("INTEGRATION_FORCE_FAILURE") == "1" { t.Fatal("forced integration fixture failure") }
}
FIXTURE
cat > internal/fixture/integration_test.go <<'FIXTURE'
//go:build integration
package fixture
import "testing"
func TestDependencyFixture(t *testing.T) {}
FIXTURE
INTEGRATION_WIRING_RUN=1 make --no-print-directory test-integration BUILD_COMMIT=fixture BUILD_TIME=unknown
if INTEGRATION_WIRING_RUN=1 INTEGRATION_FORCE_FAILURE=1 make --no-print-directory test-integration BUILD_COMMIT=fixture BUILD_TIME=unknown > "$sandbox/integration-failure.log" 2>&1; then
  echo 'FAIL: cmd integration failure escaped the Make gate' >&2; exit 1
fi
grep -Fq 'forced integration fixture failure' "$sandbox/integration-failure.log"
rm cmd/integration_test.go
: > "$MAKE_WIRING_TRACE"
INTEGRATION_WIRING_RUN=1 INTEGRATION_FORCE_FAILURE=1 make --no-print-directory test-integration BUILD_COMMIT=fixture BUILD_TIME=unknown
if grep '^GO test.*-race.*\./cmd/' "$MAKE_WIRING_TRACE"; then echo 'FAIL: removed fixture still discovered' >&2; exit 1; fi
grep -q '^GO_EXEC test -vet=off ' "$MAKE_WIRING_TRACE"
if grep -Eq '^GO_EXEC .* (-race|-cover)' "$MAKE_WIRING_TRACE"; then
  echo 'FAIL: ordinary fixture execution enabled race or coverage checks' >&2
  exit 1
fi
echo 'OK: Make entry/clean boundaries; ordinary Go fixtures (vet off, no race/coverage), discovery and forced opt-in failure'

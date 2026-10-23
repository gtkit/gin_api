#!/usr/bin/env bash
# Exercise Make entry wiring in an isolated fixture. Go/lint stubs record the
# actual invoked commands; the cleaner itself runs with the real local Go tool.
set -euo pipefail
# The fixture owns its module and output paths. Parent command-line overrides
# (notably external coverage output) must not redirect the fixture's cleaner.
unset MAKEFLAGS MFLAGS MAKELEVEL MAKEOVERRIDES COVERAGE_PROFILE GO_TARGETS GO_BUILD_TAGS
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
  exec "$WIRING_GO" "$@"
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

# Override the metadata variable only; the sole executed goal is __inspect.
# In particular, do not use -n/-q on release-check or other recursive recipes.
target_metadata() {
  make --no-print-directory -p -f Makefile -f "$sandbox/inspect.mk" __inspect BUILD_COMMIT=fixture BUILD_TIME=unknown "MAKECMDGOALS=$1" > "$sandbox/make.database"
  awk -v target="$2" '$0 ~ "^" target ":" { found=1; print; next } found && /^\t/ { print; next } found && /^$/ { exit }' "$sandbox/make.database"
}
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
echo 'OK: Make entry/clean boundaries; real cmd integration discovery and forced opt-in failure'

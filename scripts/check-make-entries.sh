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
cp "$ROOT/security/toolchain.mk" "$fixture/security/toolchain.mk"
cp "$ROOT/scripts/clean.go" "$ROOT/scripts/check-tool-version.sh" "$fixture/scripts/"
printf 'APP_NAME = fixture\nAPP_DISPLAY_NAME = Fixture\n' > "$fixture/project.mk"
printf 'module example.com/fixture\n\ngo 1.27.0\n' > "$fixture/go.mod"
MAKE_WIRING_TRACE="$sandbox/trace"
export MAKE_WIRING_TRACE
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
STUB
cat > "$fixture/bin/fixture" <<'STUB'
#!/usr/bin/env bash
set -eu
printf 'BINARY %s\n' "$*" >> "$MAKE_WIRING_TRACE"
STUB
chmod +x "$sandbox/tools/go" "$sandbox/tools/golangci-lint" "$fixture/bin/fixture"
export PATH="$sandbox/tools:$PATH"
cd "$fixture"
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

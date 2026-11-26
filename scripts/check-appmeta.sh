#!/bin/sh
# Print APP_NAME only after validating the restricted Make assignment file.
set -eu
[ "$#" -gt 0 ] || set -- internal/appmeta/identity.mk

LC_ALL=C awk '
function fail(message) {
    print "[check-appmeta] " FILENAME ": " message > "/dev/stderr"
    invalid = 1
    exit 1
}
{
    sub(/\r$/, "")
}
/^$/ || (/^#/ && !/\\$/) { next }
/^APP_NAME = / {
    if (name != "") fail("duplicate APP_NAME")
    name = substr($0, length("APP_NAME = ") + 1)
    if (name !~ /^[a-z][a-z0-9_]*$/ || length(name) > 63) fail("invalid APP_NAME")
    next
}
/^APP_DISPLAY_NAME = / {
    if (display != "") fail("duplicate APP_DISPLAY_NAME")
    display = substr($0, length("APP_DISPLAY_NAME = ") + 1)
    if (display !~ /^[A-Za-z0-9][A-Za-z0-9 ._-]*$/ || display ~ / $/ || length(display) > 64) {
        fail("invalid APP_DISPLAY_NAME")
    }
    next
}
{ fail("expected a literal identity assignment or a full-line comment") }
END {
    if (invalid) exit 1
    if (name == "" || display == "") fail("APP_NAME and APP_DISPLAY_NAME are both required")
    print name
}
' "$1"

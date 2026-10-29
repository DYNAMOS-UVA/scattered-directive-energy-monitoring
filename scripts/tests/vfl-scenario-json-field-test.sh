#!/bin/bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/vfl-scenario-common.sh"

assert_field() {
    local json="$1" field="$2" expected="$3" actual
    actual=$(json_field "$json" "$field")
    if [ "$actual" != "$expected" ]; then
        fail "${field}: expected '${expected}', got '${actual}'"
    fi
}

assert_field '{"status":"pending","results":[]}' status pending
assert_field '{"status":"pending","results":[]}' rounds 0
assert_field '{"status":"pending","results":[]}' last_clients ""
assert_field '{}' status ""
assert_field '{}' rounds 0
assert_field 'not JSON' status ""

# Exceed Linux's per-argument limit without passing the payload to an executable.
printf -v padding '%*s' 1048576 ''
large_json='{"status":"done","results":[{"clients":3},{"clients":2}],"padding":"'"$padding"'"}'
assert_field "$large_json" status done
assert_field "$large_json" rounds 2
assert_field "$large_json" last_clients 2

echo "PASS: scenario JSON fields, including a response larger than 1 MiB"

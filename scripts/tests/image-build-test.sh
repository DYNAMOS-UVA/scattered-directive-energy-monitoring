#!/bin/bash

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP="$(mktemp -d)"
trap 'rm -rf "$TEMP"' EXIT
export BUILD_LOG="$TEMP/docker.log"
(
    unset DYNAMOS_ROOT DYNAMOS_IMAGE_TAG
    cd "$TEMP"
    source "$ROOT/dynamos.conf"
    test "$DYNAMOS_ROOT" = "$ROOT"
    test "$DOCKERHUB_ACCOUNT" = dynamos1
    test "$DYNAMOS_IMAGE_TAG" = fed-encrypt-dynamic
    test "$CONFIG_PATH" = "$ROOT/configuration"
)
(
    export DYNAMOS_ROOT="$TEMP/checkout with spaces"
    export DYNAMOS_IMAGE_TAG=custom-test
    source "$ROOT/dynamos.conf"
    test "$DYNAMOS_ROOT" = "$TEMP/checkout with spaces"
    test "$DYNAMOS_IMAGE_TAG" = custom-test
    test "$CHARTS_FABRIC_PATH" = "$DYNAMOS_ROOT/fabric/charts"
)
branch="$(git -C "$ROOT" rev-parse --abbrev-ref HEAD)"
for project in go python; do
    target=api-gateway
    if [ "$project" = python ]; then target=vfl-party; fi
    make -n -C "$ROOT/$project" "$target" > "$TEMP/dry-run"
    grep -q -- "-t dynamos1/$target:$branch" "$TEMP/dry-run"
done
mkdir -p "$TEMP/bin" "$TEMP/go/cmd/api-gateway" "$TEMP/go/pkg" \
    "$TEMP/python/dynamos-python-lib/dist" \
    "$TEMP/python/dynamos-python-lib/protofiles"
for target in vfl-party vfl-aggregator vfl-authority; do
    mkdir -p "$TEMP/python/$target"
done
cp "$ROOT/go/Makefile" "$TEMP/go/"
cp "$ROOT/python/Makefile" "$TEMP/python/"
cp "$ROOT/dynamos.conf" "$TEMP/"
touch "$TEMP/go/Dockerfile" "$TEMP/go/go.mod" "$TEMP/go/go.sum" \
    "$TEMP/python/Dockerfile" "$TEMP/python/dynamos-python-lib/dist/dynamos.whl"
for command in go protoc python3; do
    printf '#!/bin/bash\nexit 0\n' > "$TEMP/bin/$command"
done
cat > "$TEMP/bin/docker" <<'STUB'
#!/bin/bash
echo "$*" >> "$BUILD_LOG"
if [ "$1" = build ] && [ "${FAIL_BUILD:-0}" = 1 ]; then exit 31; fi
if [ "$1" = push ] && [ "${FAIL_PUSH:-0}" = 1 ]; then exit 32; fi
STUB
chmod +x "$TEMP/bin/"*
export PATH="$TEMP/bin:$PATH"

for project in go python; do
    target=api-gateway
    context="$TEMP/go/cmd/$target"
    if [ "$project" = python ]; then
        target=vfl-party
        context="$TEMP/python/$target"
    fi
    image="dynamos1/$target"
    for mode in default both local; do
        : > "$BUILD_LOG"
        flags=()
        if [ "$mode" = both ]; then flags+=(BUILD_BOTH=1); fi
        if [ "$mode" = local ]; then flags+=(PUSH=0); fi
        make -C "$TEMP/$project" "$target" branch_name=fed-encrypt-dynamic "${flags[@]}" > "$TEMP/output" 2>&1
        grep -q "build .* -t $image:fed-encrypt-dynamic" "$BUILD_LOG"
        if [ "$mode" = both ]; then
            grep -q "build .* -t $image:latest" "$BUILD_LOG"
            grep -q "^push $image:latest$" "$BUILD_LOG"
        else
            if grep -q ':latest' "$BUILD_LOG"; then exit 1; fi
        fi
        if [ "$mode" = local ]; then
            if grep -q '^push ' "$BUILD_LOG"; then exit 1; fi
        else
            grep -q "^push $image:fed-encrypt-dynamic$" "$BUILD_LOG"
        fi
        test ! -f "$context/Dockerfile"
        test ! -d "$context/pkg"
        test ! -d "$context/protofiles"
        test ! -f "$context/dynamos.whl"
    done
    for both in 0 1; do
        : > "$BUILD_LOG"
        if FAIL_BUILD=1 make -C "$TEMP/$project" "$target" branch_name=fed-encrypt-dynamic BUILD_BOTH="$both" > "$TEMP/output" 2>&1; then
            echo "$project hid build failure" >&2
            exit 1
        fi
        if grep -q '^push ' "$BUILD_LOG"; then
            echo "$project pushed after failed build" >&2
            exit 1
        fi
        test ! -f "$context/Dockerfile"
    done
    : > "$BUILD_LOG"
    if FAIL_PUSH=1 make -C "$TEMP/$project" "$target" branch_name=fed-encrypt-dynamic BUILD_BOTH=1 > "$TEMP/output" 2>&1; then
        echo "$project hid push failure" >&2
        exit 1
    fi
    test "$(grep -c '^push ' "$BUILD_LOG")" = 1
    test ! -f "$context/Dockerfile"
done

: > "$BUILD_LOG"
make -C "$TEMP/python" vfl branch_name=fed-encrypt-dynamic PUSH=0 > "$TEMP/output" 2>&1
for target in vfl-party vfl-aggregator vfl-authority; do
    grep -q "build .* -t dynamos1/$target:fed-encrypt-dynamic" "$BUILD_LOG"
done
test "$(grep -c '^build ' "$BUILD_LOG")" = 3
if grep -q '^push ' "$BUILD_LOG"; then exit 1; fi
echo "Image build tests passed."

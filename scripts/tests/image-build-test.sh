#!/bin/bash

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP="$(mktemp -d)"
trap 'rm -rf "$TEMP"' EXIT
export BUILD_LOG="$TEMP/docker.log"
mkdir -p "$TEMP/bin" "$TEMP/go/cmd/api-gateway" "$TEMP/go/pkg" \
    "$TEMP/python/dynamos-python-lib/dist" \
    "$TEMP/python/dynamos-python-lib/protofiles" "$TEMP/python/vfl-train"
cp "$ROOT/go/Makefile" "$TEMP/go/"
cp "$ROOT/python/Makefile" "$TEMP/python/"
printf 'export DOCKERHUB_ACCOUNT=dynamos1\n' > "$TEMP/dynamos.conf"
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
        target=vfl-train
        context="$TEMP/python/$target"
    fi
    image="dynamos1/$target"
    for mode in default both local; do
        : > "$BUILD_LOG"
        flags=()
        if [ "$mode" = both ]; then flags+=(BUILD_BOTH=1); fi
        if [ "$mode" = local ]; then flags+=(PUSH=0); fi
        make -C "$TEMP/$project" "$target" branch_name=fed-bcd "${flags[@]}" > "$TEMP/output" 2>&1
        grep -q "build .* -t $image:fed-bcd" "$BUILD_LOG"
        if [ "$mode" = both ]; then
            grep -q "build .* -t $image:latest" "$BUILD_LOG"
            grep -q "^push $image:latest$" "$BUILD_LOG"
        else
            if grep -q ':latest' "$BUILD_LOG"; then exit 1; fi
        fi
        if [ "$mode" = local ]; then
            if grep -q '^push ' "$BUILD_LOG"; then exit 1; fi
        else
            grep -q "^push $image:fed-bcd$" "$BUILD_LOG"
        fi
        test ! -f "$context/Dockerfile"
    done
    for both in 0 1; do
        : > "$BUILD_LOG"
        if FAIL_BUILD=1 make -C "$TEMP/$project" "$target" branch_name=fed-bcd BUILD_BOTH="$both" > "$TEMP/output" 2>&1; then
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
    if FAIL_PUSH=1 make -C "$TEMP/$project" "$target" branch_name=fed-bcd BUILD_BOTH=1 > "$TEMP/output" 2>&1; then
        echo "$project hid push failure" >&2
        exit 1
    fi
    test "$(grep -c '^push ' "$BUILD_LOG")" = 1
    test ! -f "$context/Dockerfile"
done
echo "Image build tests passed."

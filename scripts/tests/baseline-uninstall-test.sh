#!/bin/bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP="$(mktemp -d)"
trap 'rm -rf "$TEMP"' EXIT
mkdir -p "${TEMP}/bin"
export UNINSTALL_LOG="${TEMP}/commands.log"
export DYNAMOS_ROOT="$ROOT"
export HELM_NAMESPACE=default

cat > "${TEMP}/bin/helm" <<'STUB'
#!/bin/bash
set -eu
echo "helm $*" >> "$UNINSTALL_LOG"
if [ "${FAIL_HELM:-0}" = 1 ]; then exit 23; fi
if [ "$1" = list ] && [ "${EMPTY_RELEASES:-0}" != 1 ]; then
    # Simulate the installed core releases and ingress, but no monitoring stack.
    case "$*" in
        *"--namespace default --filter ^core$"*) echo core ;;
        *"--namespace default --filter ^namespaces$"*) echo namespaces ;;
        *"--namespace ingress --filter ^nginx$"*) echo nginx ;;
    esac
fi
STUB
cat > "${TEMP}/bin/kubectl" <<'STUB'
#!/bin/bash
set -eu
echo "kubectl $*" >> "$UNINSTALL_LOG"
if [ "${FAIL_KUBECTL:-0}" = 1 ]; then exit 24; fi
if [ "$1" = get ] && [ "${EMPTY_RELEASES:-0}" != 1 ]; then
    echo "namespace/$3"
fi
STUB
chmod +x "${TEMP}/bin/"*
export PATH="${TEMP}/bin:${PATH}"

bash "${ROOT}/configuration/uninstall-dynamos.sh" > "${TEMP}/output"
grep -q 'helm uninstall core --namespace default --wait' "$UNINSTALL_LOG"
grep -q 'helm uninstall nginx --namespace ingress --wait' "$UNINSTALL_LOG"
grep -q 'helm uninstall namespaces --namespace default --wait' "$UNINSTALL_LOG"
test "$(tail -n 1 "$UNINSTALL_LOG")" = 'helm uninstall namespaces --namespace default --wait'
if grep '^helm ' "$UNINSTALL_LOG" | grep -q -- '--ignore-not-found'; then
    echo "Unsupported Helm flag was used." >&2
    exit 1
fi

: > "$UNINSTALL_LOG"
EMPTY_RELEASES=1 bash "${ROOT}/configuration/uninstall-dynamos.sh" > "${TEMP}/output"
if grep -q 'helm uninstall\|kubectl delete' "$UNINSTALL_LOG"; then
    echo "Uninstaller did not skip absent releases/namespaces." >&2
    exit 1
fi

if FAIL_HELM=1 bash "${ROOT}/configuration/uninstall-dynamos.sh" > "${TEMP}/output" 2>&1; then
    echo "Uninstaller hid a Helm failure." >&2
    exit 1
fi
if FAIL_KUBECTL=1 bash "${ROOT}/configuration/uninstall-dynamos.sh" > "${TEMP}/output" 2>&1; then
    echo "Uninstaller hid a kubectl failure." >&2
    exit 1
fi

echo "Baseline uninstall tests passed."

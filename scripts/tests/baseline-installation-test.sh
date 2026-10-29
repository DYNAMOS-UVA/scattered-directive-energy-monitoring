#!/bin/bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP="$(mktemp -d)"
trap 'rm -rf "$TEMP"' EXIT

export DYNAMOS_ROOT="${TEMP}/test repository"
export INSTALL_LOG="${TEMP}/install.log"
unset DYNAMOS_IMAGE_TAG
unset SUDO
mkdir -p "${DYNAMOS_ROOT}/configuration/k8s_service_files" \
    "${DYNAMOS_ROOT}/configuration/etcd_launch_files" "${TEMP}/bin"
cp "${ROOT}/dynamos.conf" "${DYNAMOS_ROOT}/"
cp "${ROOT}/configuration/dynamos-configuration.sh" \
    "${ROOT}/configuration/dynamos-configuration-no-monitoring.sh" \
    "${DYNAMOS_ROOT}/configuration/"
printf '{"name": "server"}\n' > "${DYNAMOS_ROOT}/configuration/etcd_launch_files/agreements.json"
printf '{"password": "%%PASSWORD%%"}\n' > "${DYNAMOS_ROOT}/configuration/k8s_service_files/definitions_example.json"

for helper in configure_dynamos fill-rabbit-pvc fill-etcd-pvc dynamos-monitoring-configuration; do
    cat > "${DYNAMOS_ROOT}/configuration/${helper}.sh" <<'STUB'
#!/bin/bash
printf '%s %s\n' "$(basename "$0")" "$*" >> "$INSTALL_LOG"
STUB
done

cat > "${TEMP}/bin/helm" <<'STUB'
#!/bin/bash
printf 'helm %s\n' "$*" >> "$INSTALL_LOG"
if [ "${FAIL_HELM:-0}" = 1 ]; then exit 23; fi
exit 0
STUB
cat > "${TEMP}/bin/docker" <<'STUB'
#!/bin/bash
printf 'Hashing password\nfake-hash\n'
STUB
cat > "${TEMP}/bin/openssl" <<'STUB'
#!/bin/bash
printf 'fake-password\n'
STUB
cat > "${TEMP}/bin/sleep" <<'STUB'
#!/bin/bash
exit 0
STUB
chmod +x "${TEMP}/bin/"*
export PATH="${TEMP}/bin:${PATH}"

if grep -q '^kind: Job' "${ROOT}/charts/namespaces/templates/etcd-pvc.yaml"; then
    echo "Local etcd PVC chart must not include the obsolete initialization Job." >&2
    exit 1
fi

: > "$INSTALL_LOG"
if DYNAMOS_ROOT="${TEMP}/stale checkout" bash "${DYNAMOS_ROOT}/configuration/dynamos-configuration-no-monitoring.sh" local > "${TEMP}/output" 2>&1; then
    echo "Stale repository root was accepted." >&2
    exit 1
fi
grep -q 'unset DYNAMOS_ROOT' "${TEMP}/output"
test ! -s "$INSTALL_LOG"

: > "$INSTALL_LOG"
(
    unset DYNAMOS_ROOT
    bash "${TEMP}/test repository/configuration/dynamos-configuration-no-monitoring.sh" local
) > "${TEMP}/output"
test "$(grep -c 'dockerArtifactAccount=dynamos1 --set-string branchNameTag=baseline' "$INSTALL_LOG")" = 4

for mode in local fabric; do
    : > "$INSTALL_LOG"
    bash "${DYNAMOS_ROOT}/configuration/dynamos-configuration-no-monitoring.sh" "$mode" > "${TEMP}/output"
    if grep -q 'dynamos-monitoring-configuration.sh' "$INSTALL_LOG"; then
        echo "No-monitoring installation invoked monitoring." >&2
        exit 1
    fi
    test "$(grep -c 'dockerArtifactAccount=dynamos1 --set-string branchNameTag=baseline' "$INSTALL_LOG")" = 4
    grep -q "fill-rabbit-pvc.sh ${mode}" "$INSTALL_LOG"
    grep -q 'helm upgrade -i .* nginx ' "$INSTALL_LOG"
    if [ "$mode" = local ]; then
        grep -q 'fill-etcd-pvc.sh' "$INSTALL_LOG"
    else
        grep -q 'fabric/charts/orchestrator' "$INSTALL_LOG"
        if grep -q 'fill-etcd-pvc.sh' "$INSTALL_LOG"; then exit 1; fi
    fi
done

: > "$INSTALL_LOG"
bash "${DYNAMOS_ROOT}/configuration/dynamos-configuration.sh" local > "${TEMP}/output"
grep -q 'dynamos-monitoring-configuration.sh local' "$INSTALL_LOG"
test "$(grep -c 'dockerArtifactAccount=dynamos1 --set-string branchNameTag=baseline' "$INSTALL_LOG")" = 4

: > "$INSTALL_LOG"
DYNAMOS_IMAGE_TAG=custom bash "${DYNAMOS_ROOT}/configuration/dynamos-configuration-no-monitoring.sh" local > "${TEMP}/output"
test "$(grep -c 'branchNameTag=custom' "$INSTALL_LOG")" = 4

for argument in invalid ""; do
    : > "$INSTALL_LOG"
    if bash "${DYNAMOS_ROOT}/configuration/dynamos-configuration-no-monitoring.sh" "$argument" > "${TEMP}/output" 2>&1; then
        echo "Invalid environment was accepted." >&2
        exit 1
    fi
    test ! -s "$INSTALL_LOG"
done

if FAIL_HELM=1 bash "${DYNAMOS_ROOT}/configuration/dynamos-configuration-no-monitoring.sh" local > "${TEMP}/output" 2>&1; then
    echo "Installation hid a Helm failure." >&2
    exit 1
fi

echo "Baseline installation tests passed."

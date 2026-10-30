#!/bin/bash

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP="$(mktemp -d)"
trap 'rm -rf "$TEMP"' EXIT
export DYNAMOS_ROOT="${TEMP}/checkout"
export COMMAND_LOG="${TEMP}/commands.log"
mkdir -p "$DYNAMOS_ROOT/configuration/etcd_launch_files" \
    "$DYNAMOS_ROOT/configuration/k8s_service_files" \
    "$DYNAMOS_ROOT/fabric/node_scripts" "$TEMP/bin"
cp "$ROOT/dynamos.conf" "$DYNAMOS_ROOT/"
cp "$ROOT/configuration/dynamos-configuration.sh" \
    "$ROOT/configuration/dynamos-configuration-no-monitoring.sh" \
    "$ROOT/configuration/uninstall-dynamos.sh" "$DYNAMOS_ROOT/configuration/"
printf '{"name": "server"}\n' > "$DYNAMOS_ROOT/configuration/etcd_launch_files/agreements.json"
printf '{"password": "%%PASSWORD%%"}\n' > "$DYNAMOS_ROOT/configuration/k8s_service_files/definitions_example.json"
for helper in fill-rabbit-pvc fill-etcd-pvc dynamos-monitoring-configuration; do
    printf '#!/bin/bash\necho "%s $*" >> "$COMMAND_LOG"\n' "$helper" \
        > "$DYNAMOS_ROOT/configuration/$helper.sh"
done
printf '#!/bin/bash\necho "configure $*" >> "$COMMAND_LOG"\n' \
    > "$DYNAMOS_ROOT/fabric/node_scripts/configure_dynamos.sh"
cat > "$TEMP/bin/helm" <<'STUB'
#!/bin/bash
echo "helm $*" >> "$COMMAND_LOG"
if [ "${FAIL_HELM:-0}" = 1 ]; then exit 23; fi
if [ "$1" = list ] && [ "${EMPTY:-0}" != 1 ]; then
    echo installed
fi
STUB
cat > "$TEMP/bin/kubectl" <<'STUB'
#!/bin/bash
echo "kubectl $*" >> "$COMMAND_LOG"
if [ "${FAIL_KUBECTL:-0}" = 1 ]; then exit 24; fi
if [ "$1" = get ] && [ "${EMPTY:-0}" != 1 ]; then echo namespace/server; fi
STUB
printf '#!/bin/bash\nprintf "Hashing password\\nfake-hash\\n"\n' > "$TEMP/bin/docker"
printf '#!/bin/bash\necho fake-password\n' > "$TEMP/bin/openssl"
printf '#!/bin/bash\nexit 0\n' > "$TEMP/bin/sleep"
chmod +x "$TEMP/bin/"*
export PATH="$TEMP/bin:$PATH"
unset DYNAMOS_IMAGE_TAG SUDO

for mode in local fabric; do
    : > "$COMMAND_LOG"
    bash "$DYNAMOS_ROOT/configuration/dynamos-configuration-no-monitoring.sh" "$mode" > "$TEMP/output"
    test "$(grep -c 'dockerArtifactAccount=dynamos1 --set-string branchNameTag=semi-overlap-fed-bcd' "$COMMAND_LOG")" = 4
    if grep -q dynamos-monitoring-configuration "$COMMAND_LOG"; then exit 1; fi
    grep -q 'helm upgrade -i .* nginx ' "$COMMAND_LOG"
    grep -q "configure server  $mode" "$COMMAND_LOG"
    if [ "$mode" = local ]; then
        grep -q fill-etcd-pvc "$COMMAND_LOG"
    else
        if grep -q fill-etcd-pvc "$COMMAND_LOG"; then exit 1; fi
    fi
done

: > "$COMMAND_LOG"
bash "$DYNAMOS_ROOT/configuration/dynamos-configuration.sh" local > "$TEMP/output"
grep -q dynamos-monitoring-configuration "$COMMAND_LOG"
: > "$COMMAND_LOG"
DYNAMOS_IMAGE_TAG=custom bash "$DYNAMOS_ROOT/configuration/dynamos-configuration-no-monitoring.sh" local > "$TEMP/output"
test "$(grep -c branchNameTag=custom "$COMMAND_LOG")" = 4
if DYNAMOS_ROOT="$TEMP/missing" bash "$DYNAMOS_ROOT/configuration/dynamos-configuration.sh" local > "$TEMP/output" 2>&1; then exit 1; fi
grep -q 'unset DYNAMOS_ROOT' "$TEMP/output"
if bash "$DYNAMOS_ROOT/configuration/dynamos-configuration-no-monitoring.sh" invalid > "$TEMP/output" 2>&1; then exit 1; fi
if FAIL_HELM=1 bash "$DYNAMOS_ROOT/configuration/dynamos-configuration.sh" local > "$TEMP/output" 2>&1; then exit 1; fi

: > "$COMMAND_LOG"
bash "$DYNAMOS_ROOT/configuration/uninstall-dynamos.sh" > "$TEMP/output"
test "$(tail -n 1 "$COMMAND_LOG")" = 'helm uninstall namespaces --namespace default --wait'
grep -q 'kubectl delete jobs --all --namespace server' "$COMMAND_LOG"
if grep '^helm ' "$COMMAND_LOG" | grep -q -- --ignore-not-found; then exit 1; fi
: > "$COMMAND_LOG"
EMPTY=1 bash "$DYNAMOS_ROOT/configuration/uninstall-dynamos.sh" > "$TEMP/output"
if grep -q 'helm uninstall\|kubectl delete' "$COMMAND_LOG"; then exit 1; fi
if FAIL_HELM=1 bash "$DYNAMOS_ROOT/configuration/uninstall-dynamos.sh" > "$TEMP/output" 2>&1; then exit 1; fi
if FAIL_KUBECTL=1 bash "$DYNAMOS_ROOT/configuration/uninstall-dynamos.sh" > "$TEMP/output" 2>&1; then exit 1; fi
if grep -q '^kind: Job' "$ROOT/charts/namespaces/templates/etcd-pvc.yaml"; then exit 1; fi

(
    unset DYNAMOS_ROOT
    source "$ROOT/dynamos.conf"
    test "$DYNAMOS_ROOT" = "$ROOT"
    test "$DYNAMOS_IMAGE_TAG" = semi-overlap-fed-bcd
)
echo "Deployment tests passed."

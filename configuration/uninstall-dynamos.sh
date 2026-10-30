#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
source "${SCRIPT_DIR}/../dynamos.conf"

agreements="${CONFIG_PATH}/etcd_launch_files/agreements.json"
if [ ! -f "$agreements" ]; then
    echo "ERROR: Agreements file not found: ${agreements}. Check DYNAMOS_ROOT." >&2
    exit 1
fi

uninstall_release() {
    local release="$1"
    local namespace="$2"
    local found
    found=$(helm list --all --short --namespace "$namespace" --filter "^${release}$")
    if [ -n "$found" ]; then
        helm uninstall "$release" --namespace "$namespace" --wait
    else
        echo "Skipping ${namespace}/${release}: release not installed."
    fi
}

echo -e "\n=============== Started uninstalling DYNAMOS ===============\n"
echo "WARNING: This removes DYNAMOS releases, participant Jobs and chart-managed storage."

agents=$(grep '"name":' "$agreements" | awk -F'"' '{print $4}' | sort -u)
while IFS= read -r agent; do
    if [ -z "$agent" ]; then
        continue
    fi
    namespace=$(kubectl get namespace "$agent" --ignore-not-found -o name)
    if [ -n "$namespace" ]; then
        kubectl delete jobs --all --namespace "$agent" --wait=true
    fi
done <<< "$agents"

for release in monitoring kepler prometheus; do
    uninstall_release "$release" monitoring
done

release_namespace="${HELM_NAMESPACE:-default}"
for release in api-gateway surf thirdparties agents orchestrator core; do
    uninstall_release "$release" "$release_namespace"
done

uninstall_release nginx ingress
if [ "$release_namespace" != ingress ]; then
    uninstall_release nginx "$release_namespace"
fi

uninstall_release namespaces "$release_namespace"
echo -e "\n=============== Finished uninstalling DYNAMOS ===============\n"

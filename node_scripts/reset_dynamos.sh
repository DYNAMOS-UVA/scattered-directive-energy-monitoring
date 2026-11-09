#!/bin/bash
# Removes whichever DYNAMOS method is installed, independent of the branch that installed it.
# Run on the control VM. Host data on the dynamos node is wiped separately by the notebook.
set -uo pipefail

release_namespace="${HELM_NAMESPACE:-default}"
failures=0

uninstall_release() {
    local release="$1" namespace="$2"
    if helm status "$release" --namespace "$namespace" > /dev/null 2>&1; then
        echo "Uninstalling ${namespace}/${release}..."
        if ! helm uninstall "$release" --namespace "$namespace" --wait --timeout 10m; then
            echo "WARNING: failed to uninstall ${namespace}/${release}" >&2
            failures=$((failures + 1))
        fi
    else
        echo "Skipping ${namespace}/${release}: not installed."
    fi
}

# Namespaces created by the DYNAMOS 'namespaces' release (core, orchestrator, agents, ...).
dynamos_namespaces=""
if helm status namespaces --namespace "$release_namespace" > /dev/null 2>&1; then
    dynamos_namespaces=$(helm get manifest namespaces --namespace "$release_namespace" \
        | awk '/^kind: Namespace/ {found=1; next} found && /^  name:/ {print $2; found=0}')
fi
echo "DYNAMOS namespaces: ${dynamos_namespaces:-none}"

for namespace in $dynamos_namespaces; do
    if kubectl get namespace "$namespace" > /dev/null 2>&1; then
        kubectl delete jobs --all --namespace "$namespace" --wait=true --timeout=5m || failures=$((failures + 1))
    fi
done

for release in monitoring kepler prometheus; do
    uninstall_release "$release" monitoring
done
for release in api-gateway surf thirdparties agents orchestrator core; do
    uninstall_release "$release" "$release_namespace"
done
uninstall_release nginx ingress
uninstall_release namespaces "$release_namespace"

for namespace in $dynamos_namespaces; do
    if kubectl get namespace "$namespace" > /dev/null 2>&1; then
        echo "Waiting for namespace ${namespace} to be deleted..."
        kubectl wait --for=delete "namespace/${namespace}" --timeout=10m || failures=$((failures + 1))
    fi
done

for pv in rabbit-pv etcd-pv core-etcd-pv-0 core-etcd-pv-1 core-etcd-pv-2; do
    if kubectl get pv "$pv" > /dev/null 2>&1; then
        echo "Deleting leftover PersistentVolume ${pv}..."
        kubectl delete pv "$pv" --wait=false
        sleep 5
        if kubectl get pv "$pv" > /dev/null 2>&1; then
            kubectl patch pv "$pv" -p '{"metadata":{"finalizers":null}}'
        fi
    fi
done

echo -e "\nRemaining Helm releases:"
helm list --all-namespaces
if [ "$failures" -gt 0 ]; then
    echo "Reset finished with ${failures} failure(s)." >&2
    exit 1
fi
echo "Reset finished."

#!/bin/bash
# Removes whichever DYNAMOS method is installed, independent of the branch that installed it.
# Run on the control VM. Host data on the dynamos node is wiped separately by the notebook.
set -uo pipefail

release_namespace="${HELM_NAMESPACE:-default}"
failures=0

uninstall_release() {
    local release="$1" namespace="$2" wait="${3:-true}"
    if helm status "$release" --namespace "$namespace" > /dev/null 2>&1; then
        echo "Uninstalling ${namespace}/${release}..."
        local wait_args=()
        if [ "$wait" = true ]; then
            wait_args=(--wait --timeout 10m)
        fi
        if ! helm uninstall "$release" --namespace "$namespace" "${wait_args[@]}"; then
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
if [ -z "$dynamos_namespaces" ]; then
    # Release already gone (e.g. an interrupted reset): fall back to the union of all methods' namespaces.
    dynamos_namespaces="core orchestrator uva vu surf ingress api-gateway server clientone clienttwo clientthree aggregator authority"
fi
echo "DYNAMOS namespaces:" $dynamos_namespaces

for namespace in $dynamos_namespaces; do
    if kubectl get namespace "$namespace" > /dev/null 2>&1; then
        kubectl delete jobs --all --namespace "$namespace" --wait=true --timeout=5m || failures=$((failures + 1))
    fi
done

for release in monitoring kepler prometheus; do
    uninstall_release "$release" monitoring
done
for release in api-gateway surf thirdparties agents orchestrator; do
    uninstall_release "$release" "$release_namespace"
done
# Core owns the etcd PVs, which remain protected until their namespace PVCs are
# removed below. Let the uninstall proceed asynchronously to avoid waiting on them here.
uninstall_release core "$release_namespace" false
uninstall_release nginx ingress

# Deleting the namespaces release removes the PVCs/PVs. Don't use --wait here: a pod left
# stuck in Terminating (e.g. after a kubelet outage) keeps the PVC protection finalizer forever.
if helm status namespaces --namespace "$release_namespace" > /dev/null 2>&1; then
    echo "Uninstalling ${release_namespace}/namespaces..."
    helm uninstall namespaces --namespace "$release_namespace" \
        || echo "WARNING: helm uninstall namespaces reported an error; cleaning up manually." >&2
else
    echo "Skipping ${release_namespace}/namespaces: not installed."
fi

for namespace in $dynamos_namespaces; do
    kubectl get namespace "$namespace" > /dev/null 2>&1 || continue
    kubectl delete namespace "$namespace" --wait=false > /dev/null 2>&1
    if ! kubectl wait --for=delete "namespace/${namespace}" --timeout=60s > /dev/null 2>&1; then
        echo "Namespace ${namespace} is stuck; force-deleting its pods and clearing PVC finalizers..."
        kubectl get pods,pvc --namespace "$namespace" --no-headers 2>/dev/null || true
        kubectl delete pods --all --namespace "$namespace" --grace-period=0 --force --wait=false 2>/dev/null || true
        for pvc in $(kubectl get pvc --namespace "$namespace" -o name 2>/dev/null); do
            kubectl patch "$pvc" --namespace "$namespace" --type=merge -p '{"metadata":{"finalizers":null}}' || true
        done
    fi
done

namespace_cleanup_failed=0
for namespace in $dynamos_namespaces; do
    if kubectl get namespace "$namespace" > /dev/null 2>&1; then
        echo "Waiting for namespace ${namespace} to be deleted..."
        if ! kubectl wait --for=delete "namespace/${namespace}" --timeout=3m; then
            echo "Namespace ${namespace} still exists. Remaining resources and conditions:" >&2
            kubectl api-resources --verbs=list --namespaced -o name 2>/dev/null \
                | xargs -r -n 1 kubectl get --show-kind --ignore-not-found --namespace "$namespace" 2>/dev/null >&2
            kubectl get namespace "$namespace" -o jsonpath='{.status.conditions}' >&2; echo >&2
            failures=$((failures + 1))
            namespace_cleanup_failed=1
        fi
    fi
done

if [ "$namespace_cleanup_failed" -eq 0 ]; then
    for pv in rabbit-pv etcd-pv core-etcd-pv-0 core-etcd-pv-1 core-etcd-pv-2; do
        if kubectl get pv "$pv" > /dev/null 2>&1; then
            echo "Deleting leftover PersistentVolume ${pv}..."
            kubectl delete pv "$pv" --wait=false
            sleep 5
            if kubectl get pv "$pv" > /dev/null 2>&1; then
                kubectl patch pv "$pv" --type=merge -p '{"metadata":{"finalizers":null}}'
            fi
        fi
    done
else
    echo "Skipping PersistentVolume cleanup because one or more namespaces are still terminating." >&2
fi

echo -e "\nRemaining Helm releases:"
helm list --all-namespaces
if [ "$failures" -gt 0 ]; then
    echo "Reset finished with ${failures} failure(s)." >&2
    exit 1
fi
echo "Reset finished."

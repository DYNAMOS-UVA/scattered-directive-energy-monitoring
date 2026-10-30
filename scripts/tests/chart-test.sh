#!/bin/bash

set -Eeuo pipefail
trap 'echo "Chart test failed at line $LINENO: $BASH_COMMAND" >&2' ERR
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HELM="${HELM:-helm}"
TEMP="$(mktemp -d)"
trap 'rm -rf "$TEMP"' EXIT
export DYNAMOS_ROOT="$TEMP/checkout"
mkdir -p "$DYNAMOS_ROOT/configuration" "$DYNAMOS_ROOT/scripts"
cp "$ROOT/dynamos.conf" "$DYNAMOS_ROOT/"
cp "$ROOT/configuration/configure_dynamos.sh" "$DYNAMOS_ROOT/configuration/"
cp "$ROOT/scripts/add_agent.sh" "$ROOT/scripts/add_namespace.sh" \
    "$ROOT/scripts/add_thirdparty.sh" "$DYNAMOS_ROOT/scripts/"
cp -r "$ROOT/charts" "$DYNAMOS_ROOT/charts"
mkdir -p "$DYNAMOS_ROOT/fabric"
cp -r "$ROOT/fabric/charts" "$DYNAMOS_ROOT/fabric/charts"

for mode in local fabric; do
    charts="$DYNAMOS_ROOT/charts"
    if [ "$mode" = fabric ]; then charts="$DYNAMOS_ROOT/fabric/charts"; fi
    find "$charts" -type f -print0 | sort -z | xargs -0 sha256sum > "$TEMP/before"
    # Existing templates, including encryption-specific storage, must survive.
    bash "$DYNAMOS_ROOT/configuration/configure_dynamos.sh" \
        "server,clientone,clienttwo,clientthree,aggregator,authority,testworker" "testttp" "$mode" > "$TEMP/generate"
    for participant in aggregator authority testworker; do
        test -f "$charts/agents/templates/$participant.yaml"
        grep -q "namespace: $participant" "$charts/agents/templates/cluster_role.yaml"
        grep -q "name: $participant" "$charts/namespaces/templates/namespaces.yaml"
    done
    while read -r hash path; do
        case "$path" in
            */cluster_role.yaml|*/namespaces.yaml) continue ;;
        esac
        test "$hash" = "$(sha256sum "$path" | cut -d' ' -f1)"
    done < "$TEMP/before"
    find "$charts" -type f -print0 | sort -z | xargs -0 sha256sum > "$TEMP/once"
    bash "$DYNAMOS_ROOT/configuration/configure_dynamos.sh" \
        "server,clientone,clienttwo,clientthree,aggregator,authority,testworker" "testttp" "$mode" > "$TEMP/generate"
    find "$charts" -type f -print0 | sort -z | xargs -0 sha256sum > "$TEMP/twice"
    cmp "$TEMP/once" "$TEMP/twice"

    for chart in namespaces agents orchestrator api-gateway thirdparty; do
        "$HELM" template verify "$charts/$chart" > "$TEMP/$chart.yaml"
        "$HELM" template verify "$charts/$chart" \
            --set-string dockerArtifactAccount=customrepo \
            --set-string branchNameTag=customtag > "$TEMP/$chart-custom.yaml"
    done
    for chart in agents orchestrator api-gateway thirdparty; do
        grep -q 'image: dynamos1/.*:fed-encrypt-dynamic' "$TEMP/$chart.yaml"
        grep -q 'image: customrepo/.*:customtag' "$TEMP/$chart-custom.yaml"
        if grep -q '^[[:space:]]*image: .*:latest' "$TEMP/$chart.yaml"; then exit 1; fi
    done
    for chart in agents thirdparty; do
        agents="$(grep -c '^[[:space:]]*image: .*/agent:' "$TEMP/$chart.yaml")"
        for key in MICROSERVICE_TAG MICROSERVICE_REPOSITORY_NAME SIDECAR_REPOSITORY_NAME; do
            test "$(grep -c "^[[:space:]]*- name: $key$" "$TEMP/$chart.yaml")" = "$agents"
        done
        test "$(grep -c '^[[:space:]]*value: "customtag"$' "$TEMP/$chart-custom.yaml")" = "$agents"
        test "$(grep -c '^[[:space:]]*value: "customrepo"$' "$TEMP/$chart-custom.yaml")" = "$((agents * 2))"
        if grep -q 'value: apipilikas' "$TEMP/$chart.yaml"; then exit 1; fi
    done
    for participant in aggregator authority; do
        grep -q "name: $participant" "$TEMP/agents.yaml"
        grep -q "name: $participant" "$TEMP/namespaces.yaml"
    done
    grep -q 'name: testttp' "$TEMP/thirdparty.yaml"
    grep -q 'namespace: testttp' "$charts/thirdparty/templates/cluster_role.yaml"
    if [ "$mode" = local ]; then
        if grep -q 'init-etcd-pvc' "$TEMP/namespaces.yaml"; then exit 1; fi
    else
        grep -q 'name: init-etcd-pvc' "$TEMP/namespaces.yaml"
        grep -q 'ls -la /etcd' "$TEMP/namespaces.yaml"
    fi
done
echo "Chart generation/rendering tests passed."

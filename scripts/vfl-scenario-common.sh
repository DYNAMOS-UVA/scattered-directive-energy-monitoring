#!/bin/bash
# Shared helpers for the VFL policy scenario scripts (vfl-scenario-*.sh).
# Source this file; do not execute it directly.
#
# Policy changes go through the orchestrator (PUT /api/v1/policyEnforcer/{steward}),
# never through etcdctl, so the scenarios exercise the real update path.

set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPTS_DIR}/.." && pwd)"
EFLINT_DIR="${REPO_ROOT}/configuration/eflint-models"

REQUESTOR="${REQUESTOR:-requestor}"
API_PORT="${API_PORT:-8080}"
ORCH_PORT="${ORCH_PORT:-18082}"
PE_PORT="${PE_PORT:-18083}"
CYCLES="${CYCLES:-5}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-5}"
MAX_POLLS="${MAX_POLLS:-120}"

API_BASE_URL="http://localhost:${API_PORT}/api/v1"
ORCH_BASE_URL="http://127.0.0.1:${ORCH_PORT}/api/v1"
PE_BASE_URL="http://127.0.0.1:${PE_PORT}/api/v1"

PORT_FORWARD_PIDS=()
MODIFIED_STEWARDS=()

log()  { echo -e "\n=== $* ==="; }
fail() { echo ">!< FAIL: $*"; exit 1; }

is_port_open() {
    timeout 1 bash -c "</dev/tcp/127.0.0.1/$1" >/dev/null 2>&1
}

# ensure_port_forward <namespace> <service> <local-port>
ensure_port_forward() {
    local ns="$1" svc="$2" port="$3"
    if is_port_open "$port"; then
        return
    fi
    echo "Starting port-forward ${ns}/${svc} on localhost:${port}..."
    kubectl -n "$ns" port-forward "svc/${svc}" "${port}:8080" >"/tmp/vfl-scenario-pf-${svc}.log" 2>&1 &
    PORT_FORWARD_PIDS+=("$!")
    sleep 3
    is_port_open "$port" || fail "port-forward for ${svc} did not open localhost:${port} (see /tmp/vfl-scenario-pf-${svc}.log)"
}

# put_policy <steward> <file> — returns non-zero unless the orchestrator answers 200.
put_policy() {
    local steward="$1" file="$2" code
    code=$(curl -sS -o /tmp/vfl-scenario-put.out -w '%{http_code}' -X PUT \
        "${ORCH_BASE_URL}/policyEnforcer/${steward}" \
        -H "Content-Type: text/plain" --data-binary "@${file}")
    echo "PUT policyEnforcer/${steward} -> HTTP ${code}: $(cat /tmp/vfl-scenario-put.out)"
    [ "$code" = "200" ]
}

# revoke_requestor <steward> — uploads the steward's model without the requestor's
# relation block. The whole block goes, not just has-relation, because every
# relation-allows-* fact is "Conditioned by has-relation" in 02_agreement_rules.
revoke_requestor() {
    local steward="$1"
    local source="${EFLINT_DIR}/${steward}.eflint"
    local revoked="/tmp/vfl-scenario-${steward}-revoked.eflint"

    [ -f "$source" ] || fail "missing model ${source}"
    grep -v "\"${REQUESTOR}\"" "$source" > "$revoked"

    log "Revoking ${REQUESTOR} at ${steward}"
    MODIFIED_STEWARDS+=("$steward")
    put_policy "$steward" "$revoked" || fail "policy update for ${steward} was rejected"
}

restore_policies() {
    local steward
    for steward in "${MODIFIED_STEWARDS[@]+"${MODIFIED_STEWARDS[@]}"}"; do
        log "Restoring original policy for ${steward}"
        put_policy "$steward" "${EFLINT_DIR}/${steward}.eflint" \
            || echo ">!< WARNING: restore of ${steward} failed — re-run: curl -X PUT ${ORCH_BASE_URL}/policyEnforcer/${steward} -H 'Content-Type: text/plain' --data-binary @${EFLINT_DIR}/${steward}.eflint"
    done
}

cleanup() {
    local exit_code=$?
    restore_policies
    local pid
    for pid in "${PORT_FORWARD_PIDS[@]+"${PORT_FORWARD_PIDS[@]}"}"; do
        kill "$pid" 2>/dev/null || true
    done
    exit "$exit_code"
}

setup() {
    trap cleanup EXIT INT TERM
    ensure_port_forward api-gateway api-gateway "$API_PORT"
    ensure_port_forward orchestrator orchestrator "$ORCH_PORT"
    ensure_port_forward orchestrator policy-enforcer "$PE_PORT"
}

# show_policy_verdict — prints which stewards the policy enforcer currently
# permits for the requestor, so a scenario failure can be traced to policy vs code.
show_policy_verdict() {
    log "Policy enforcer verdict for ${REQUESTOR}"
    curl -sS -X POST "${PE_BASE_URL}/policy-enforcer/validate" \
        -H "Content-Type: application/json" \
        --data-raw "{\"user\":{\"id\":\"GUID\",\"user_name\":\"${REQUESTOR}\"},\"data_providers\":[\"clientone\",\"clienttwo\",\"clientthree\",\"server\"]}" \
    | python3 -c 'import json, sys
d = json.load(sys.stdin)
print("request_approved:", d.get("request_approved"))
print("valid:  ", sorted((d.get("valid_dataproviders") or {}).keys()))
print("invalid:", sorted(d.get("invalid_dataproviders") or []))'
}

# run_vfl_request — submits a vflTrainModelRequest, polls until done/failed and
# writes the final status JSON to $FINAL_STATUS_FILE.
FINAL_STATUS_FILE="/tmp/vfl-scenario-final-status.json"

run_vfl_request() {
    log "Submitting vflTrainModelRequest (cycles=${CYCLES})"
    local response request_id status_response status poll

    response=$(curl -sS -X POST "${API_BASE_URL}/requestApproval" \
        -H "Host: api-gateway.api-gateway.svc.cluster.local" \
        -H "Content-Type: application/json" \
        --data-raw "{
            \"type\": \"vflTrainModelRequest\",
            \"user\": {\"id\": \"GUID\", \"userName\": \"${REQUESTOR}\"},
            \"dataProviders\": [\"clientone\", \"clienttwo\", \"clientthree\", \"server\"],
            \"data_request\": {
                \"type\": \"vflTrainModelRequest\",
                \"data\": {
                    \"learning_rate\": 0.1,
                    \"cycles\": ${CYCLES},
                    \"policy_removal\": -1,
                    \"policy_reintroduction\": -1,
                    \"training_backtrack\": 0,
                    \"communication_frequency\": 15,
                    \"sample_batch_size\": 256
                },
                \"requestMetadata\": {}
            }
        }")
    echo "Response: ${response}"

    request_id=$(python3 -c 'import json, sys
d = json.load(sys.stdin)
print(d.get("request_id") or "")' <<< "$response")
    [ -n "$request_id" ] || fail "no request_id in response (is another job still active?)"

    for (( poll=1; poll<=MAX_POLLS; poll++ )); do
        status_response=$(curl -sS "${API_BASE_URL}/getTrainingStatus?id=${request_id}" \
            -H "Host: api-gateway.api-gateway.svc.cluster.local")
        status=$(python3 -c 'import json, sys
try:
    print(json.load(sys.stdin).get("status", ""))
except json.JSONDecodeError:
    print("")' <<< "$status_response")
        echo "[${poll}/${MAX_POLLS}] status=${status}"

        if [ "$status" = "done" ] || [ "$status" = "failed" ]; then
            echo "$status_response" > "$FINAL_STATUS_FILE"
            return
        fi
        sleep "$POLL_INTERVAL_SECONDS"
    done
    fail "timed out waiting for request ${request_id}"
}

# summarize_result — prints status, rounds run and clients per round.
summarize_result() {
    log "Result"
    python3 -c 'import json, sys
d = json.load(open(sys.argv[1]))
results = d.get("results") or []
print("status:          ", d.get("status"))
print("rounds completed:", len(results))
print("clients per round:", [r.get("clients") for r in results])' "$FINAL_STATUS_FILE"
}

final_field() {
    python3 -c 'import json, sys
d = json.load(open(sys.argv[1]))
results = d.get("results") or []
field = sys.argv[2]
if field == "status":
    print(d.get("status"))
elif field == "rounds":
    print(len(results))
elif field == "client_counts":
    print(" ".join(sorted({str(r.get("clients")) for r in results})))' "$FINAL_STATUS_FILE" "$1"
}

#!/bin/bash

set -euo pipefail

API_PORT="${API_PORT:-8080}"
API_BASE_URL="${API_BASE_URL:-http://localhost:${API_PORT}/api/v1}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-5}"
MAX_POLLS="${MAX_POLLS:-120}"
CYCLES="${CYCLES:-10}"

PORT_FORWARD_PID=""

cleanup() {
    if [ -n "$PORT_FORWARD_PID" ]; then
        kill "$PORT_FORWARD_PID" 2>/dev/null || true
        wait "$PORT_FORWARD_PID" 2>/dev/null || true
    fi
}

trap cleanup EXIT INT TERM

is_api_port_open() {
    timeout 1 bash -c "</dev/tcp/127.0.0.1/${API_PORT}" >/dev/null 2>&1
}

if ! is_api_port_open; then
    echo "Starting api-gateway port-forward on localhost:${API_PORT}..."
    kubectl port-forward svc/api-gateway "${API_PORT}:8080" -n api-gateway >/tmp/dynamos-api-gateway-port-forward.log 2>&1 &
    PORT_FORWARD_PID=$!
    sleep 3

    if ! is_api_port_open; then
        echo ">!< ERROR: api-gateway port-forward did not open localhost:${API_PORT}."
        echo "Port-forward log: /tmp/dynamos-api-gateway-port-forward.log"
        exit 1
    fi
fi

echo "Sending vflTrainModelRequest with cycles=${CYCLES}..."

response=$(curl -sS -X POST "${API_BASE_URL}/requestApproval" \
    -H "Host: api-gateway.api-gateway.svc.cluster.local" \
    -H "Content-Type: application/json" \
    --data-raw "{
        \"type\": \"vflTrainModelRequest\",
        \"user\": {
            \"id\": \"GUID\",
            \"userName\": \"requestor\"
        },
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
data = json.load(sys.stdin)
print(data.get("request_id") or data.get("active_request_id") or "")' <<< "$response")

if [ -z "$request_id" ]; then
    echo ">!< ERROR: Response did not include request_id or active_request_id."
    exit 1
fi

echo "Request id: ${request_id}"
echo "Polling every ${POLL_INTERVAL_SECONDS}s, up to ${MAX_POLLS} times..."

for (( poll=1; poll<=MAX_POLLS; poll++ )); do
    status_response=$(curl -sS "${API_BASE_URL}/getTrainingStatus?id=${request_id}" \
        -H "Host: api-gateway.api-gateway.svc.cluster.local" \
        -H "Content-Type: application/json")

    status=$(python3 -c 'import json, sys
try:
    data = json.load(sys.stdin)
except json.JSONDecodeError:
    print("")
    raise SystemExit
print(data.get("status", ""))' <<< "$status_response")

    echo "[$poll/${MAX_POLLS}] ${status_response}"

    if [ "$status" = "done" ] || [ "$status" = "failed" ]; then
        exit 0
    fi

    sleep "$POLL_INTERVAL_SECONDS"
done

echo ">!< ERROR: Timed out waiting for request ${request_id}."
exit 1
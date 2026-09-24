#!/bin/bash

set -euo pipefail

API_BASE_URL="${API_BASE_URL:-http://localhost:8080/api/v1}"
POLL_SECONDS="${POLL_SECONDS:-5}"
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

if ! curl -fsS "$API_BASE_URL/getTrainingStatus?id=healthcheck" \
    -H "Host: api-gateway.api-gateway.svc.cluster.local" >/dev/null 2>&1; then
    echo "Starting API gateway port-forward on localhost:8080..."
    kubectl port-forward svc/api-gateway 8080:8080 -n api-gateway >/tmp/dynamos-api-gateway-port-forward.log 2>&1 &
    PORT_FORWARD_PID=$!
    sleep 4
fi

response=$(curl -fsS -X POST "$API_BASE_URL/requestApproval" \
    -H "Host: api-gateway.api-gateway.svc.cluster.local" \
    -H "Content-Type: application/json" \
    --data-raw "{
      \"type\": \"vflTrainModelRequest\",
      \"user\": {
        \"id\": \"GUID\",
        \"userName\": \"evangelos.pipilikas@student.uva.nl\"
      },
      \"dataProviders\": [\"clientone\", \"clienttwo\", \"clientthree\", \"server\"],
      \"data_request\": {
        \"type\": \"vflTrainModelRequest\",
        \"data\": {
          \"learning_rate\": 0.1,
          \"cycles\": $CYCLES,
          \"policy_removal\": -1,
          \"policy_reintroduction\": -1,
          \"training_backtrack\": 0,
          \"communication_frequency\": 15,
          \"sample_batch_size\": 256
        },
        \"requestMetadata\": {}
      }
    }")

echo "Request response: $response"

request_id=$(printf '%s' "$response" | python3 -c 'import json,sys; data=json.load(sys.stdin); print(data.get("request_id") or data.get("active_request_id") or "")')

if [ -z "$request_id" ]; then
    echo "Could not find request_id or active_request_id in response."
    exit 1
fi

echo "Polling request id: $request_id"

for poll in $(seq 1 "$MAX_POLLS"); do
    status_response=$(curl -fsS "$API_BASE_URL/getTrainingStatus?id=$request_id" \
        -H "Host: api-gateway.api-gateway.svc.cluster.local" \
        -H "Content-Type: application/json")
    echo "[$poll/$MAX_POLLS] $status_response"

    status=$(printf '%s' "$status_response" | python3 -c 'import json,sys; data=json.load(sys.stdin); print(data.get("status", ""))')
    if [ "$status" = "done" ] || [ "$status" = "failed" ]; then
        exit 0
    fi

    sleep "$POLL_SECONDS"
done

echo "Timed out waiting for request to finish."
exit 1
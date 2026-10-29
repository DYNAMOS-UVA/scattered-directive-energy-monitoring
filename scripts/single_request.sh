#!/bin/bash

set -euo pipefail

API_PORT="${API_PORT:-8080}"
API_BASE_URL="${API_BASE_URL:-http://localhost:${API_PORT}/api/v1}"
API_BASE_URL="${API_BASE_URL%/}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-5}"
MAX_POLLS="${MAX_POLLS:-120}"
CYCLES="${CYCLES:-10}"
COMMUNICATION_FREQUENCY="${COMMUNICATION_FREQUENCY:-10}"
SAMPLE_BATCH_SIZE="${SAMPLE_BATCH_SIZE:-64}"
OUTPUT_FILE="${OUTPUT_FILE:-fed-bcd-smoke-result.json}"

for setting in API_PORT MAX_POLLS CYCLES COMMUNICATION_FREQUENCY SAMPLE_BATCH_SIZE; do
    if ! [[ "${!setting}" =~ ^[1-9][0-9]*$ ]]; then
        echo "ERROR: ${setting} must be a positive integer." >&2
        exit 1
    fi
done
if (( COMMUNICATION_FREQUENCY < 2 || CYCLES < COMMUNICATION_FREQUENCY || CYCLES % COMMUNICATION_FREQUENCY != 0 )); then
    echo "ERROR: COMMUNICATION_FREQUENCY must be >= 2 and CYCLES must be a positive multiple of it." >&2
    exit 1
fi
if ! [[ "$POLL_INTERVAL_SECONDS" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
    echo "ERROR: POLL_INTERVAL_SECONDS must be a nonnegative number." >&2
    exit 1
fi

PORT_FORWARD_PID=""
PORT_FORWARD_LOG=""
cleanup() {
    if [ -n "$PORT_FORWARD_PID" ]; then
        if kill -0 "$PORT_FORWARD_PID" 2>/dev/null; then
            kill "$PORT_FORWARD_PID"
        fi
        if wait "$PORT_FORWARD_PID" 2>/dev/null; then :; fi
    fi
    if [ -n "$PORT_FORWARD_LOG" ]; then
        rm -f "$PORT_FORWARD_LOG"
    fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

api_ready() {
    curl -fsS --connect-timeout 2 --max-time 5 \
        "${API_BASE_URL}/getAvailableProviders" \
        -H "Host: api-gateway.api-gateway.svc.cluster.local" >/dev/null
}

if ! api_ready 2>/dev/null; then
    if [ "$API_BASE_URL" != "http://localhost:${API_PORT}/api/v1" ]; then
        echo "ERROR: API unavailable at ${API_BASE_URL}; check the configured endpoint." >&2
        exit 1
    fi
    PORT_FORWARD_LOG="$(mktemp)"
    echo "Starting API gateway port-forward on localhost:${API_PORT}..."
    kubectl port-forward svc/api-gateway "${API_PORT}:8080" -n api-gateway >"$PORT_FORWARD_LOG" 2>&1 &
    PORT_FORWARD_PID=$!
    ready=false
    for ((attempt=1; attempt<=15; attempt++)); do
        if ! kill -0 "$PORT_FORWARD_PID" 2>/dev/null; then break; fi
        if api_ready 2>/dev/null; then ready=true; break; fi
        sleep 1
    done
    if [ "$ready" != true ]; then
        echo "ERROR: API gateway port-forward did not become ready." >&2
        cat "$PORT_FORWARD_LOG" >&2
        exit 1
    fi
fi

echo "Submitting ${CYCLES} FedBCD local cycles (Q=${COMMUNICATION_FREQUENCY}, batch=${SAMPLE_BATCH_SIZE})..."
response=$(curl -fsS --connect-timeout 5 --max-time 30 \
    "${API_BASE_URL}/requestApproval" \
    -H "Host: api-gateway.api-gateway.svc.cluster.local" \
    -H "Content-Type: application/json" \
    --data-raw "{
      \"type\": \"vflTrainModelRequest\",
      \"user\": {\"id\": \"GUID\", \"userName\": \"evangelos.pipilikas@student.uva.nl\"},
      \"dataProviders\": [\"clientone\", \"clienttwo\", \"clientthree\", \"server\"],
      \"data_request\": {
        \"type\": \"vflTrainModelRequest\",
        \"data\": {
          \"learning_rate\": 0.1,
          \"cycles\": ${CYCLES},
          \"communication_frequency\": ${COMMUNICATION_FREQUENCY},
          \"sample_batch_size\": ${SAMPLE_BATCH_SIZE},
          \"policy_removal\": -1,
          \"policy_reintroduction\": -1,
          \"training_backtrack\": 0
        },
        \"requestMetadata\": {}
      }
    }")
echo "Response: ${response}"
request_id=$(python3 -c 'import json,sys
data=json.load(sys.stdin)
request_id=data.get("request_id")
if not isinstance(request_id,str) or not request_id:
    sys.exit("ERROR: Response did not include a valid request_id.")
print(request_id)' <<< "$response")

for ((poll=1; poll<=MAX_POLLS; poll++)); do
    status_response=$(curl -fsS --connect-timeout 5 --max-time 30 \
        "${API_BASE_URL}/getTrainingStatus?id=${request_id}" \
        -H "Host: api-gateway.api-gateway.svc.cluster.local")
    status=$(python3 -c 'import json,sys
status=json.load(sys.stdin).get("status")
if status not in ("pending","done","failed"):
    sys.exit("ERROR: Unexpected training status: " + repr(status))
print(status)' <<< "$status_response")
    echo "[${poll}/${MAX_POLLS}] ${status}"
    if [ "$status" = done ] || [ "$status" = failed ]; then
        python3 -m json.tool <<< "$status_response" > "$OUTPUT_FILE"
        echo "Result saved to ${OUTPUT_FILE}"
        if [ "$status" = failed ]; then
            echo "ERROR: Training failed. Inspect ${OUTPUT_FILE} and API gateway logs." >&2
            exit 1
        fi
        echo "Training completed successfully."
        exit 0
    fi
    if [ "$poll" -lt "$MAX_POLLS" ]; then sleep "$POLL_INTERVAL_SECONDS"; fi
done

echo "ERROR: Timed out waiting for request ${request_id}; the cluster run may still be active." >&2
exit 1

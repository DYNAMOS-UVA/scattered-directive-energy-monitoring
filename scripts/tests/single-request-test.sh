#!/bin/bash

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP="$(mktemp -d)"
trap 'rm -rf "$TEMP"' EXIT
mkdir -p "${TEMP}/bin"
export REQUEST_LOG="${TEMP}/requests.log"
export FORWARD_READY="${TEMP}/forward-ready"
export FORWARD_STOPPED="${TEMP}/forward-stopped"
export OUTPUT_FILE="${TEMP}/result.json"
export API_BASE_URL=http://test.invalid/api/v1
export CYCLES=120 COMMUNICATION_FREQUENCY=10 SAMPLE_BATCH_SIZE=64 MAX_POLLS=2 POLL_INTERVAL_SECONDS=0

cat > "${TEMP}/bin/curl" <<'STUB'
#!/bin/bash
set -eu
echo "$*" >> "$REQUEST_LOG"
case "$*" in
    *getAvailableProviders*)
        if [ "${SCENARIO:-}" = unavailable ]; then exit 7; fi
        if [ "${SCENARIO:-}" = forward ] && [ ! -f "$FORWARD_READY" ]; then exit 7; fi
        echo '{}' ;;
    *requestApproval*)
        if [ "${SCENARIO:-}" = http-error ]; then exit 22; fi
        if [ "${SCENARIO:-}" = malformed ]; then echo 'not json'; else echo '{"request_id":"test-run","status":"pending"}'; fi ;;
    *getTrainingStatus*)
        case "${SCENARIO:-}" in
            failed) echo '{"status":"failed","results":[]}' ;;
            timeout) echo '{"status":"pending","results":[]}' ;;
            *) echo '{"status":"done","results":[{"train_round":0,"timing":{}}]}' ;;
        esac ;;
esac
STUB
cat > "${TEMP}/bin/kubectl" <<'STUB'
#!/bin/bash
set -eu
echo "kubectl $*" >> "$REQUEST_LOG"
trap 'touch "$FORWARD_STOPPED"; exit 0' TERM
touch "$FORWARD_READY"
while :; do sleep 0.1; done
STUB
chmod +x "${TEMP}/bin/curl" "${TEMP}/bin/kubectl"
export PATH="${TEMP}/bin:${PATH}"

bash "${ROOT}/scripts/single_request.sh" > "${TEMP}/output"
grep -q '"status": "done"' "$OUTPUT_FILE"
grep -q '"userName": "evangelos.pipilikas@student.uva.nl"' "$REQUEST_LOG"
grep -q '"cycles": 120' "$REQUEST_LOG"
grep -q '"communication_frequency": 10' "$REQUEST_LOG"
grep -q '"sample_batch_size": 64' "$REQUEST_LOG"
grep -q 'getTrainingStatus?id=test-run' "$REQUEST_LOG"

for scenario in failed timeout http-error malformed unavailable; do
    rm -f "$OUTPUT_FILE"
    if SCENARIO="$scenario" bash "${ROOT}/scripts/single_request.sh" > "${TEMP}/output" 2>&1; then
        echo "Scenario ${scenario} incorrectly succeeded." >&2
        exit 1
    fi
    if [ "$scenario" = failed ]; then
        grep -q '"status": "failed"' "$OUTPUT_FILE"
    else
        test ! -f "$OUTPUT_FILE"
    fi
done

API_BASE_URL=http://localhost:8080/api/v1 SCENARIO=forward \
    bash "${ROOT}/scripts/single_request.sh" > "${TEMP}/output"
test -f "$FORWARD_STOPPED"
grep -q 'kubectl port-forward svc/api-gateway 8080:8080 -n api-gateway' "$REQUEST_LOG"

if CYCLES=0 bash "${ROOT}/scripts/single_request.sh" > "${TEMP}/output" 2>&1; then
    echo "Invalid cycle count was accepted." >&2
    exit 1
fi
echo "Single request tests passed."

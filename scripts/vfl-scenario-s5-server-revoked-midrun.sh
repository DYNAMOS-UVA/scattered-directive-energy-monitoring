#!/bin/bash
# VFL scenario S5 — server permission revoked while training is running.
#
# Training starts with the server and all clients permitted. After
# REVOKE_AFTER_ROUNDS rounds the requestor's relation at the server is revoked
# through the orchestrator. Expected: the next scheduled policy check stops the
# whole run (status "failed" with a stop_reason), at most POLICY_CHECK_INTERVAL
# rounds after the revocation took effect. The server policy is restored on
# exit, even on failure.
#
# See docs/notes/VFL_EFLINT_POLICY_SCENARIOS.md.
#
# Usage:  bash scripts/vfl-scenario-s5-server-revoked-midrun.sh
#         POLICY_CHECK_INTERVAL=3 CYCLES=15 bash scripts/vfl-scenario-s5-server-revoked-midrun.sh
# Env:    REVOKE_AFTER_ROUNDS (default 3), POLICY_CHECK_INTERVAL (default 1),
#         CYCLES (default 10), REQUESTOR, API_PORT, ORCH_PORT, PE_PORT

CYCLES="${CYCLES:-10}"
# Rounds can be short; poll often so the revocation lands near the target round.
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-1}"
MAX_POLLS="${MAX_POLLS:-900}"

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/vfl-scenario-common.sh"

SCENARIO_NAME="S5"

REVOKE_AFTER_ROUNDS="${REVOKE_AFTER_ROUNDS:-3}"
[ "$POLICY_CHECK_INTERVAL" -ge 1 ] || fail "POLICY_CHECK_INTERVAL must be >= 1: with 0 there are no mid-run checks to stop the run"
[ "$REVOKE_AFTER_ROUNDS" -lt "$CYCLES" ] || fail "REVOKE_AFTER_ROUNDS must be smaller than CYCLES"

setup
show_policy_verdict

submit_vfl_request

log "Waiting until ${REVOKE_AFTER_ROUNDS} rounds have completed"
for (( poll=1; poll<=MAX_POLLS; poll++ )); do
    status_response=$(fetch_status)
    status=$(json_field "$status_response" status)
    rounds=$(json_field "$status_response" rounds)
    echo "[${poll}] status=${status} rounds=${rounds}"

    if [ "$status" = "done" ] || [ "$status" = "failed" ]; then
        fail "run finished (${status}) before the revocation point; lower REVOKE_AFTER_ROUNDS or raise CYCLES"
    fi
    [ "${rounds:-0}" -ge "$REVOKE_AFTER_ROUNDS" ] && break
    sleep "$POLL_INTERVAL_SECONDS"
done

revoke_requestor server
rounds_at_revoke=$(json_field "$(fetch_status)" rounds)
echo "Revocation effective after ${rounds_at_revoke} completed rounds."
show_policy_verdict

wait_for_vfl_request
summarize_result

status=$(final_field status)
rounds=$(final_field rounds)
stop_reason=$(final_field stop_reason)
latest_stop=$(( rounds_at_revoke + POLICY_CHECK_INTERVAL ))

[ "$status" = "failed" ]             || fail "expected status 'failed' (run stopped), got '${status}'"
[ -n "$stop_reason" ]                || fail "run failed but recorded no stop_reason; it may have failed for an unrelated reason"
[ "$rounds" -lt "$CYCLES" ]          || fail "all ${CYCLES} rounds ran; the revocation was never acted on"
[ "$rounds" -ge "$rounds_at_revoke" ] || fail "fewer rounds recorded (${rounds}) than before the revocation (${rounds_at_revoke})"
[ "$rounds" -le "$latest_stop" ]     || fail "stopped after ${rounds} rounds; expected at most ${latest_stop} (revoked at ${rounds_at_revoke}, interval ${POLICY_CHECK_INTERVAL})"

echo -e "\n>>> PASS: S5 — run stopped after ${rounds}/${CYCLES} rounds (revoked at ${rounds_at_revoke}, interval ${POLICY_CHECK_INTERVAL}): ${stop_reason}"

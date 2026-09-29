#!/bin/bash
# VFL scenario S6 — one client's permission revoked while training is running.
#
# Training starts with the server and all three clients permitted. After
# REVOKE_AFTER_ROUNDS rounds the requestor's relation at DROPPED_CLIENT is
# revoked. Expected: the run is NOT stopped; from the next scheduled policy
# check on it continues with two clients (server model resized 3 -> 2) and
# finishes all rounds. The client's policy is restored on exit.
#
# See docs/notes/VFL_EFLINT_POLICY_SCENARIOS.md.
#
# Usage:  bash scripts/vfl-scenario-s6-client-excluded-midrun.sh
#         DROPPED_CLIENT=clientone POLICY_CHECK_INTERVAL=2 bash scripts/vfl-scenario-s6-client-excluded-midrun.sh
# Env:    DROPPED_CLIENT (default clientthree), REVOKE_AFTER_ROUNDS (default 3),
#         POLICY_CHECK_INTERVAL (default 1), CYCLES (default 10),
#         TRAINING_BACKTRACK (default 0), REQUESTOR, API_PORT, ORCH_PORT, PE_PORT

CYCLES="${CYCLES:-10}"
# Rounds can be short; poll often so the revocation lands near the target round.
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-1}"
MAX_POLLS="${MAX_POLLS:-900}"

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/vfl-scenario-common.sh"

DROPPED_CLIENT="${DROPPED_CLIENT:-clientthree}"
REVOKE_AFTER_ROUNDS="${REVOKE_AFTER_ROUNDS:-3}"
case "$DROPPED_CLIENT" in
    clientone|clienttwo|clientthree) ;;
    *) fail "DROPPED_CLIENT must be clientone, clienttwo or clientthree" ;;
esac
[ "$POLICY_CHECK_INTERVAL" -ge 1 ] || fail "POLICY_CHECK_INTERVAL must be >= 1: with 0 there are no mid-run checks"
[ $(( REVOKE_AFTER_ROUNDS + POLICY_CHECK_INTERVAL )) -lt "$CYCLES" ] \
    || fail "CYCLES must exceed REVOKE_AFTER_ROUNDS + POLICY_CHECK_INTERVAL so rounds remain after the exclusion"

setup
show_policy_verdict

submit_vfl_request
wait_until_rounds "$REVOKE_AFTER_ROUNDS"

revoke_requestor "$DROPPED_CLIENT"
rounds_at_revoke=$(json_field "$(fetch_status)" rounds)
echo "Revocation effective after ${rounds_at_revoke} completed rounds."
show_policy_verdict

wait_for_vfl_request
summarize_result

status=$(final_field status)
rounds=$(final_field rounds)
client_runs=$(final_field client_runs)
three_client_rounds=$(final_field first_run_length)
latest_switch=$(( rounds_at_revoke + POLICY_CHECK_INTERVAL ))

[ "$status" = "done" ]          || fail "expected status 'done' (run continues without ${DROPPED_CLIENT}), got '${status}'"
[ "$rounds" = "$CYCLES" ]       || fail "expected ${CYCLES} rounds, got ${rounds}"
[ "$client_runs" = "3 2" ]      || fail "expected clients per round to go 3 -> 2, got: ${client_runs}"
[ "$three_client_rounds" -ge "$REVOKE_AFTER_ROUNDS" ] \
                                || fail "switched to 2 clients after ${three_client_rounds} rounds, before the revocation"
[ "$three_client_rounds" -le "$latest_switch" ] \
                                || fail "switched to 2 clients after ${three_client_rounds} rounds; expected at most ${latest_switch}"

echo -e "\n>>> PASS: S6 — ${DROPPED_CLIENT} excluded after ${three_client_rounds} rounds; finished ${rounds} rounds with 2 clients."

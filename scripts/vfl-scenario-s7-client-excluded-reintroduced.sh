#!/bin/bash
# VFL scenario S7 — a client is excluded mid-run and later reintroduced.
#
# Training starts with the server and all three clients permitted. After
# REVOKE_AFTER_ROUNDS rounds the requestor's relation at DROPPED_CLIENT is
# revoked; once TWO_CLIENT_ROUNDS rounds have trained with two clients it is
# restored. Expected: the run is never stopped, clients per round go 3 -> 2 -> 3,
# and all rounds complete. With TRAINING_BACKTRACK=1 the server reloads the
# 3-client model it saved when shrinking. The policy is restored on exit.
#
# See docs/notes/VFL_EFLINT_POLICY_SCENARIOS.md.
#
# Usage:  bash scripts/vfl-scenario-s7-client-excluded-reintroduced.sh
#         TRAINING_BACKTRACK=1 bash scripts/vfl-scenario-s7-client-excluded-reintroduced.sh
# Env:    DROPPED_CLIENT (default clientthree), REVOKE_AFTER_ROUNDS (default 3),
#         TWO_CLIENT_ROUNDS (default 3), POLICY_CHECK_INTERVAL (default 1),
#         CYCLES (default 15), TRAINING_BACKTRACK (default 0), REQUESTOR,
#         API_PORT, ORCH_PORT, PE_PORT

CYCLES="${CYCLES:-15}"
# Rounds can be short; poll often so policy changes land near the target rounds.
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-1}"
MAX_POLLS="${MAX_POLLS:-900}"

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/vfl-scenario-common.sh"

DROPPED_CLIENT="${DROPPED_CLIENT:-clientthree}"
REVOKE_AFTER_ROUNDS="${REVOKE_AFTER_ROUNDS:-3}"
TWO_CLIENT_ROUNDS="${TWO_CLIENT_ROUNDS:-3}"
case "$DROPPED_CLIENT" in
    clientone|clienttwo|clientthree) ;;
    *) fail "DROPPED_CLIENT must be clientone, clienttwo or clientthree" ;;
esac
[ "$POLICY_CHECK_INTERVAL" -ge 1 ] || fail "POLICY_CHECK_INTERVAL must be >= 1: with 0 there are no mid-run checks"
# Each policy change can take up to POLICY_CHECK_INTERVAL rounds to act on.
[ $(( REVOKE_AFTER_ROUNDS + TWO_CLIENT_ROUNDS + 2 * POLICY_CHECK_INTERVAL )) -lt "$CYCLES" ] \
    || fail "CYCLES too small: need more than REVOKE_AFTER_ROUNDS + TWO_CLIENT_ROUNDS + 2*POLICY_CHECK_INTERVAL"

setup
show_policy_verdict

submit_vfl_request
wait_until_rounds "$REVOKE_AFTER_ROUNDS"

revoke_requestor "$DROPPED_CLIENT"
show_policy_verdict

wait_until_client_count 2
rounds_with_two=$(json_field "$(fetch_status)" rounds)
wait_until_rounds $(( rounds_with_two + TWO_CLIENT_ROUNDS - 1 ))

restore_requestor "$DROPPED_CLIENT"
show_policy_verdict

wait_for_vfl_request
summarize_result

status=$(final_field status)
rounds=$(final_field rounds)
client_runs=$(final_field client_runs)

[ "$status" = "done" ]          || fail "expected status 'done', got '${status}'"
[ "$rounds" = "$CYCLES" ]       || fail "expected ${CYCLES} rounds, got ${rounds}"
[ "$client_runs" = "3 2 3" ]    || fail "expected clients per round to go 3 -> 2 -> 3, got: ${client_runs} (if it ends on 2, raise CYCLES)"

echo -e "\n>>> PASS: S7 — ${DROPPED_CLIENT} excluded and reintroduced; clients went ${client_runs} over ${rounds} rounds (training_backtrack=${TRAINING_BACKTRACK})."

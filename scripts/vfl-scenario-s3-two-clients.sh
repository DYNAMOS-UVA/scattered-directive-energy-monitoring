#!/bin/bash
# VFL scenario S3 — requestor permitted at the server and only two clients.
#
# Expected: the request is admitted and every round trains with exactly two
# clients (status "done"). The dropped client's policy is restored on exit,
# even on failure.
#
# See docs/notes/VFL_EFLINT_POLICY_SCENARIOS.md.
#
# Usage:  bash scripts/vfl-scenario-s3-two-clients.sh
#         DROPPED_CLIENT=clientone bash scripts/vfl-scenario-s3-two-clients.sh
# Env:    DROPPED_CLIENT (default clientthree), CYCLES, REQUESTOR, API_PORT,
#         ORCH_PORT, PE_PORT (see vfl-scenario-common.sh)

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/vfl-scenario-common.sh"

DROPPED_CLIENT="${DROPPED_CLIENT:-clientthree}"
case "$DROPPED_CLIENT" in
    clientone|clienttwo|clientthree) ;;
    *) fail "DROPPED_CLIENT must be clientone, clienttwo or clientthree" ;;
esac

setup

revoke_requestor "$DROPPED_CLIENT"
show_policy_verdict

run_vfl_request
summarize_result

status=$(final_field status)
rounds=$(final_field rounds)
client_counts=$(final_field client_counts)

[ "$status" = "done" ]       || fail "expected status 'done', got '${status}'"
[ "$rounds" = "$CYCLES" ]    || fail "expected ${CYCLES} rounds, got ${rounds}"
[ "$client_counts" = "2" ]   || fail "expected 2 clients in every round, got: ${client_counts}"

echo -e "\n>>> PASS: S3 — training ran ${rounds} rounds with 2 clients (${DROPPED_CLIENT} excluded)."

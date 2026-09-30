#!/bin/bash
# VFL scenario S4 — requestor permitted at the server and only ONE client.
#
# Expected: the request is admitted (one client satisfies "server mandatory,
# >=1 client") and every round trains with exactly one client (status "done").
# The two revoked clients' policies are restored on exit, even on failure.
#
# See docs/notes/VFL_EFLINT_POLICY_SCENARIOS.md.
#
# Usage:  bash scripts/vfl-scenario-s4-one-client.sh
#         KEPT_CLIENT=clienttwo bash scripts/vfl-scenario-s4-one-client.sh
# Env:    KEPT_CLIENT (default clientone), CYCLES, REQUESTOR, API_PORT,
#         ORCH_PORT, PE_PORT (see vfl-scenario-common.sh)

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/vfl-scenario-common.sh"


SCENARIO_NAME="S4"

KEPT_CLIENT="${KEPT_CLIENT:-clientone}"
case "$KEPT_CLIENT" in
    clientone|clienttwo|clientthree) ;;
    *) fail "KEPT_CLIENT must be clientone, clienttwo or clientthree" ;;
esac

setup

for client in clientone clienttwo clientthree; do
    if [ "$client" != "$KEPT_CLIENT" ]; then
        revoke_requestor "$client"
    fi
done
show_policy_verdict

run_vfl_request
summarize_result

status=$(final_field status)
rounds=$(final_field rounds)
client_counts=$(final_field client_counts)

[ "$status" = "done" ]       || fail "expected status 'done', got '${status}'"
[ "$rounds" = "$CYCLES" ]    || fail "expected ${CYCLES} rounds, got ${rounds}"
[ "$client_counts" = "1" ]   || fail "expected 1 client in every round, got: ${client_counts}"

echo -e "\n>>> PASS: S4 — training ran ${rounds} rounds with only ${KEPT_CLIENT}."

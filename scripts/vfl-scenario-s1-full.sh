#!/bin/bash
# VFL scenario S1 — full permission, normal execution.
#
# Expected: every round trains with all three clients (status "done"). Use
# POLICY_CHECK_INTERVAL to verify that spacing out the mid-run policy checks
# does not disturb a normal run.
#
# See docs/notes/VFL_EFLINT_POLICY_SCENARIOS.md.
#
# Usage:  bash scripts/vfl-scenario-s1-full.sh
#         POLICY_CHECK_INTERVAL=3 CYCLES=10 bash scripts/vfl-scenario-s1-full.sh
# Env:    POLICY_CHECK_INTERVAL (default 1, 0 = initial check only), CYCLES,
#         REQUESTOR, API_PORT, ORCH_PORT, PE_PORT (see vfl-scenario-common.sh)

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/vfl-scenario-common.sh"

SCENARIO_NAME="S1"

setup
show_policy_verdict

run_vfl_request
summarize_result

status=$(final_field status)
rounds=$(final_field rounds)
client_counts=$(final_field client_counts)

[ "$status" = "done" ]       || fail "expected status 'done', got '${status}'"
[ "$rounds" = "$CYCLES" ]    || fail "expected ${CYCLES} rounds, got ${rounds}"
[ "$client_counts" = "3" ]   || fail "expected 3 clients in every round, got: ${client_counts}"

echo -e "\n>>> PASS: S1 — ${rounds} rounds with all 3 clients (policy_check_interval=${POLICY_CHECK_INTERVAL})."

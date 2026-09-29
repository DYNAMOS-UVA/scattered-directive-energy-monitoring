#!/bin/bash
# VFL scenario S2 — requestor NOT permitted at the server.
#
# Expected: the request is rejected before training starts (status "failed",
# zero rounds). The original server policy is restored on exit, even on failure.
#
# See docs/notes/VFL_EFLINT_POLICY_SCENARIOS.md.
#
# Usage:  bash scripts/vfl-scenario-s2-server-denied.sh
# Env:    CYCLES, REQUESTOR, API_PORT, ORCH_PORT, PE_PORT (see vfl-scenario-common.sh)

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/vfl-scenario-common.sh"

setup

revoke_requestor server
show_policy_verdict

run_vfl_request
summarize_result

status=$(final_field status)
rounds=$(final_field rounds)

[ "$status" = "failed" ] || fail "expected status 'failed', got '${status}'"
[ "$rounds" = "0" ]      || fail "expected 0 training rounds, got ${rounds}"

echo -e "\n>>> PASS: S2 — request rejected because the server is not permitted."

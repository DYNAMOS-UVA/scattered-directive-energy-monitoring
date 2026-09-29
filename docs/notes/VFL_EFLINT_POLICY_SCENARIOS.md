# VFL × eFLINT Policy Enforcement — Target Scenarios

Defines the behaviour the VFL workflow must exhibit once the eFLINT policy
enforcer replaces the previous (v0) one, and records what already exists versus
what still has to be built.

Companion document: `EFLINT_ORCHESTRATOR_PORT_TRACKER.md` (the port itself).

---

## 1. Scope and assumptions

**Assumed granted, not under test for now.** The requester is assumed to hold
permission for the requested *archetype* and *dataset*. Only the
requester↔steward relation decides acceptance in the scenarios below. Making
archetype/dataset genuinely enforceable may require changes to the eFLINT
Layer-2 files; deliberately deferred.

**Actors.** One `server` steward and three client stewards (`clientone`,
`clienttwo`, `clientthree`). The requester is `requestor` in every eFLINT
model, in `agreements.json` and in the example request scripts.

---

## 2. Policy check points

| # | When | Purpose |
|---|---|---|
| **C1** | Before the job is composed | Is the VFL request admissible at all? |
| **C2** | After every `policy_check_interval` completed rounds | Is continuing still permitted, and with which clients? |

### Periodic check interval (C2)

Set per request in `data_request.data`, next to `cycles`:

```json
"data": { "cycles": 10, "policy_check_interval": 3, ... }
```

| Value | Behaviour |
|---|---|
| `1` (default) | check before every round from round 1 on |
| `N` | check before rounds `N, 2N, 3N, ...` — i.e. after every N completed rounds |
| `0` | no mid-run checks; only C1 |

Round 0 is never re-checked, since C1 just ran. The interval is recorded in the
run's `metadata.policy_check_interval`, so experiment results show which
setting produced them. A policy change therefore takes effect at most
`policy_check_interval` rounds after it lands.

**Why checks are expensive.** A C2 check re-runs the full approval path:
api-gateway → policy enforcer → orchestrator `handleRequestApproval`, which
composes the job again — a new job name plus a composition request to every
agent — on each check. The interval divides that cost. A lighter check that
only asks the policy enforcer (no re-composition) would be the structural fix.

When a check stops the run, the status is `failed` and `metadata` carries
`stop_reason` and `stopped_before_round`.

---

## 3. Acceptance rule

A VFL request is admissible iff:

```
permitted(server) AND count(permitted clients) >= 1
```

- The **server relation is mandatory**. Without it there is no label holder and
  no aggregation point, so training cannot proceed.
- **At least one client** must be permitted. Clients that are not permitted are
  excluded from the round; training continues with the remainder.

The same rule applies at C1 (admit/reject) and at C2 (continue/stop, and with
which client set).

---

## 4. Scenarios

### S1 — Full permission

**Policy:** requester permitted at `server` + `clientone` + `clienttwo` + `clientthree`.

**Expected:** request admitted; VFL runs with all three clients for every round;
server model input width stays `3 × intermediate_neurons`.

### S2 — Server not permitted

**Policy:** requester *not* permitted at `server` (client permissions irrelevant).

**Expected:** request **rejected at C1** — no composition request is sent, no
pods are started. If it happens mid-run at C2, training stops at that round and
reports that policy no longer allows continuation.

### S3 — Partial client permission

**Policy:** requester permitted at `server` + exactly two of the three clients.

**Expected:** request admitted; VFL runs with **two** clients. The excluded
client contributes no embeddings and the server model architecture shrinks to
`2 × intermediate_neurons` (`update_server_model_architecture` already handles
this).

### S4 — Minimal client permission

**Policy:** requester permitted at `server` + exactly **one** client.

**Expected:** request admitted — one client is the boundary case of
"≥ 1 client"; VFL runs with a single client and the server model shrinks to
`1 × intermediate_neurons`.

### S5 — Server permission revoked mid-run

**Policy:** starts as S1. After a few completed rounds the requestor's relation
at `server` is revoked (via `PUT /api/v1/policyEnforcer/server`).

**Expected:** the next scheduled C2 check fails the "server mandatory" rule and
the **whole run stops** — status `failed`, `metadata.stop_reason` set, and at
most `policy_check_interval` further rounds after the revocation.

### S6 — Client permission revoked mid-run (exclusion)

**Policy:** starts as S1. After a few completed rounds the requestor's relation
at **one client** is revoked.

**Expected:** the run is **not** stopped. From the next scheduled C2 check on it
continues with two clients: the api-gateway drops the client, and the server
model detects the smaller input and shrinks to `2 × intermediate_neurons`. All
rounds complete; clients per round go `3 → 2`.

### S7 — Client excluded, then reintroduced

**Policy:** as S6, then after a few 2-client rounds the client's relation is
restored.

**Expected:** clients per round go `3 → 2 → 3` and all rounds complete. The
reintroduced client's pod was never shut down, so it continues with its own
model state. The server model grows back to `3 × intermediate_neurons`: with
`training_backtrack = 1` it reloads the 3-client model it saved when shrinking,
otherwise it starts from fresh weights.

### Summary

| Scenario | server | clients permitted | C1 | Training | Script | Result |
|---|---|---|---|---|---|---|
| S1 | yes | 3 | admit | 3 clients | `vfl-scenario-s1-full.sh` | **pass** 2026-09-29 |
| S2 | **no** | any | **reject** | does not start | `vfl-scenario-s2-server-denied.sh` | **pass** 2026-09-29 |
| S3 | yes | 2 | admit | 2 clients | `vfl-scenario-s3-two-clients.sh` | **pass** 2026-09-29 |
| S4 | yes | 1 | admit | 1 client | `vfl-scenario-s4-one-client.sh` | not yet run |
| S5 | yes → **no** mid-run | 3 | admit | stops at next C2 | `vfl-scenario-s5-server-revoked-midrun.sh` | **pass** 2026-09-29 |
| S6 | yes | 3 → 2 mid-run | admit | 3 → 2 clients, completes | `vfl-scenario-s6-client-excluded-midrun.sh` | not yet run |
| S7 | yes | 3 → 2 → 3 mid-run | admit | 3 → 2 → 3 clients, completes | `vfl-scenario-s7-client-excluded-reintroduced.sh` | not yet run |

### Client ordering (fixed 2026-09-29, prerequisite for S6/S7)

`runVFLTrainingRound` built the server's embedding list and sent the gradients
back by iterating the `clients` **map** twice. Go randomises map iteration, so
embeddings reached the server in a different column order every round, and
gradient *i* could be sent to a different client than the one that produced
embedding *i*. This already hurt normal 3-client runs (silently worse
training), and it would break S7 backtracking, which relies on each client
keeping its column slot in the saved server model. Clients are now processed
in sorted name order for both directions, and a gradient-count mismatch returns
an error instead of panicking.

---

## 5. What already exists

Most of the machinery is in place. In
`go/cmd/api-gateway/requests.go`, `runVFLTraining`:

- **C2 is implemented** (`reverifyVFLPolicy`), running every
  `policy_check_interval` rounds (§2). It sends a
  `pb.RequestApproval{Type: "vflTrainModelRequest", DataProviders: …}` to
  `policyEnforcer-in` and blocks on the response channel.
- **Denial stops training:** a policy error or a failed "server mandatory,
  ≥1 client" rule records a `stop_reason` and ends the run.
- **Client exclusion is already policy-driven:** providers missing from the
  response are deleted from the `clients` map.
- **The server model already adapts** to a changing client count —
  `update_server_model_architecture` / `shrink_server_model` /
  `expand_server_model` in `python/vfl-train-model-demo/main.py`.

The policy enforcer itself already approves VFL requests (verified 2026-09-29):
`POST /api/v1/policy-enforcer/validate` for the requester over all four stewards
returns `request_approved: true` with `computeToData` for each.

---

## 6. What has to change

### 6.1 Replace the hardcoded policy change (the main piece)

`runVFLTraining` accepts `policy_removal` and `policy_reintroduction` round
numbers. On those rounds it sends:

```go
&pb.RequestApproval{Type: "policyRemoval", …}        // or "policyReintroduction"
```

which the **v0** enforcer handled in `cmd/policy-enforcer_v0/consume.go` via
`removePolicy()` / `reintroducePolicy()` — both of which are, in fact, entirely
commented out in `policy_update.go`.

The eFLINT enforcer only accepts `requestApproval`, `policyUpdate`,
`agreementUpdate` and `sharedRulesUpdate`, so these two message types now fall
through to **`unknown message type`**.

**Replacement:** drive the change through the orchestrator endpoint built in
step 3 of the port:

```
PUT /api/v1/policyEnforcer/{steward}
Content-Type: text/plain
<eFLINT model with the client's relation added or removed>
```

This is already implemented and verified end to end (T3/T5 in the tracker), and
it makes the policy change *real* rather than hardcoded.

### 6.2 Enforce "server mandatory, ≥1 client" — **done 2026-09-29, option (b)**

Implemented in the api-gateway as `vflPolicyAdmits`, applied at C1 (request
rejected, status `failed`, active-job slot released) and at C2 (training
stops). It also fixed a
pre-existing bug: a per-round denial only broke out of the `select`, so
training carried on. Option (a) is still the long-term home.

The two options considered:

- **(a) In eFLINT** — a Layer-2 rule making admissibility depend on the server
  relation. Most faithful to the policy-as-code goal, but needs the request
  context to distinguish the server steward from client stewards.
- **(b) In the orchestrator / api-gateway** — after validation, reject if
  `server ∉ ValidDataproviders` or if no client remains.

(b) is the pragmatic first step and is enough for S1–S3; (a) is the better
long-term home.

### 6.3 Reintroduction re-adds clients — **already implemented**

On closer reading, the C2 handler does both: it deletes providers missing from
the response, then re-adds any authorised non-server provider that is not in
`clients`. S6 needs no new code for this; it still needs testing.

### 6.4 eFLINT models for the VFL stewards — **done 2026-09-29**

`server.eflint` and `client{one,two,three}.eflint` created and switched to
`eflint` in `provider_configs.json`. Originals backed up in
`configuration/eflint-models/backup-2026-09-29/`. To produce S2–S4, remove the
requestor's **whole relation block** (every line containing `"requestor"`) from
`server.eflint` or from the relevant client models. Removing only `+has-relation` is not
enough: every `relation-allows-*` fact is `Conditioned by has-relation` in
`02_agreement_rules.eflint`, so leaving them would assert facts whose
condition fails.

### 6.5 Running the scenarios

S1 was confirmed with `scripts/single_request.sh` on 2026-09-29; S2 and S3 with
the scripts below on the same day.

```bash
bash scripts/vfl-scenario-s1-full.sh                         # normal run
POLICY_CHECK_INTERVAL=3 CYCLES=10 bash scripts/vfl-scenario-s1-full.sh
bash scripts/vfl-scenario-s2-server-denied.sh
bash scripts/vfl-scenario-s3-two-clients.sh                  # drops clientthree
DROPPED_CLIENT=clientone bash scripts/vfl-scenario-s3-two-clients.sh
bash scripts/vfl-scenario-s4-one-client.sh                   # keeps clientone
KEPT_CLIENT=clienttwo bash scripts/vfl-scenario-s4-one-client.sh
bash scripts/vfl-scenario-s5-server-revoked-midrun.sh        # revoke after 3 rounds
POLICY_CHECK_INTERVAL=3 CYCLES=15 REVOKE_AFTER_ROUNDS=4 \
  bash scripts/vfl-scenario-s5-server-revoked-midrun.sh
bash scripts/vfl-scenario-s6-client-excluded-midrun.sh       # drop clientthree after 3 rounds
bash scripts/vfl-scenario-s7-client-excluded-reintroduced.sh # 3 -> 2 -> 3
TRAINING_BACKTRACK=1 bash scripts/vfl-scenario-s7-client-excluded-reintroduced.sh
```

Each script revokes the requestor through
`PUT /api/v1/policyEnforcer/{steward}` (the real update path), prints the
policy enforcer's verdict, runs a `vflTrainModelRequest`, checks the outcome
(S1: `done` with 3 clients; S2: `failed` with 0 rounds; S3/S4: `done` with 2/1
clients every round; S5: `failed` with a `stop_reason`, within
`policy_check_interval` rounds of the revocation; S6: `done` with clients
`3 → 2`; S7: `done` with clients `3 → 2 → 3`) and
**always restores the original policy on exit**. The repo's `.eflint` files are
never modified — only the live copy in etcd. Shared logic lives in
`scripts/vfl-scenario-common.sh`. `CYCLES` defaults to 5 (10 for S5/S6, 15 for
S7), `POLICY_CHECK_INTERVAL` to 1 and `TRAINING_BACKTRACK` to 0.

If a script is killed without running its exit handler (e.g. `kill -9`), etcd
keeps the revoked policy; restore it with `GET /api/v1/updateEtc`.

---

## 7. Suggested order of work

Steps 1–6 are **done** (2026-09-29). S4, S6 and S7 are scripted and awaiting
their first run.

1. ~~**Baseline** — S1 with the new enforcer.~~ Done.
2. ~~**eFLINT models for `server` + the three clients** (§6.4).~~ Done.
3. ~~**Server-mandatory check** (§6.2).~~ Done.
4. ~~**S2/S3 by live policy change** through
   `PUT /api/v1/policyEnforcer/{steward}`.~~ Done, scripted.
5. ~~**Periodic C2 check with stoppage** (`policy_check_interval`, §2), S5.~~
   Done, scripted.
6. ~~**Client exclusion / reintroduction mid-run** (S6, S7), including the
   client-ordering fix.~~ Done, scripted.
7. **Lighter C2 check** — ask the policy enforcer only, without re-composing
   the job in the orchestrator (§2, "Why checks are expensive").
8. **Replace `policyRemoval`/`policyReintroduction`** (§6.1) with that PUT, so
   mid-run changes can be triggered from the training loop itself.

All code changes for steps 1–6 were in the api-gateway (admission check,
periodic check, client ordering); the orchestrator and the Python services
needed nothing beyond the port itself.

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
| **C2** | After **each training round** | Is continuing still permitted, and with which clients? |

C2's granularity is provisional — per-round checks can be slow, so this may
later move to every *n* rounds or to an event-driven trigger.

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

### Summary

| Scenario | server | clients permitted | C1 | Training | Script | Result |
|---|---|---|---|---|---|---|
| S1 | yes | 3 | admit | 3 clients | `single_request.sh` | **pass** 2026-09-29 |
| S2 | **no** | any | **reject** | does not start | `vfl-scenario-s2-server-denied.sh` | **pass** 2026-09-29 |
| S3 | yes | 2 | admit | 2 clients | `vfl-scenario-s3-two-clients.sh` | **pass** 2026-09-29 |
| S4 | yes | 1 | admit | 1 client | `vfl-scenario-s4-one-client.sh` | not yet run |

### Future — S5, dynamic policy change mid-run

A policy change lands *while* training is running; at the next C2 the client set
changes and the VFL training configuration adapts in real time (client dropped
or reintroduced, server architecture resized, optionally backtracking to a saved
checkpoint). Not in scope yet, but S1–S4 are deliberately shaped so that S5 is
just "S1 → S3 → S1 without restarting".

---

## 5. What already exists

Most of the machinery is in place. In
`go/cmd/api-gateway/requests.go`, `runVFLTraining`:

- **C2 is already implemented.** Every round sends a
  `pb.RequestApproval{Type: "vflTrainModelRequest", DataProviders: …}` to
  `policyEnforcer-in` and blocks on the response channel.
- **Denial already stops training:** if `msg.Error != ""` it logs
  *"Policy does not allow this training to continue"* and breaks the loop.
- **Client exclusion is already policy-driven:** when
  `len(msg.AuthorizedProviders) != len(authorizedProviders)`, providers missing
  from the response are deleted from the `clients` map.
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
`clients`. S5 needs no new code for this; it still needs testing.

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
bash scripts/vfl-scenario-s2-server-denied.sh
bash scripts/vfl-scenario-s3-two-clients.sh                  # drops clientthree
DROPPED_CLIENT=clientone bash scripts/vfl-scenario-s3-two-clients.sh
bash scripts/vfl-scenario-s4-one-client.sh                   # keeps clientone
KEPT_CLIENT=clienttwo bash scripts/vfl-scenario-s4-one-client.sh
```

Each script revokes the requestor through
`PUT /api/v1/policyEnforcer/{steward}` (the real update path), prints the
policy enforcer's verdict, runs a `vflTrainModelRequest`, checks the outcome
(S2: `failed` with 0 rounds; S3/S4: `done` with 2/1 clients every round) and
**always restores the original policy on exit**. The repo's `.eflint` files are
never modified — only the live copy in etcd. Shared logic lives in
`scripts/vfl-scenario-common.sh`. `CYCLES` defaults to 5.

If a script is killed without running its exit handler (e.g. `kill -9`), etcd
keeps the revoked policy; restore it with `GET /api/v1/updateEtc`.

---

## 7. Suggested order of work

Steps 1–4 are **done** (2026-09-29); S4 is scripted and awaiting its first run.

1. ~~**Baseline** — S1 with the new enforcer.~~ Done.
2. ~~**eFLINT models for `server` + the three clients** (§6.4).~~ Done.
3. ~~**Server-mandatory check** (§6.2).~~ Done.
4. ~~**S2/S3 by live policy change** through
   `PUT /api/v1/policyEnforcer/{steward}`.~~ Done, scripted.
5. **Replace `policyRemoval`/`policyReintroduction`** (§6.1) with that PUT, so
   mid-run changes are triggered from the training loop.
6. **S5** — dynamic mid-run change.

None of steps 1–4 needed new orchestrator code beyond the port itself; the only
code change was the api-gateway admission check.

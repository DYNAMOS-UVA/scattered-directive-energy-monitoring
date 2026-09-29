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
`clienttwo`, `clientthree`). The requester is currently
`evangelos.pipilikas@student.uva.nl` — the only user with relations in
`configuration/etcd_launch_files/agreements.json`.

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

### Summary

| Scenario | server | clients permitted | C1 | Training |
|---|---|---|---|---|
| S1 | yes | 3 | admit | 3 clients |
| S2 | **no** | any | **reject** | does not start |
| S3 | yes | 2 | admit | 2 clients |

### Future — S4, dynamic policy change mid-run

A policy change lands *while* training is running; at the next C2 the client set
changes and the VFL training configuration adapts in real time (client dropped
or reintroduced, server architecture resized, optionally backtracking to a saved
checkpoint). Not in scope yet, but S1–S3 are deliberately shaped so that S4 is
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

### 6.2 Enforce "server mandatory, ≥1 client"

Not expressed anywhere today. The per-round handler only deletes unauthorised
providers from `clients`; it never checks that `server` survived. Two options:

- **(a) In eFLINT** — a Layer-2 rule making admissibility depend on the server
  relation. Most faithful to the policy-as-code goal, but needs the request
  context to distinguish the server steward from client stewards.
- **(b) In the orchestrator / api-gateway** — after validation, reject if
  `server ∉ ValidDataproviders` or if no client remains.

(b) is the pragmatic first step and is enough for S1–S3; (a) is the better
long-term home.

### 6.3 Verify reintroduction actually re-adds clients

The C2 handler deletes providers missing from the response. A comment says
*"or add the authorised ones if they were not present before"*, but the visible
code only deletes. If so, S4 (and `policy_reintroduction`) cannot work until
re-adding is implemented.

### 6.4 eFLINT models for the VFL stewards

The VFL stewards are currently served by the **legacy JSON** agreements
translated into eFLINT phrases, which works. To drive S1–S3 by *editing policy*,
each steward needs a real `.eflint` model (like `VU.eflint`) plus a
`provider_configs.json` entry with `validationStrategy: "eflint"`. Without this
the `PUT /policyEnforcer/{steward}` flow has nothing meaningful to update.

---

## 7. Suggested order of work

1. **Baseline** — run VFL end to end unchanged, confirm S1 works with the new
   enforcer. Nothing to build; this is tracker test T7.
2. **eFLINT models for `server` + the three clients** (§6.4), then re-confirm S1.
   Mechanical: mirror `VU.eflint` per steward.
3. **S2 and S3 by static policy** — author the models with the server relation
   removed (S2) / one client relation removed (S3) and confirm the expected
   behaviour. Requires §6.2 for S2 to reject rather than silently continue.
4. **S2/S3 by live policy change** — same scenarios driven through
   `PUT /api/v1/policyEnforcer/{steward}` instead of editing files up front.
5. **Replace `policyRemoval`/`policyReintroduction`** (§6.1) with that PUT, so
   mid-run changes are triggered from the training loop.
6. **S4** — dynamic mid-run change, after §6.3 is confirmed.

Steps 1–3 need no new orchestrator code; they exercise what the port already
delivered.

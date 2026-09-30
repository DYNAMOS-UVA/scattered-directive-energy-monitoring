# DYNAMOS VFL with eFLINT reasoner 

This guide records DYNAMOS vertical federated learning (VFL) demonstrations with eFLINT-based policy reasoning. It shows how policy decisions determine whether a VFL operation may start, continue, exclude a client, or stop.

### Policy reasoning in DYNAMOS

DYNAMOS is a policy-aware data-exchange system for executing data operations involving multiple partners. Earlier versions used static agreements expressed in JSON files. This version introduces an eFLINT integration that replaces the previous static policy-evaluation mechanism with a multi-instance eFLINT reasoner component. eFLINT provides richer semantics and reasoning capabilities for expressing and evaluating policies. Using multiple reasoner instances also helps avoid a single-instance bottleneck, so that subsequent policy-evaluation requests are not unnecessarily delayed.

The original JSON agreements have been translated into equivalent eFLINT agreement files and adapted to the VFL use case with four participants. These files are located in `configuration/eflint-models`. The live eFLINT state is stored in the etcd knowledge repository, consistent with the previous DYNAMOS design, providing shared and persistent system-wide policy storage.

The eFLINT integration also supports dynamic policy change while a workflow is executing. A revised agreement can cause DYNAMOS to stop an operation when its permissions are no longer acceptable, or to perform a more specific adjustment—such as continuing a VFL workflow with fewer authorised clients.

The VFL operation has four possible participants:

- **Server**: holds labels and performs aggregation; its permission is mandatory.
- **Client 1** (`clientone`)
- **Client 2** (`clienttwo`)
- **Client 3** (`clientthree`)

A request is admissible when the server and at least one client are permitted.

## eFLINT agreement files

The eFLINT agreement files describe permitted relations and capabilities for each data steward. There is one agreement model per steward, for example `clientone`.

The example `clientone` agreement identifies the steward, compute provider, dataset, and supported `computeToData` archetype. Its relation block states that `requestor` may submit the listed VFL request types, use `clientoneData`, use the archetype, and use `clientone` as compute provider.

The policy enforcer evaluates the agreement together with shared eFLINT rules and request-specific facts. A relation missing from a steward agreement means that the requester is not authorised to use that steward in the VFL request.


## eFLINT agreement files

The eFLINT agreement files describe permitted relations and capabilities for each data steward. There is one agreement model per steward, for example `clientone`.

The example `clientone` agreement identifies the steward, compute provider, dataset, and supported `computeToData` archetype. Its relation block states that `requestor` may submit the listed VFL request types, use `clientoneData`, use the archetype, and use `clientone` as compute provider.

The policy enforcer evaluates the agreement together with shared eFLINT rules and request-specific facts. A relation missing from a steward agreement means that the requester is not authorised to use that steward in the VFL request.

---

## Setup

The commands below were tested from a WSL terminal on Windows.

The scenario scripts are in the repository's `scripts/` directory. The policy models are stored and updated through the policy-enforcer/orchestrator path; the repository copies of `.eflint` files are not modified by a normal scenario run.

### Install DYNAMOS

For a lightweight local demonstration without monitoring:

```bash
bash ./configuration/dynamos-configuration-no-monitoring.sh local
```

### Uninstall DYNAMOS

```bash
bash ./configuration/uninstall-dynamos.sh
```

---

## Port forwarding

Run each command in a separate terminal:

```bash
kubectl -n orchestrator port-forward svc/policy-enforcer 18083:8080
```

```bash
kubectl -n orchestrator port-forward svc/orchestrator 18082:8080
```

---

## Tests

The scenario scripts submit a VFL request, query the policy enforcer at the configured policy-check interval, and store a final JSON result. The result contains a status and, for each executed training round, the number of participating clients.

### Timeline legend

| Symbol | Meaning |
|---|---|
| ✓ | Participant took part in the training round. |
| ✗ | Participant was denied by policy at the decision point. |
| ⊘ | No round was executed because the VFL operation was rejected or stopped. |
| ? | The available result does not identify this participant's individual permission. |

**Important:** The JSON result records the number of participating clients per round, not their identities. Where a scenario's default script identifies the excluded client, the timelines use that default (`clientthree`). Otherwise, the table explicitly states the assumption.

---

## Scenarios with available results

you can find the raw JSON results here: results\vfl-scenarios 

### S1 — Full permission

**Result:** `done` after five rounds. Every recorded round used three clients, consistent with full participation by the server and all three clients.

| Participant / operation | Round 0 | Round 1 | Round 2 | Round 3 | Round 4 |
|---|---:|---:|---:|---:|---:|
| Server | ✓ | ✓ | ✓ | ✓ | ✓ |
| Client 1 | ✓ | ✓ | ✓ | ✓ | ✓ |
| Client 2 | ✓ | ✓ | ✓ | ✓ | ✓ |
| Client 3 | ✓ | ✓ | ✓ | ✓ | ✓ |
| VFL operation | Continue | Continue | Continue | Continue | Complete |

**Configuration recorded in the result:** five total rounds; policy-check interval of one round.

---

### S2 — Server denied before execution

**Result:** `failed` with no recorded training rounds.

This is the admission-control case: the operation was not executed. The available JSON contains no explicit stop reason and no individual client verdicts, so the client rows cannot be inferred from the result itself.

| Participant / operation | Before round 0 | Rounds 0 onward |
|---|---:|---:|
| Server | ✗ — scenario policy denies server permission | ⊘ |
| Client 1 | ? | ⊘ |
| Client 2 | ? | ⊘ |
| Client 3 | ? | ⊘ |
| VFL operation | Cancelled at admission check | Not executed |

---

### S3 — Partial client permission

**Result:** `done` after five rounds. Every recorded round used two clients.

The S3 default scenario excludes `clientthree`; the JSON confirms two participating clients in every round. The table therefore uses the default script configuration.

| Participant / operation | Round 0 | Round 1 | Round 2 | Round 3 | Round 4 |
|---|---:|---:|---:|---:|---:|
| Server | ✓ | ✓ | ✓ | ✓ | ✓ |
| Client 1 | ✓ | ✓ | ✓ | ✓ | ✓ |
| Client 2 | ✓ | ✓ | ✓ | ✓ | ✓ |
| Client 3 | ✗ | ✗ | ✗ | ✗ | ✗ |
| VFL operation | Continue | Continue | Continue | Continue | Complete |

**Configuration recorded in the result:** five total rounds; policy-check interval of one round.

---

### S5 — Server permission revoked during execution

**Result:** `failed`. Four rounds were completed with three clients. The result records `stop_reason: "the server is not authorized by policy"` and `stopped_before_round: 4`.

The server remains a mandatory VFL participant. Once its permission was no longer accepted at the scheduled policy check, the reasoner prevented the next cycle from starting. The client permissions are not the cause of the stoppage; the entire operation is cancelled because there is no authorised server.

| Participant / operation | Round 0 | Round 1 | Round 2 | Round 3 | Before round 4 |
|---|---:|---:|---:|---:|---:|
| Server | ✓ | ✓ | ✓ | ✓ | ✗ |
| Client 1 | ✓ | ✓ | ✓ | ✓ | ⊘ |
| Client 2 | ✓ | ✓ | ✓ | ✓ | ⊘ |
| Client 3 | ✓ | ✓ | ✓ | ✓ | ⊘ |
| VFL operation | Continue | Continue | Continue | Continue | **Cancelled** |

**Configuration recorded in the result:** ten requested rounds; policy-check interval of one round; execution stopped before round 4.

---

### S6 — Client permission revoked during execution

**Result:** `done` after ten rounds. Rounds 0–3 used three clients; rounds 4–9 used two clients.

The S6 default scenario revokes `clientthree`. The result confirms the expected transition from three to two clients while the overall VFL operation continues and completes.

| Participant / operation | Round 0 | Round 1 | Round 2 | Round 3 | Round 4 | Round 5 | Round 6 | Round 7 | Round 8 | Round 9 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Server | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Client 1 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Client 2 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Client 3 | ✓ | ✓ | ✓ | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |
| VFL operation | Continue | Continue | Continue | Continue | Continue | Continue | Continue | Continue | Continue | Complete |

**Policy effect:** the client exclusion takes effect from round 4. The server remains permitted and two clients remain available, so the VFL acceptance rule remains satisfied.

**Configuration recorded in the result:** ten total rounds; policy-check interval of one round.

---

## Manual policy change

To change a policy manually, edit the relevant steward's `.eflint` agreement file and upload the changed file through the **orchestrator endpoint**. Do not use `etcdctl` for this workflow.

```text
PUT /api/v1/policyEnforcer/{steward}
Content-Type: text/plain
<body: revised complete .eflint agreement file>
```

The endpoint replaces the live agreement for that steward. The scenario scripts use the same endpoint.

### Example: exclude `clientthree`

1. Locate and make a backup of the client agreement:

```bash
cd <repository-root>

cp configuration/eflint-models/clientthree.eflint \
   configuration/eflint-models/clientthree.eflint.bak
```

2. Open `configuration/eflint-models/clientthree.eflint` in VS Code.

3. Remove the entire relation block for `requestor`, not only the `+has-relation` statement:

```eflint
+has-relation("requestor", "clientthree").
+relation-allows-request-type("requestor", "clientthree", "vflTrainModelRequest").
+relation-allows-request-type("requestor", "clientthree", "vflTrainRequest").
+relation-allows-dataset("requestor", "clientthree", "clientthreeData").
+relation-allows-archetype("requestor", "clientthree", "computeToData").
+relation-allows-compute-provider("requestor", "clientthree", "clientthree").
```

The shared agreement rules condition the `relation-allows-*` facts on `has-relation`; therefore the complete requestor relation block should be removed together.

4. Upload the edited agreement file through the orchestrator. This assumes the orchestrator port-forward is running on port `18082`:

```bash
curl -sS -X PUT \
  "http://127.0.0.1:18082/api/v1/policyEnforcer/clientthree" \
  -H "Content-Type: text/plain" \
  --data-binary "@configuration/eflint-models/clientthree.eflint"
```

A successful update returns HTTP `200`.

5. Verify the updated verdict through the policy enforcer. This assumes its port-forward is running on port `18083`:

```bash
curl -sS -X POST \
  "http://127.0.0.1:18083/api/v1/policy-enforcer/validate" \
  -H "Content-Type: application/json" \
  --data-raw '{
    "user": {"id": "GUID", "user_name": "requestor"},
    "data_providers": ["clientone", "clienttwo", "clientthree", "server"]
  }' | python3 -m json.tool
```

The expected verdict lists `clientthree` as invalid while `clientone`, `clienttwo`, and `server` remain valid.

### Effect on VFL execution

- For a **new** VFL request, the changed agreement is evaluated at admission.
- For an **active** VFL request, the change is observed at its next scheduled policy check. With `policy_check_interval: 1`, this is before the next round after the current completed round.
- Excluding `clientthree` leaves the server plus two clients, so the VFL operation may continue, as in S6.

### Restore Client 3

After the demonstration, restore the original file and upload it again:

```bash
mv configuration/eflint-models/clientthree.eflint.bak \
   configuration/eflint-models/clientthree.eflint

curl -sS -X PUT \
  "http://127.0.0.1:18082/api/v1/policyEnforcer/clientthree" \
  -H "Content-Type: text/plain" \
  --data-binary "@configuration/eflint-models/clientthree.eflint"
```

For a reintroduction during an active run, upload the restored agreement before a later policy-check boundary. The client can then be considered again at that check.

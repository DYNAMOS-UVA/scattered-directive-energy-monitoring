# VU Policy Change Quick Check (WSL)

---

This guide shows a minimal flow to:
1. Change an eFLINT policy file
2. Upload it via the Orchestrator API (no pod file sync required)
3. Confirm that Policy Enforcer behavior changed

Two update paths are available:
- Compatibility endpoint: `POST /api/v1/policyEnforcer/eflintModels`
- Native endpoint: `PUT /api/v1/policyEnforcer/{steward}`

## Prerequisites

- Kubernetes context points to docker-desktop
- Orchestrator and Policy Enforcer are running
- You are in the repository root in WSL

## 1) Edit the policy file locally

Update the relevant file under:

    configuration/eflint-models/VU.eflint     # or UVA.eflint / RUG.eflint

Example change for testing:
- Revoke or remove `computeToData` authorization line
- Keep `dataThroughTtp` allowed

## 2) Port-forward Orchestrator and Policy Enforcer

Run each in a separate terminal and keep them open:

    kubectl -n orchestrator port-forward svc/orchestrator 18082:8080
    kubectl -n orchestrator port-forward svc/policy-enforcer 18083:8080

  Quick sanity check before upload:

    curl -i "http://127.0.0.1:18082/api/v1/requesttypes"

  Expected:
  - `HTTP/1.1 200 OK`

  If you get `404 page not found`, your local `18082` forward is likely not attached to the Orchestrator API (common when an old WSL relay/forward is still bound). Stop the existing forward and start a fresh one, or use a clean local port (for example `28082`) and update the commands below accordingly.

## 3) Upload the updated policy file to etcd

### Option A: Compatibility upload endpoint (POST)

Use the compatibility endpoint to upload an `.eflint` file directly. The steward name is derived from the filename stem (for example, `VU.eflint` maps to etcd key `/policyEnforcer/eflintModels/VU`).

    curl -i -X POST "http://127.0.0.1:18082/api/v1/policyEnforcer/eflintModels" \
      -F "file=@configuration/eflint-models/VU.eflint"

<!-- (confirm it works)

Alternative (raw body + filename query parameter):

    curl -i -X POST "http://127.0.0.1:18082/api/v1/policyEnforcer/eflintModels?filename=VU.eflint" \
      -H "Content-Type: text/plain" \
      --data-binary @configuration/eflint-models/VU.eflint -->

### Option B: Native steward endpoint (PUT)

Use the native endpoint to update a specific steward directly. For eFLINT payloads, send plain text.

    curl -i -X PUT "http://127.0.0.1:18082/api/v1/policyEnforcer/VU" \
      -H "Content-Type: text/plain" \
      --data-binary @configuration/eflint-models/VU.eflint

(Confirmed that works)

Notes:
- `PUT /policyEnforcer/{steward}` supports both formats:
  - `application/json` for legacy Agreement JSON
  - non-JSON types (for example `text/plain`) for eFLINT text
- `sharedRules` is reserved and must be updated via `PUT /api/v1/policyEnforcer/sharedRules`.
- Both flows wait for Policy Enforcer acknowledgement and can return `504 Timeout waiting for Policy Enforcer validation` if no ack arrives in time.

Continue with the verification steps below regardless of which option you used.

<!-- ## 4) Confirm model in etcd

    ETCD_POD=$(kubectl -n core get pod -l app=etcd -o jsonpath="{.items[0].metadata.name}")
    kubectl -n core exec "$ETCD_POD" -- sh -c 
      "ETCDCTL_API=3 etcdctl --endpoints=http://127.0.0.1:2379 get /policyEnforcer/eflintModels/VU | sed -n '1,160p'"

Confirm the output matches your edited `VU.eflint` content. -->

## 5) Confirm policy behavior through Policy Enforcer

Check allowed clauses for the user:

    curl -sS -G "http://127.0.0.1:18083/api/v1/policy-enforcer/allowed-clauses" \
      --data-urlencode "steward=VU" \
      --data-urlencode "requester=Jorrit"

Expected after removing `computeToData`:
- `archetypes` contains `dataThroughTtp`
- `archetypes` does **not** contain `computeToData`

Validate request through the current HTTP validate schema:

    curl -sS -X POST "http://127.0.0.1:18083/api/v1/policy-enforcer/validate" \
      -H "Content-Type: application/json" \
      -d '{
        "user": {
          "id": "1",
          "user_name": "Jorrit"
        },
        "data_providers": ["VU"]
      }'

Note: `user_name` must exactly match the requester identifier used in the
agreement's `+has-relation(...)` fact (see `configuration/eflint-models/VU.eflint`,
currently `"Jorrit"`, not the email `jorrit.stutterheim@cloudnation.nl`). The
validate request builds the Layer-3 `+requester(...)` fact directly from this
field, so a mismatch causes `has-relation` to fail and the steward to be
reported as invalid even though the agreement supports the archetype.

Expected:
- Response is a `validationResponse` JSON.
- Inspect `valid_dataproviders.VU.archetypes` to confirm policy effect:
  - after removing `computeToData`, it should no longer appear
  - `dataThroughTtp` should still appear when kept in the model

## Result

If steps 4 and 5 pass, the policy change is successfully loaded and enforced.

---

## Legacy method: updateEtc (requires pod filesystem sync)

The original method writes etcd from the Orchestrator pod's own filesystem. Use this only if the pod already has the correct files, or after manually copying updated files into the pod.

### Copy updated files into the pod first

    ORCH_POD=$(kubectl -n orchestrator get pod -l app=orchestrator -o jsonpath="{.items[0].metadata.name}")
    kubectl cp configuration/eflint-models/VU.eflint \
      orchestrator/${ORCH_POD}:/app/etcd/eflint-models/VU.eflint

Verify:

    kubectl -n orchestrator exec "$ORCH_POD" -- sh -c \
      "sed -n '1,120p' /app/etcd/eflint-models/VU.eflint"

### Trigger updateEtc

    curl -i http://127.0.0.1:18082/api/v1/updateEtc

Expected response:

    HTTP/1.1 200 OK
    Updated all config

Then continue from step 4 above to verify.

# eFLINT Policy Enforcer — Orchestrator Integration Tracker

Scratch/working document. Tracks the port of the eFLINT policy enforcer
integration from `DYNAMOS-UVA/DYNAMOS-eflint` into this fork.

> **Fork lineage warning.** This repo (`scattered-directive-energy-monitoring`)
> is the VFL / energy-monitoring fork: generic `Request` proto, `redeploy` flag,
> `getClients`, VFL agents (`clientone/two/three`, `server`).
> `DYNAMOS-eflint` forks `nielsarts/DYNAMOS` and uses `SqlDataRequest`.
> This is a **cherry-pick** job, never a merge.

Upstream clone used for diffing: `git clone https://github.com/DYNAMOS-UVA/DYNAMOS-eflint`

---

## Status

| Step | Scope | State |
|---|---|---|
| 1 | S5 + O11 + O8 + O9 — populate etcd from the PVC | **Done — deployed and verified 2026-09-28** |
| 2 | S1 + O12 + O13 — `checkJobs` by steward name | Not started |
| 3 | O5 + O6 + O2 + O1 + O3 — policy update endpoint | Not started |
| 4 | S3, O15, O16, O17, O14 — correctness fixes | Not started |
| 5 | P3 — drop vendored `cmd/policy-enforcer/pkg` + go.mod/go.sum | Not started |

---

## 1. Policy enforcer service itself

Every file under `go/cmd/policy-enforcer/` (`main.go`, `consume.go`, `eflint/`,
`reasoner/`, `repository/`, `service/`, `policyenforcerhttp/`, `httpapi/`,
`embedded-models/`) is **byte-identical** to upstream. Only three deltas:

| # | Delta | Direction | Verdict |
|---|---|---|---|
| P1 | `routes.go`: `/readyz` + `readinessHandler` checking the eFLINT instance pool | local-only | **Keep ours.** Chart `readinessProbe` hits `/api/v1/readyz`; upstream has no such route. |
| P2 | `Dockerfile`: `haskell:9.6.6`, `command -v ghc` symlink, `~/.local/bin/eflint-server`, build context from repo root | local-only | **Keep ours.** Upstream's `haskell:9.2.8` + archived-buster apt hack is more fragile. |
| P3 | `cmd/policy-enforcer/go.mod`, `go.sum` + vendored copy of `pkg/` (~7.5k lines) | local-only | **Drop.** Our own Dockerfile already `rm -f`s them; stale duplicate of `go/pkg`. |

## 2. Orchestrator

| # | Upstream change | Verdict | Why |
|---|---|---|---|
| O1 | `api.go`: `handlePutPolicyResource` → `PUT /policyEnforcer/{steward}` (Content-Type picks `json` vs `eflint`) and `PUT /policyEnforcer/sharedRules` | **Keep** | This *is* "change policies via the orchestrator endpoint". |
| O2 | `api.go`: `awaitPolicyEnforcerAck` — correlation-ID channel, 30s timeout, 504 on no ack, 400 on rejection | **Keep** | Without it a bad policy is persisted silently. |
| O3 | `api.go`: `GET /policyEnforcer` rewritten onto the `/agreements/` sub-prefix | **Keep** | Otherwise GET tries to unmarshal `eflintModels`/`configs` as `api.Agreement` and 500s. |
| O4 | `api.go`: `POST /policyEnforcer/eflintModels` multipart compat upload | **Optional** | Only for the `-F file=@VU.eflint` flow. Prefer skipping — one code path. |
| O5 | `main.go`: `agreementUpdateMutex` / `agreementUpdateMap[string]chan *pb.PolicyUpdate` + routes | **Keep** | Required by O2. |
| O6 | `consume.go`: `case "agreementUpdate", "sharedRulesUpdate"` routing acks by correlation ID | **Keep** | Required by O2. |
| O7 | `consume.go`: deletion of `case "revalidationResponse"` and the `redeploy bool` param | **Do not keep** | Their fork's cleanup; our energy-experiment redeploy path depends on it. |
| O8 | `etcd_config.go`: scan `eflintModelsDirectory`, route by filename to `/policyEnforcer/eflintLayer1/interface`, `/policyEnforcer/eflintRules/shared`, `/policyEnforcer/eflintModels/<steward>` | **Keep** | Mandatory — the enforcer's repositories read exactly these prefixes. |
| O9 | `etcd_config.go`: load `provider_configs.json` → `/policyEnforcer/configs/<name>` | **Keep** | `provider_config_repository.go` reads this to pick legacy vs eflint per steward. |
| O10 | `etcd_config.go`: explicit generic params `SaveStructToEtcd[api.X](...)` | **Do not keep** | Cosmetic; inference already works. |
| O11 | `config_prod.go` / `config_local.go`: `eflintModelsDirectory`, `providerConfigsLocation` | **Keep** | Required by O8/O9. |
| O12 | `manage_jobs.go`: `checkJobs(agreementName string)` instead of `checkJobs(*api.Agreement)`, deriving users from etcd job keys | **Keep** | Non-negotiable: an eFLINT policy is opaque text; no `api.Agreement.Relations` to iterate. |
| O13 | `manage_jobs.go`: `checkAllJobs()` | **Keep** | Needed for the `sharedRules` path (O1). |
| O14 | `manage_jobs.go`: `deleteJobAcrossAgents` + revocation handling in `processPolicyUpdate` (`continue` not `return`, drop agents absent from `ValidDataproviders`, empty-routing-key guard) | **Keep, port by hand** | Real bug fixes, but our copy has VFL branches around the same code. |
| O15 | `composition_request.go`: nil-safety on `ValidArchetypes.Archetypes[provider]` + `archetypesForLog` | **Keep** | The eFLINT reasoner returns providers with no archetype entry → current code nil-panics. |
| O16 | `composition_request.go`: dedupe in `chooseThirdParty` | **Keep** | Reasoner emits repeated compute providers, inflating the intersection count. |
| O17 | `composition_request.go`: `computeToData` enforced when `aggregate:false` and >1 provider | **Keep** | Behavioural fix, independent of eFLINT. |
| O18 | `composition_request.go`: removal of `getClients`, the VFL branch and `redeploy` | **Do not keep** | Would break VFL. |

## 3. Shared `pkg/` and configuration

| # | Change | Verdict |
|---|---|---|
| S1 | `pkg/etcd/etcd_get.go`: new `GetFullKeysFromPrefix` | **Keep** — required by O12/O13. |
| S2 | `pkg/etcd/etcd_get.go`: upstream *removes* the `StopOnMissing` → `bo.Permanent` branch in `GetKeysFromPrefix` | **Do not keep** — regression against ours. |
| S3 | `pkg/lib/utils.go`: `GenerateJobName` accepts non-email usernames | **Keep** — eFLINT payloads use `"Jorrit"`; current code returns `""` without `@`. |
| S4 | `pkg/api/http.go`: `ProviderValidationConfig`, `EflintSavedState`, `ValidationStrategy*` | **Already present locally** — no-op. |
| S5 | `configuration/etcd_launch_files/provider_configs.json` | **Keep / add** — was missing; required by O9. |
| S6 | `proto-files`: `SqlDataRequest`, `traces` in `RequestMetadata` | **Do not keep** — fork divergence. `PolicyUpdate` is already identical, so no proto regeneration needed. |
| S7 | `orchestrator/archetype_test.go` | **Do not keep** — 100 lines, entirely commented out. |
| S8 | `scripts/policy-change-demo-*.sh`, `trigger-and-poll-request.sh` | **Optional** — assume `sqlDataRequest`; port later if wanted. |

---

## Findings log

### 2026-09-28 — `svc/policy-enforcer` not found
The chart shipped a Deployment but **no Service**. Added a `ClusterIP` Service
(port 8080 → 8080, selector `app: policy-enforcer`, `http-` port-name prefix for
Linkerd) to `charts/orchestrator/templates/policyEnforcer.yaml` and the `fabric/`
copy. Port-forward works now.

### 2026-09-28 — how etcd got populated
`/policyEnforcer/{eflintModels,eflintRules,eflintLayer1,configs}/*` were present
in the live cluster, but **nothing in the repo writes them**:

- `configuration/fill-etcd-pvc.sh` only `kubectl cp`s files onto the **PVC
  filesystem** (`/mnt` → `/app/etcd` in the orchestrator). No `etcdctl put`
  anywhere in the repo.
- `init-etcd-pvc` Job (`charts/namespaces/templates/etcd-pvc.yaml`) only does
  `mkdir -p /etcd/eflint-models`.
- The policy enforcer's PVC mount at `/app/eflint-models` serves exactly one
  variable, `eflintModelPath` = `01_interface_policy.eflint` (Layer 1 only,
  and it has an embedded fallback). Everything else is etcd-only.
- The enforcer has `SaveEflintModel`/`SaveProviderConfig` but only on the
  validation/reconcile path — no directory-scan bootstrap.
- Running image is `dynamos1/orchestrator:baseline`, built from local source.
  `grep -c 'eflintLayer1' /app/orchestrator` inside the pod → **0**. No loader.
- `/app/etcd` has no `provider_configs.json`, yet `/policyEnforcer/configs/*`
  exists.

**Conclusion:** seeded by hand in an earlier session. Not reproducible, and
nothing keeps `/policyEnforcer/eflintModels/VU` in sync with
`configuration/eflint-models/VU.eflint`. Hence step 1.

### 2026-09-28 — step 1 implemented
- Added `configuration/etcd_launch_files/provider_configs.json`
  (VU/UVA → `eflint`, RUG → `legacy`; matches current cluster state).
  Stewards with no entry default to `legacy` in the enforcer, so the VFL
  agents (`clientone`…, `server`) keep working unchanged.
- `config_prod.go`: `providerConfigsLocation`, `eflintModelsDirectory`
  (= `/app/etcd/eflint-models`, already PVC-populated).
- `etcd_config.go`: new `registerEflintSpecifications()` + provider-config loop.
  Used `os.ReadDir`/`os.ReadFile` instead of upstream's deprecated `ioutil`.
- Both run from `registerPolicyEnforcerConfiguration()`, so they fire on
  startup **and** on `GET /api/v1/updateEtc` — the re-seed button.
- `go build ./cmd/orchestrator/` passes.

### 2026-09-28 — step 1 deployed and verified

Deployed as `dynamos1/orchestrator:baseline-eflint-2`
(`sha256:ac21afe0…`). All checks passed:

1. `grep -c 'eflintLayer1' /app/orchestrator` inside the pod → `1` (was `0`).
2. Startup logs show `Loading eFLINT models from directory /app/etcd/eflint-models`
   plus 9 × `Loaded eFLINT spec … into …` (Layer 1, shared rules, RUG, UVA, VU,
   empty, both generated bundles, simple_facts).
3. Deleted `/policyEnforcer/eflintModels/VU` and `/policyEnforcer/configs/VU`,
   then `GET /api/v1/updateEtc` → `200 OK` and both keys restored.
   `configs/VU` came back as
   `{"name":"VU","validationStrategy":"eflint","agreementLocation":"/app/eflint-models/VU.eflint"}`.
4. `allowed-clauses?steward=VU&requester=Jorrit` still returns
   `dataThroughTtp` + `computeToData`, compute provider `SURF`, dataset
   `wageGap`.

---

## Cluster housekeeping

- Deployed tags after step 1: `dynamos1/orchestrator:baseline-eflint-2`,
  `dynamos1/policy-enforcer:baseline-eflint-2`, `dynamos1/sidecar:baseline`.
- **Always deploy via `configuration/dynamos-configuration.sh`**, which passes
  `--set dockerArtifactAccount=${DOCKERHUB_ACCOUNT}`. A bare
  `helm upgrade -f charts/orchestrator/values.yaml …` leaves
  `dockerArtifactAccount: ""` (see `charts/orchestrator/values.yaml`), producing
  image `/orchestrator:latest` → `InvalidImageName` pods that never roll out
  while the old ReplicaSet keeps serving. Decision: **leave the empty default
  as-is** — it fails loudly, whereas hard-coding `dynamos1` would make other
  accounts silently pull the wrong images.
  Stuck ReplicaSets seen this way: `orchestrator-cd74d886c`,
  `orchestrator-7ccb95554d`, `policy-enforcer-59c9674dfd`.
- Port-forwards die when a pod restarts. `curl: (52) Empty reply from server`
  against `18082`/`18083` almost always means the forward needs restarting, not
  that the service is broken.

## Useful commands

```bash
# Inspect the policy-enforcer keys (must target the etcd container explicitly;
# linkerd-proxy is the default container and has no shell)
ETCD_POD=$(kubectl -n core get pod -l app=etcd -o jsonpath="{.items[0].metadata.name}")
kubectl -n core exec "$ETCD_POD" -c etcd -- \
  etcdctl --endpoints=http://127.0.0.1:2379 get /policyEnforcer/ --prefix --keys-only

# Port-forwards
kubectl -n orchestrator port-forward svc/orchestrator 18082:8080
kubectl -n orchestrator port-forward svc/policy-enforcer 18083:8080

# Re-seed etcd from the PVC (after the orchestrator carries the new loader)
curl -i http://127.0.0.1:18082/api/v1/updateEtc

# Build + push + roll
cd go && make orchestrator
kubectl -n orchestrator rollout restart deploy/orchestrator
```

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
| 2 | S1 + O12 + O13 — `checkJobs` by steward name | **Done — verified in-cluster 2026-09-28** |
| 3 | O5 + O6 + O2 + O1 + O3 (+ SC1–SC3) — policy update endpoint | **Done — fully verified 2026-09-29 (T3–T6)** |
| 4 | S3, O15, O16, O17, O14 — correctness fixes | **Mostly done 2026-09-29.** S3, O15, O16, O17 implemented; O14's revocation path landed in step 2 behind the G2 guard. Remainder of O14 outstanding. Not yet deployed. |
| 5 | P3 — drop vendored `cmd/policy-enforcer/pkg` + go.mod/go.sum | Not started |

Open risks: **G5** (`getJobAcrossAgents` returns an empty map — causes spurious
denials), **G3** (stale job records), **G6** (Linkerd cert rotation).
**G2** mitigated (job deletion on revocation is opt-in).
**G1 retracted** — VFL *is* compatible with the eFLINT enforcer. See
[Known gaps and risks](#known-gaps-and-risks).

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
| O9 | `etcd_config.go`: load `provider_configs.json` → `/policyEnforcer/configs/<name>` | **Keep — done** | `provider_config_repository.go` reads this. ⚠️ It only selects the *phrase provider* (eFLINT text vs legacy JSON); both feed the same reasoner — see G1. |
| O10 | `etcd_config.go`: explicit generic params `SaveStructToEtcd[api.X](...)` | **Do not keep** | Cosmetic; inference already works. |
| O11 | `config_prod.go`: `eflintModelsDirectory`, `providerConfigsLocation` | **Keep — done** | Required by O8/O9. Upstream also adds a `config_local.go`; this fork has none, so only `config_prod.go` was touched. |
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

### 2026-09-28 — steps 2 + 3 implemented

Done together because step 2 alone changes no observable behaviour.

- `pkg/etcd/etcd_get.go`: added `GetFullKeysFromPrefix` (S1). Kept our
  `StopOnMissing` → `bo.Permanent` behaviour in `GetKeysFromPrefix` (S2 stays
  a don't-keep) and honoured `StopOnMissing` in the new function too.
- `manage_jobs.go`: `checkJobs(agreementName string)` now derives the user set
  from `/agents/jobs/<steward>/<user>/<job>` keys (O12); added `checkAllJobs()`
  (O13).
- `api.go`: `PUT /policyEnforcer/{steward}` and
  `PUT /policyEnforcer/sharedRules` (O1), `awaitPolicyEnforcerAck` (O2), GET
  rewritten onto the `/agreements/` sub-prefix (O3).
- `main.go`: `agreementUpdateMutex` / `agreementUpdateMap` (O5).
- `consume.go`: `case "agreementUpdate", "sharedRulesUpdate"` ack routing (O6).
  The `revalidationResponse` case and the `redeploy` flag were left untouched
  (O7 stays a don't-keep).

Deviations from upstream, deliberate:

- **O4 skipped.** No `POST /policyEnforcer/eflintModels` multipart route. Only
  the `PUT` path exists. `docs/VU_POLICY_CHANGE_QUICKCHECK.md` "Option A" will
  therefore not work — use Option B (`PUT` + `text/plain`).
- **Part of O14 pulled forward.** The old `checkJobs` deleted job info when a
  legacy agreement granted no archetypes (`relationDetails.AllowedArchetypes`
  empty). That branch cannot survive the O12 rewrite, so `deleteJobAcrossAgents`
  plus the `len(ValidDataproviders) == 0` early return in `processPolicyUpdate`
  were added now to avoid regressing full-revocation cleanup. The rest of O14
  (per-agent revocation, `continue` instead of `return`, empty-routing-key
  guard) is still pending in step 4.
- **`deleteJobInfo` removed.** Became unreachable after the O12 rewrite, and it
  only deleted the `queueInfo` key, never the job key. Superseded by
  `deleteJobAcrossAgents`.
- **Ack plumbing de-duplicated.** Upstream repeats the
  build-PolicyUpdate/await/check-approval block in three handlers; here it is a
  single `submitPolicyUpdate` helper. The 30 s timeout is a named constant,
  `policyEnforcerAckTimeout`.

`go build ./...` and `go vet ./cmd/orchestrator/ ./pkg/etcd/` both pass.
(`gofmt -l` flags most files in the repo due to CRLF line endings — pre-existing,
not caused by these edits.)

### 2026-09-28 — 504 on the first live test: the sidecar was the missing piece
`PUT /policyEnforcer/sharedRules` returned
`504 Timeout waiting for Policy Enforcer validation`. That already proved the new
orchestrator code was live (old code would have failed instantly in
`GenericPutToEtcd`). The ack never came back. Trace:

- orchestrator: `awaitPolicyEnforcerAck: sending type="sharedRulesUpdate"` →
  30 s later, timeout.
- orchestrator sidecar: `PolicyUpdate destination queue: policyEnforcer-in`,
  then `Backoff!!`.
- policy-enforcer sidecar: `switchin: {"msg,Type": "policyUpdate"}` →
  `Starting handlePolicyUpdate`.
- policy-enforcer main: nothing but `/api/v1/readyz` probes; the container
  restarted right after the message arrived.

**Root cause:** `SendPolicyUpdate` in the sidecar hardcoded
`Type: "policyUpdate"` on the AMQP publishing, discarding `in.Type`. So
`agreementUpdate` / `sharedRulesUpdate` reached the enforcer labelled
`policyUpdate` and were dispatched to the wrong handler, which never acks.
(`SendMicroserviceComm` already used `in.Type` — `SendPolicyUpdate` was the
outlier.)

**Lesson:** the original diff analysis only covered `cmd/orchestrator` and
`pkg/`. The sidecar is a third component in this integration and was missed.
When tracing a message-routing bug, follow it through **all four** hops:
orchestrator → orchestrator-sidecar → policy-enforcer-sidecar → policy-enforcer.

Also: `grep` on a stripped Go binary is an unreliable "is the new code
deployed?" check — `sharedRulesUpdate` and even `policyEnforcer-in` reported 0
matches while `checkAllJobs` and `agreementUpdate` reported several. Prefer a
functional probe (hit the endpoint and look at the status code).

---

### 2026-09-29 — step 4 correctness fixes (S3, O15, O16, O17)

- **S3** `pkg/lib/utils.go`: `GenerateJobName` no longer returns `""` for
  usernames without `@`. It uses the local part when an `@` is present,
  otherwise the whole string, then lowercases and hyphenates so the result is a
  valid Kubernetes object name. Covered by a new `TestGenerateJobNamePrefix`.
  Behaviour change: mixed-case local parts are now lowercased
  (`Jake.Jongejans@…` → `jake-jongejans-…`), which K8s names require anyway.
- **O15** `composition_request.go`: `getArchetypeBasedOnOptions` and
  `chooseArchetype` are nil-safe on `ValidArchetypes.Archetypes[provider]`, and
  `chooseArchetype` now errors when `ValidArchetypes` is nil/empty instead of
  nil-panicking. Added the `archetypesForLog` helper.
- **O16** `composition_request.go`: `chooseThirdParty` dedupes each provider's
  `ComputeProviders` before counting, so repeated entries can no longer inflate
  the intersection past one per data provider.
- **O17** `composition_request.go`: with `aggregate:false` and more than one
  authorized provider, `computeToData` is selected explicitly instead of
  falling through to weight-based picking.

`go build ./...` and `go vet ./cmd/orchestrator/ ./pkg/lib/` pass.

Note: `go test ./pkg/lib/` reports two **pre-existing** failures,
`TestGetNotMatchedElements` and `TestSliceIntersectAndDifference2`. Both pass in
isolation and fail only in full-suite order — map-iteration-order flakes in set
helpers, unrelated to this work.

---

## 4. Sidecar (`go/cmd/sidecar`) — discovered 2026-09-28

| # | Change | Verdict |
|---|---|---|
| SC1 | `rabbit_send.go`: `SendPolicyUpdate` publishes `in.Type` (falling back to `"policyUpdate"`) instead of the hardcoded literal | **Keep — mandatory**, without it the new endpoints always 504. |
| SC2 | `rabbit_consume.go`: `handleAgreementUpdate` / `handleSharedRulesUpdate` (both unmarshal `pb.PolicyUpdate`) | **Keep — mandatory.** |
| SC3 | `grpc_server.go`: `Consume` switch cases for `"agreementUpdate"` / `"sharedRulesUpdate"` | **Keep — mandatory.** |
| SC4 | `rabbit_send.go`: `SendCompositionRequest` guard rejecting an empty `DestinationQueue` | **Do not keep (for now)** — out of scope and could trip the VFL flow. |
| SC5 | `SendRequest`→`SendSqlDataRequest`, `handleRequest`→`handleSqlDataRequest`, `case "request"`→`case "sqlDataRequest"` | **Do not keep** — fork divergence (S6). |

Note: the sidecar runs in **both** the orchestrator and policy-enforcer pods, so
`make sidecar` must be followed by restarting both deployments.

---

## Known gaps and risks

### G1 — ~~VFL is not compatible with the eFLINT enforcer~~ **RETRACTED 2026-09-29**

**This gap does not exist.** The original conclusion was wrong.

Evidence it was based on: `steward marked invalid by reasoner
{"steward":"clienttwo","reason":"permitted-at-steward did not hold"}` during the
post-`sharedRules` re-evaluation burst. But those denials were for
`jake.jongejans@student.uva.nl` and `Jorrit`, and
`configuration/etcd_launch_files/agreements.json` defines relations **only** for
`evangelos.pipilikas@student.uva.nl`. Denying users with no relation is correct
behaviour, not a translation failure.

Verified against the user who does have relations:

```
GET /api/v1/policy-enforcer/allowed-clauses?steward=clientone&requester=evangelos.pipilikas@student.uva.nl
-> {"supported_archetypes":["computeToData"],"relations":[{"request_types":["vflTrainRequest"],...}]}

POST /api/v1/policy-enforcer/validate
  {"user":{"id":"1234","user_name":"evangelos.pipilikas@student.uva.nl"},
   "data_providers":["clientone","clienttwo","clientthree","server"]}
-> "request_approved": true
   all four stewards in valid_dataproviders with computeToData
   "invalid_dataproviders": []
```

So `resolveProvider`'s legacy path **does** translate the VFL JSON agreements
into working eFLINT phrases (there is a `service/legacy_translator_test.go`
covering it). T7 is **not blocked**.

The `Jorrit` denials in that same burst have a different cause — see G5:
`agentsWithThisJob` was empty, so the `policyUpdate` carried zero data
providers and was trivially denied.

Lesson: do not infer a systemic incompatibility from denials without first
checking whether the subject actually has a relation in the agreement.

### G2 — `checkAllJobs()` is a destructive thundering herd

Observed after the first successful `PUT /policyEnforcer/sharedRules`:

- `checkAllJobs()` enumerated every steward with jobs and emitted one
  `policyUpdate` per job (~38 messages), each costing ~1.5 s of eFLINT
  reasoning — a multi-minute serialized burst.
- Because of G1, **every** re-evaluation returned zero valid data providers, so
  `processPolicyUpdate` took the revocation branch for all of them:
  `no valid data providers — deleting all active jobs for user …`.
- `deleteJobAcrossAgents` deletes `/agents/jobs/<agent>/<user>/<jobName>` and
  `/agents/jobs/<agent>/queueInfo/<localJobName>`. The job's pods keep running
  but become orphaned: status polling, archetype re-evaluation and routing all
  break, and the registration cannot be recovered.
- `jake.jongejans@student.uva.nl`'s jobs were deleted this way. `Jorrit`'s and
  `evangelos.pipilikas`'s survived only incidentally (their
  `agentsWithThisJob` map was empty).

**Risk:** as written, one `sharedRules` PUT can wipe every job registration in
the cluster, VFL included.

**Mitigated 2026-09-28.** Job deletion on revocation is now **opt-in** via the
`POLICY_REVOCATION_DELETE_JOBS` environment variable (default: off). When off,
`processPolicyUpdate` logs a warning naming the user and the number of job
registrations it left alone, and returns without deleting.

An earlier idea — only delete on an explicit `RequestApproved == false` — was
rejected: in the observed failure the enforcer *did* evaluate and *did* deny
(`validProviders:0, invalidProviders:3`), so that test would not have prevented
anything. The real problem is G1, and until G1 is fixed a denial cannot be
trusted to mean "revoked". Flip the env var on once G1 is resolved and
revocation semantics are trustworthy.

### G3 — Stale job records are never cleaned up

`/agents/jobs/` accumulated ~37 entries from old runs (25 × `Jorrit`,
12 × `evangelos.pipilikas`). They are **not** removed by a clean reinstall:

- `configuration/uninstall-dynamos.sh` only runs `kubectl delete jobs --all`
  per agent namespace — Kubernetes Job objects, not etcd keys.
- etcd is a StatefulSet using `volumeClaimTemplates`
  (`charts/core/templates/etcd.yaml`) with no
  `persistentVolumeClaimRetentionPolicy`, so its PVCs survive `helm uninstall`;
  the namespaces also carry `helm.sh/resource-policy: keep`.

Every stale entry is re-evaluated (and, per G2, deletion-attempted) on each
`sharedRules` update. Manual cleanup if ever wanted:

```bash
kubectl -n core exec "$ETCD_POD" -c etcd -- \
  etcdctl --endpoints=http://127.0.0.1:2379 del /agents/jobs/ --prefix
```

Decision 2026-09-28: left alone for now.

### G4 — Both services crash-loop on startup (pre-existing, benign)

```
FATAL /app/pkg/lib/consume.go:34  Error on consume: rpc error: code = Unavailable
  desc = "dial tcp 127.0.0.1:50051: connect: connection refused"
```

The main container starts consuming before its sidecar's gRPC server is
listening; `startConsuming` calls `Fatalf`, the process exits, Kubernetes
restarts it. Typically settles after 2–4 restarts. Unrelated to this port —
it explains the non-zero `RESTARTS` column on orchestrator and policy-enforcer.

### G5 — `getJobAcrossAgents` returns an empty map for stale job records

The G2 guard logs `leaving 0 job registration(s) untouched` — `agentsWithThisJob`
is empty. This is also why `Jorrit`'s jobs survived the pre-guard storm while
`jake.jongejans`'s did not. Harmless while the guard is off, but it means
enabling `POLICY_REVOCATION_DELETE_JOBS` would still be a partial no-op for
these records. **Must be understood before that flag is ever switched on.**
Not investigated.

### G6 — Linkerd identity cert stops rotating, taking down the whole cluster

Symptom: **every** meshed pod goes `CrashLoopBackOff` at once (`api-gateway`,
`orchestrator`, `policy-enforcer`, `core`, agent namespaces), proxies logging:

```
Failed to obtain identity error=... invalid peer certificate: certificate expired
WARN linkerd_app: Waiting for identity to be initialized...
```

Cause: Linkerd issues each meshed pod a **24-hour** workload certificate. If the
`linkerd-identity` pod runs for days (and the host sleeps / the clock jumps), its
*own* proxy certificate stops rotating and it serves an expired cert, so nobody
can bootstrap an identity. The trust anchor and issuer certs are **not** the
problem — check before regenerating anything:

```bash
kubectl -n linkerd get secret linkerd-identity-issuer -o jsonpath='{.data.crt\.pem}' \
  | base64 -d | openssl x509 -noout -dates
```

Observed 2026-09-29: issuer valid until `Jun 18 2027`, but the expired peer cert
belonged to the `linkerd-identity` pod (up since 2026-09-24).

Fix — restart the control plane, **identity first**, then the workloads:

```bash
kubectl -n linkerd rollout restart deploy/linkerd-identity
kubectl -n linkerd rollout status deploy/linkerd-identity
kubectl -n linkerd rollout restart deploy/linkerd-destination deploy/linkerd-proxy-injector

kubectl -n core rollout restart deploy;  kubectl -n core rollout restart statefulset
kubectl -n core rollout restart daemonset
kubectl -n orchestrator rollout restart deploy
kubectl -n api-gateway rollout restart deploy
for ns in clientone clienttwo clientthree server; do kubectl -n $ns rollout restart deploy; done
```

No data is lost (verified: 19 `/policyEnforcer/` keys and 37 job records intact).
Transient `etcd` `MsgPreVote ... request timed out` messages during the restart
are normal while `etcd-1`/`etcd-2` are still coming back.

G4 amplifies this: `startConsuming` calls `Fatalf` when the sidecar's gRPC port
is unreachable, so the DYNAMOS containers crash-loop instead of waiting.

---

## Test results

### 2026-09-28 — steps 2 + 3 verified in-cluster

| Test | Result |
|---|---|
| Deploy | `make orchestrator` + `make sidecar`, both deployments rolled. |
| T1 — etcd populated | Pass. All expected `/policyEnforcer/*` keys present. |
| T2 — `updateEtc` re-seed | Pass. Deleted `eflintModels/VU` and `configs/VU` were both restored. |
| T4a — `PUT /policyEnforcer/sharedRules` | `200 OK` after the sidecar fix (was `504`). |
| T3 — `PUT /policyEnforcer/VU` | Pass. Commenting out `+steward-supports-archetype("VU","dataThroughTtp")` and PUTting made `allowed-clauses` return `supported_archetypes:["computeToData"]`; restoring the line returned `["dataThroughTtp","computeToData"]`. Policy changes now flow through the orchestrator end to end. |
| O12 — `checkJobs` key walking | Pass. `checkJobs: agreement="VU" found 0 user(s) with active jobs` — correct, no `/agents/jobs/VU/...` keys exist. |
| T4b — O13 + G2 guard | Pass. A `sharedRules` PUT fanned out across all stewards: **25** `leaving N job registration(s) untouched` warnings (matching the 25 `Jorrit` jobs under UVA), **0** `deleting all active jobs` lines, job count steady at **37**. Pre-guard, those 25 would all have hit `deleteJobAcrossAgents`. |
| T5 — invalid eFLINT → `400` | **Pass (2026-09-29).** `400 Policy update rejected by Policy Enforcer`. |
| T6 — `GET /policyEnforcer[/VU]` | **Pass (2026-09-29).** `200` with the legacy Agreement JSON for VU, read from the `/agreements/` sub-prefix — confirms O3. |
| T7 — VFL end to end | **Unblocked.** Policy enforcer approves the VFL request (`request_approved: true`, all four stewards valid). Full end-to-end run still to be done. |

---

## Validation tests

Re-runnable checks for everything built in steps 1–3. Assumes both
port-forwards are up (see [Useful commands](#useful-commands)) and:

```bash
ORCH_POD=$(kubectl -n orchestrator get pod -l app=orchestrator -o jsonpath="{.items[0].metadata.name}")
ETCD_POD=$(kubectl -n core get pod -l app=etcd -o jsonpath="{.items[0].metadata.name}")
```

> Port-forwards die whenever a pod is replaced. `curl: (52) Empty reply from
> server` almost always means "restart the forward", not "the service is down".

### T1 — etcd is populated by the orchestrator (step 1)

```bash
kubectl -n core exec "$ETCD_POD" -c etcd -- \
  etcdctl --endpoints=http://127.0.0.1:2379 get /policyEnforcer/ --prefix --keys-only
```

Expect `agreements/*`, `configs/{VU,UVA,RUG}`, `eflintLayer1/interface`,
`eflintRules/shared`, and one `eflintModels/<name>` per `.eflint` file.

### T2 — `updateEtc` re-seeds deleted keys (step 1)

```bash
kubectl -n core exec "$ETCD_POD" -c etcd -- etcdctl --endpoints=http://127.0.0.1:2379 del /policyEnforcer/eflintModels/VU
kubectl -n core exec "$ETCD_POD" -c etcd -- etcdctl --endpoints=http://127.0.0.1:2379 del /policyEnforcer/configs/VU
curl -i http://127.0.0.1:18082/api/v1/updateEtc
kubectl -n core exec "$ETCD_POD" -c etcd -- etcdctl --endpoints=http://127.0.0.1:2379 get /policyEnforcer/configs/VU
```

Expect `200 OK` and `configs/VU` restored as
`{"name":"VU","validationStrategy":"eflint","agreementLocation":"/app/eflint-models/VU.eflint"}`.

### T3 — policy change through the orchestrator (steps 2+3, the headline test)

```bash
# Baseline
curl -sS -G "http://127.0.0.1:18083/api/v1/policy-enforcer/allowed-clauses" \
  --data-urlencode "steward=VU" --data-urlencode "requester=Jorrit"
# -> supported_archetypes: ["dataThroughTtp","computeToData"]

# Comment out +steward-supports-archetype("VU","dataThroughTtp"). in
# configuration/eflint-models/VU.eflint, then:
curl -i -X PUT "http://127.0.0.1:18082/api/v1/policyEnforcer/VU" \
  -H "Content-Type: text/plain" --data-binary @configuration/eflint-models/VU.eflint
# -> 200 OK, after a short pause (the pause IS awaitPolicyEnforcerAck)

curl -sS -G "http://127.0.0.1:18083/api/v1/policy-enforcer/allowed-clauses" \
  --data-urlencode "steward=VU" --data-urlencode "requester=Jorrit"
# -> supported_archetypes: ["computeToData"]

# Restore the line and PUT again -> both archetypes return.
```

An *instant* reply instead of a pause means the ack path is not wired.

### T4 — shared rules + global re-evaluation (O13 and the G2 guard)

```bash
JOBS_BEFORE=$(kubectl -n core exec "$ETCD_POD" -c etcd -- etcdctl --endpoints=http://127.0.0.1:2379 get /agents/jobs/ --prefix --keys-only | grep -c .)

curl -i -X PUT "http://127.0.0.1:18082/api/v1/policyEnforcer/sharedRules" \
  -H "Content-Type: text/plain" --data-binary @configuration/eflint-models/02_agreement_rules.eflint
# -> 200 OK; then wait ~1.5s per running job while the burst drains

kubectl -n orchestrator logs "$ORCH_POD" -c orchestrator --tail=800 | grep -c "untouched"
kubectl -n orchestrator logs "$ORCH_POD" -c orchestrator --tail=800 | grep -c "deleting all active"
kubectl -n core exec "$ETCD_POD" -c etcd -- etcdctl --endpoints=http://127.0.0.1:2379 get /agents/jobs/ --prefix --keys-only | grep -c .
```

Expect: `untouched` > 0, `deleting all active` == **0**, job count == `$JOBS_BEFORE`.
A non-zero deletion count means the G2 guard has regressed.

### T5 — rejection path (O2)

```bash
curl -i -X PUT "http://127.0.0.1:18082/api/v1/policyEnforcer/VU" \
  -H "Content-Type: text/plain" --data-binary $'not valid eflint\n'
```

Expect `400 Policy update rejected by Policy Enforcer` and
`/policyEnforcer/eflintModels/VU` **unchanged** in etcd.
Empty body expects `400 request body is empty`.

Timeout path: `kubectl -n orchestrator scale deploy/policy-enforcer --replicas=0`,
PUT, expect `504` after ~30 s, then scale back to 1.

### T6 — GET regression (O3)

```bash
curl -i "http://127.0.0.1:18082/api/v1/policyEnforcer/VU"
curl -i "http://127.0.0.1:18082/api/v1/policyEnforcer"
```

Expect `200` with Agreement JSON, not `500`.

### T7 — VFL regression

First, confirm the enforcer approves the request (this passes as of 2026-09-29):

```bash
cat > /tmp/vfl.json <<'EOF'
{"user":{"id":"1234","user_name":"evangelos.pipilikas@student.uva.nl"},
 "data_providers":["clientone","clienttwo","clientthree","server"]}
EOF
curl -sS -X POST "http://127.0.0.1:18083/api/v1/policy-enforcer/validate" \
  -H "Content-Type: application/json" --data-binary @/tmp/vfl.json
# expect "request_approved": true and all four stewards in valid_dataproviders
```

Then run a normal `vflTrainModelRequest` end to end;
`clientone/clienttwo/clientthree/server` must still receive composition
requests. Note the requester **must** be
`evangelos.pipilikas@student.uva.nl` — it is the only user with relations in
`configuration/etcd_launch_files/agreements.json`.

### Deploy / redeploy

```bash
cd go && make orchestrator && make sidecar && cd ..
kubectl -n orchestrator rollout restart deploy/orchestrator deploy/policy-enforcer
kubectl -n orchestrator rollout status deploy/orchestrator
```

The sidecar image is shared by every pod, but SC1–SC3 are additive, so pods
still running an older sidecar keep working; restarting the two above is enough.

Do **not** verify a deploy by grepping the binary — `grep` on a stripped Go
binary is unreliable (`sharedRulesUpdate` and even `policyEnforcer-in` reported
0 matches in a binary that contained both). Use a functional probe instead.

---

## Next steps

Goal: finish the port and prove the VFL workflow still works end to end.
Test improvements are explicitly deferred.

VFL target behaviour and scenarios are specified separately in
[VFL_EFLINT_POLICY_SCENARIOS.md](VFL_EFLINT_POLICY_SCENARIOS.md).

1. **Deploy step 4.** S3/O15/O16/O17 are implemented but **not deployed**.
   `make orchestrator` + rollout, then re-run T3 and T4 — O15/O16/O17 all sit on
   the `chooseArchetype` / `chooseThirdParty` path that T3 exercises.
2. **Run T7 end to end** — the main goal, and scenario S1 of the VFL doc. The
   policy enforcer already approves the VFL request (verified 2026-09-29); what
   remains is driving a real `vflTrainModelRequest` through the api-gateway and
   confirming all four stewards receive composition requests. Requester must be
   `evangelos.pipilikas@student.uva.nl`.
3. **VFL scenarios S2/S3** — see the VFL doc. Needs eFLINT models for the four
   VFL stewards and a "server mandatory, ≥1 client" check.
4. **Replace `policyRemoval` / `policyReintroduction`** — the api-gateway still
   sends these message types, which the eFLINT enforcer does not handle
   (`unknown message type`). They become
   `PUT /api/v1/policyEnforcer/{steward}`, i.e. the endpoint step 3 delivered.
5. **Step 4 remainder — O14.** `manage_jobs.go`: per-agent revocation,
   `continue` instead of `return`, empty-routing-key guard. Entangled with the
   VFL branches, so do it as a focused change *after* T7 gives a known-good
   baseline to compare against.
6. **G5** (`getJobAcrossAgents` returns an empty map) — it produced the spurious
   `Jorrit` denials, and it must be understood before
   `POLICY_REVOCATION_DELETE_JOBS` is ever switched on. Likely related to the
   stale records in G3.
7. **Step 5 — P3 cleanup.** Delete `go/cmd/policy-enforcer/go.mod`, `go.sum` and
   the vendored `pkg/` copy (~7.5k lines). The Dockerfile already removes them
   at build time, so this is dead weight that will drift from `go/pkg`.
8. **Deferred, not blocking:** unit tests for the step-4 logic (O15/O16/O17 have
   no coverage), fixing the two order-flaky tests in `pkg/lib`, scripting
   T1–T7, **G3** (stale job records), and **O4** (multipart upload — without it
   `docs/VU_POLICY_CHANGE_QUICKCHECK.md` Option A does not work).
5. **Optional, only if wanted:** O4, the `POST /policyEnforcer/eflintModels`
   multipart upload. Without it, "Option A" in
   `docs/VU_POLICY_CHANGE_QUICKCHECK.md` does not work; that doc should either
   be updated to use the `PUT` flow or O4 should be ported.

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

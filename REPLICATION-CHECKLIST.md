# Fed-encrypt-dynamic replication checklist

Reference: completed deployment, instrumentation, experiment and plotting work
on `fed-bcd` and `semi-overlap-fed-bcd`. This checklist tracks only
`fed-encrypt-dynamic`; successful runs on other branches are not verification
of this branch.

Preserve the encrypted strategy, dynamic participation/policy behavior and
existing policy engine. Do not import eFLINT or replace the execution flow with
the semi-overlap pipeline. Retain the party, aggregator and authority services,
their configuration, permissions and key-management interactions.

## 1. Repository hygiene and build configuration

- [x] Confirm branch `fed-encrypt-dynamic` and inspect the worktree before editing.
  At the start only the carried-over checklist and experiment output were untracked.
- [x] Ignore generated `/experiments/` output without deleting prior results.
- [x] Set LF attributes for shell scripts and `dynamos.conf`.
- [x] Replace the author-specific root in `dynamos.conf` with checkout discovery;
  preserve an explicit `DYNAMOS_ROOT` override.
- [x] Set Docker Hub account `dynamos1` and deployment configuration default
  `DYNAMOS_IMAGE_TAG=fed-encrypt-dynamic`, retaining a tag override.
- [x] Make both Makefiles build/push the current branch tag by default, not
  `latest`; retain `BUILD_BOTH=1` and support `PUSH=0` in both.
- [x] Stop on Docker build/push failure and clean temporary build context
  after Docker commands.
- [x] Preserve Python `vfl` targets: `vfl-party`, `vfl-aggregator`,
  `vfl-authority`. Do not substitute the previous branch's training images.
- [x] Run mocked build tests covering default/both-tag/local-only modes,
  failure propagation, cleanup and all three encrypted VFL targets.
- [x] Verify root/tag overrides, current-branch build defaults, shell syntax
  and ignored output; `git diff --check` passes.
- [x] Document build defaults and the still-pending deployment tag wiring.

Build verification uses stubs; it does not build/push images or certify
Docker Hub authentication. Makefile tag overrides use `branch_name=<tag>`;
`DYNAMOS_IMAGE_TAG` is a deployment setting, not a build override.

## 2. Installation and removal

- [x] Port focused installer/uninstaller changes, not whole charts or strategy
  configuration. Preserve aggregator/authority namespaces, deployments, RBAC,
  storage and FABRIC resources.
- [x] Add the no-monitoring wrapper for local/FABRIC modes. Skipping monitoring
  must not uninstall an existing stack.
- [x] Add root/config preflight errors; verify the chart-generation helper
  location, quote arguments and invoke shell helpers through Bash.
- [x] Make NGINX installation upgrade-safe; inspect any obsolete etcd init Job
  before removing it, retaining PVC/configuration-copy behavior.
- [x] Wire account/tag overrides into gateway, orchestrator, agents, sidecars
  and generated runtime worker images in local and FABRIC charts.
- [x] Preserve per-service tag overrides; add the shared `MICROSERVICE_TAG`
  fallback and `fed-encrypt-dynamic` default in the agent.
- [x] Check image pull policy/cache handling for mutable branch tags; do not
  assume restarting an `IfNotPresent` deployment obtains a new build.
- [x] Delete agent Jobs before application releases and the namespace release
  last. Skip missing releases without unsupported Helm flags; propagate errors.
- [x] Document destructive cleanup and agreement-based namespace discovery,
  including encryption-specific services.
- [x] Run mocked install/uninstall tests, shell syntax checks, image-tag tests,
  an agent build and local/FABRIC chart rendering.

Verification details:

- The helper is `configuration/configure_dynamos.sh` on this branch, not the
  prior branch's FABRIC helper path.
- Generation preserves existing templates, including authority/aggregator
  storage; missing FABRIC participants are generated with namespaces and RBAC.
- Pull-policy changes were reverted at the user's request. Existing policies
  remain, including local `IfNotPresent`; update cached images explicitly or
  use immutable tags, rather than assuming a restart obtains a new build.
- The obsolete local init Job was removed, but the distinct FABRIC file-check
  Job, PVCs and configuration-copy helpers were retained.
- Fixed generated third-party RBAC to use the selected chart directory and
  pass the environment through namespace generation.
- Mock installation/removal passed for local/FABRIC, no-monitoring and regular
  modes, paths with spaces, tag overrides, absent releases and command failures.
- Real Helm rendering passed for five charts in each environment with default
  and custom account/tag values. Tests check every agent's runtime repository/tag
  variables, generation idempotence and preservation of existing templates.
- Tag-precedence tests, production agent build/vet, Bash syntax and
  `git diff --check` passed. Tag tests run on the isolated source files to avoid
  the agent package's existing eager Kubernetes-client initialization.

## 3. Trace the encrypted/dynamic flow before instrumenting

- [x] Trace gateway requests through party, aggregator and authority, including
  key setup/distribution, encryption, aggregation, decryption, training, accuracy
  and shutdown. Establish the actual stage order and reply semantics.
- [x] Determine local-step/round/Q and batch semantics rather than copying the
  semi-overlap 12-cycle/96-call expectation.
- [x] Identify asynchronous work, barriers, overlapping intervals and worker
  startup dependencies. Preserve the strategy's intended scheduling.
- [x] Trace dynamic participant removal/reintroduction, policy events, key or
  membership changes and coordination; establish what is measured synchronously.
- [x] Verify Python reply metadata preservation and chained sidecar behavior.
- [ ] Review the prior agent late-response deadlock and Python peer-startup
  fixes for applicability. Port with regression tests if these paths share
  the same bug; do not assume earlier branch fixes are present.
- [x] Check plain-username Job-name support before switching agreements to
  `requestor`; preserve email support and validate Kubernetes-compatible names.

## 4. Gateway and sidecar metrics

- [x] Port shared metadata/token correlation and asynchronous sidecar timer
  lifecycle/cleanup; accumulate chained durations instead of overwriting them.
- [x] Instrument approval, warmup/key setup, actual training/crypto stages,
  dynamic policy/membership coordination, final accuracy, shutdown and lifecycle.
- [x] Correlate metrics with request, round/stage, participant and membership
  where applicable; prevent races and misattribution after policy changes.
- [x] Record payload bytes, per-call RTT, measured handler-to-reply durations,
  transport/queue/middleware estimates, coverage and failures.
- [x] Keep wall time separate from summed concurrent stage/call times; use
  interval unions if this branch overlaps work.
- [x] Define request activity and reply-barrier waiting without equating them
  to compute utilization or claiming unmeasured policy work costs zero.
- [x] Retain complete result JSON and failure diagnostics; avoid logging keys,
  credentials, plaintext training data or large encrypted payloads.
- [x] Test successful and failed stages, missing/invalid timing, dynamic
  membership, concurrency and any asynchronous reply handling.
- [x] Run focused tests, builds and vet; run race tests where supported and
  explicitly record toolchain limitations.
- [ ] Run concurrency tests with a race-capable toolchain. Native Windows
  `go test -race` is blocked by disabled CGO.
- [ ] Verify deployed sidecar wire coverage, timings and dynamic-policy events
  with real encrypted workers; mocked flow tests do not establish live behavior.

Implementation/verification notes:

- The normal round has seven stages and ten calls: one batch sample, four
  parallel ciphertext extractions (three feature clients plus label server),
  MIFE key generation, feature decryption, accuracy/logistic error, SIFE key
  generation and sample decryption/weight update. `cycles` counts these rounds;
  no semi-overlap Q schedule is introduced.
- Accuracy is the party worker's batch fraction `[0,1]`, not the previous
  branch's percentage scale. It is not converted in the status JSON.
- Authority initialization precedes parallel party initialization and aggregator
  initialization. For the six-provider setup, each lifecycle phase has six calls
  before warmup retries. Per-round reinitializations are separately recorded.
- Existing Python callbacks invoke handlers and send replies on the same
  message, retaining metadata. The sidecar timer nevertheless survives dispatch
  return and expires unanswered requests, so it does not depend on synchronous
  dispatch completion.
- Active masks, including inactive parties and the label server, are preserved;
  membership snapshots are attached to results. Existing reinitialization remains
  at round boundaries. Policy mutation HTTP latency is recorded, not event
  propagation or eFLINT/reasoning time; `policy_check_measured=false`.
- Fixed tightly coupled gateway response-collection map races, checked previously
  ignored initialization/key-generation/decryption/shutdown errors, and rejected
  incomplete replies instead of accepting missing accuracy as zero.
- Tests exercise the production flow with HTTP mock workers, a parallel
  ciphertext barrier, every stage's failure, reinitialization, inactive masks,
  polling during updates, invalid requests, policy mutation timing and late policy
  delivery. Sidecar tests cover dispatch-return lifetime, upstream accumulation,
  duplicates, cancellation, invalid durations and 200 concurrent correlations.
- Gateway/sidecar tests passed ten consecutive runs; both production binaries
  built and passed vet. Chart tests still pass after restoring original pull
  policies. No Docker builds, image pushes or cluster changes were made.
- Approval is separate from post-approval lifecycle wall time. Handler timing
  includes serialization/setup waits and is not pure crypto CPU. Final accuracy
  is the last batch snapshot before the final weight update, not a separate
  final-model evaluation. No independent background completion is claimed.
- The gateway's existing parsed learning-rate variable is not applied to worker
  requests. Metadata labels it `requested_learning_rate`; optimizer behavior is
  unchanged, not silently corrected as part of timing.

## 5. Experiment runner, identities and plotting

- [x] Adapt the request trigger/runner to the actual encrypted strategy and
  existing dynamic-policy options. Preserve branch-specific behavior.
- [x] Switch all six agreements and the gateway policy-reintroduction payload
  to `requestor`/`GUID`; test plain-username and email Job-name support.
- [x] Use `requestor`/`GUID` and all six providers in the new experiment runner.
- [ ] Rebuild orchestrator/gateway and reload agreements before live tests;
  verify nonempty `requestor-<suffix>` Job IDs.
- [x] Choose valid smoke/full defaults from the traced algorithm. Validate
  expected rounds, stage/call counts, participants/membership and accuracies.
- [x] Save full results, logs, parameters, validation and CSVs under unique
  `experiments/fed-encrypt-dynamic/` directories.
- [x] Adapt plots for encryption/key management, dynamic participation and any
  overlap; separate lifecycle, wall time and summed per-call measurements.
- [x] Provide export-only validation and CSV generation for saved results.
- [x] Provide plot-only commands and test real PNG generation.
- [x] Surface recovered warmup failures as warnings; do not silently accept
  failed training, missing measurements or unavailable final accuracy.
- [x] Document timing/accuracy semantics and any unverified asynchronous
  completion. Metrics must not require the monitoring stack.
- [x] Test polling, failures/timeouts, parameter validation and CSV exports.
- [x] Test encrypted-specific plots after implementing the plotter.

Runner verification:

- `scripts/run_fed_encrypt_experiment.py` defaults to one 12-round run,
  batch=64, no policy mutations; use `--cycles 1` for the smoke test.
- Uses only the standard library, existing gateway and an optional owned,
  readiness-checked port-forward. No install/uninstall or cluster cancellations.
- Retains submission/request ID, latest pending result, logs and manifest
  failure state. Never retries submissions or advances after a failed run.
- Validates six-provider lifecycle, ten calls per normal encrypted round,
  four-party membership/masks, fraction-scale accuracy, coverage, interval
  arithmetic and agreement between root and nested metrics.
- Exposes optional removal/reintroduction rounds but limits mutation tests to
  one run; restore agreements before repeating. Event application is explicitly
  unverified. Arbitrary new-party topologies are rejected by this validator.
- Verified with Python regression tests and exported production Go gateway
  flow output driven by mock HTTP workers, not just hand-built JSON.
- A completed 120-round live run validated successfully; it took about eight
  minutes. CSV and plots can be regenerated offline from its saved `result.json`.
- Five PNGs are generated for accuracy, round duration, stage duration, active
  participants and lifecycle duration. Matplotlib is declared separately in
  `scripts/requirements-experiment.txt`.
- Pylance could not locate the new workspace file; Python execution/import and
  regression tests provide syntax/runtime verification instead.

## 6. Build, deploy and verify

- [ ] Build/publish required Go services and encrypted Python images under
  `dynamos1` with tag `fed-encrypt-dynamic`; check Docker/WSL and push permissions.
- [ ] Install without monitoring; verify agreements, registration, gateway,
  authority/aggregator availability and actual image digests, including Jobs.
- [ ] Capture fresh worker startup logs/restart counts before Job cleanup.
- [ ] Run and retain a one-round smoke result, then the full experiment.
- [ ] Verify all expected calls, stages, participants, accuracy snapshots,
  timing arithmetic and shutdown against saved JSON and all generated plots.
- [ ] Exercise intended policy removal/reintroduction and cryptographic
  membership transitions separately from the no-policy-change baseline.
- [ ] Repeat comparable runs for performance claims; report startup separately.
- [ ] Update README with verified commands, defaults and remaining limitations.
- [ ] Review final diff before committing; exclude this checklist if desired,
  experiment output, credentials and temporary contexts. Review regenerated
  protobuf files separately rather than reverting user-generated changes.

## Current handoff

Repository hygiene, root/account defaults, reliable image builds, installation/
removal scripts, local/FABRIC charts and runtime image selection are implemented.
Gateway/sidecar timing and requestor agreements/Job-name support are implemented
and locally verified. The experiment runner, CSV export and encrypted-specific plotter are implemented.
Prior agent/Python startup fixes remain pending.
Identity regression tests and the orchestrator build/vet
passed; reload agreements and rebuild the orchestrator before using requestor.
No images have been built/pushed and no cluster resources changed for this branch.

Validation: `bash scripts/tests/image-build-test.sh` passed under WSL, covering
both Makefiles, all three encrypted VFL targets, root/tag overrides, build/push
failure propagation and Docker-stage cleanup. Bash syntax checks passed for the
edited scripts and configuration. `bash scripts/tests/deployment-test.sh` and
`bash scripts/tests/chart-test.sh` passed; the latter used a checksum-verified,
session-local Helm 3.17.3 because Helm was unavailable on the native PATH.
Next: live smoke verification using the new shorter default. The pending
agent/Python startup compatibility checks from section 3 remain relevant before
live execution.

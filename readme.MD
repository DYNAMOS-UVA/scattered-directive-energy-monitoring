# Scattered Directive

Welcome to Scattered Directive. This is an automated and reproducible framework to deploy the digital data marketplace
platform [DYNAMOS](https://github.com/Jorrit05/DYNAMOS) on the [FABRIC](https://portal.fabric-testbed.net) Research
Infrastructure.

Scattered Directive automates the entire setup, from requesting nodes to configuring and deploying DYNAMOS with the
relevant relations to the nodes all defined. This allows researchers and developers (you!) to utilise DYNAMOS without
any hassle.

To prove our implementation of Scattered Directive, we utilised it to implement a Vertical Federated Learning workload,
creating a new archetype to support shared execution of the global model training on a trusted third party. This
archetype is aptly named *Shared Execution via TTP*.

## Setup

Practically everything is automated. The only thing you need to do is open the FABRIC Jupyter Notebook, clone this
repository into there, open `fabric/dynamos.ipynb` configure the preferred configuration of nodes and execute.

### Branch build configuration

Source `dynamos.conf` through Bash. It discovers the checkout root automatically;
set `DYNAMOS_ROOT` before sourcing it to override the root. Docker images use
account `dynamos1`. Both Makefiles publish the current Git branch tag by default
(`fed-encrypt-dynamic` on this branch), not `latest`.

```bash
make -C go api-gateway sidecar
make -C python vfl
# Build locally without pushing:
make -C go agent PUSH=0
# Also publish latest when explicitly requested:
make -C python vfl BUILD_BOTH=1
```

The Python `vfl` target retains this strategy's `vfl-party`, `vfl-aggregator`
and `vfl-authority` images. Build/push failures stop immediately.
`DYNAMOS_IMAGE_TAG` defaults to `fed-encrypt-dynamic` for deployment configuration;
the installer passes it to service charts and runtime worker/sidecar settings.
Makefile tag overrides use `branch_name=<tag>`; do not
assume the deployment override changes the build tag.

### Installation without monitoring

Run from Bash/WSL after building and publishing the required images:

```bash
bash configuration/dynamos-configuration-no-monitoring.sh local
# Or use the FABRIC charts:
bash configuration/dynamos-configuration-no-monitoring.sh fabric
# Explicit deployment tag override:
DYNAMOS_IMAGE_TAG=my-tag bash configuration/dynamos-configuration-no-monitoring.sh local
```

The regular `dynamos-configuration.sh <local|fabric>` still installs monitoring;
its optional `--no-monitoring` flag is equivalent to the wrapper. Skipping
monitoring leaves an existing monitoring installation untouched. The installer
uses the helper at `configuration/configure_dynamos.sh` and keeps existing
participant templates, including authority/aggregator storage and permissions.
New participant and third-party templates receive the same image settings in
both environments. The obsolete local etcd init Job is removed; its PVC and
configuration transfer remain. The FABRIC file-verification Job is retained.

Runtime workers use a service-specific `<UPPERCASE_SERVICE_NAME>_TAG` first,
then `MICROSERVICE_TAG`, then `fed-encrypt-dynamic`. Runtime worker and sidecar
repositories follow the chart account; standalone agents default to `dynamos1`.
Existing image pull policies are preserved, including `IfNotPresent` on local
agent/sidecar deployments. Republishing a branch tag can leave cached images
running even after restarting; refresh the intended images on each node or use
immutable tags for reproducible experiments.

### Removal and validation

```bash
bash configuration/uninstall-dynamos.sh
# If application releases were installed outside the default Helm namespace:
HELM_NAMESPACE=my-namespace bash configuration/uninstall-dynamos.sh
```

**Destructive:** removal deletes all Jobs in participant namespaces discovered
from `configuration/etcd_launch_files/agreements.json`, including authority and
aggregator, then removes application/monitoring releases and chart-managed
storage. Namespaces carrying Helm's `keep` policy can remain; do not assume PVC
or data recovery. Keep the agreements file available until cleanup finishes.
Application releases use `${HELM_NAMESPACE:-default}`, monitoring uses
`monitoring`, and NGINX uses `ingress` (legacy application-namespace NGINX is
also checked). Missing releases are skipped; other command errors stop cleanup.
The namespace release is removed last.

Local regression checks do not install anything into the cluster:

```bash
bash scripts/tests/image-build-test.sh
bash scripts/tests/deployment-test.sh
bash scripts/tests/chart-test.sh  # Requires Helm, or set HELM=/path/to/helm
cd go
go test cmd/agent/image_tags.go cmd/agent/image_tags_test.go
go build ./cmd/agent
go vet ./cmd/agent
```

Generated experiment output under `experiments/` is ignored. Track replication
progress and remaining validation in [REPLICATION-CHECKLIST.md](REPLICATION-CHECKLIST.md).

## Encrypted/dynamic VFL timing

All six provider agreements and the gateway's policy-reintroduction payload use
username `requestor`, user ID `GUID`. Job naming supports plain usernames and
email identities. Rebuild the orchestrator as well as gateway/agent/sidecar
images, and reload the updated agreements before submitting as `requestor`;
an old orchestrator can otherwise generate an empty Job ID.

The gateway status JSON retains per-round results and now includes
`results[].timing` and run-level `metadata.timing`. These metrics require rebuilt
gateway and sidecar images, including the sidecar used by newly created worker
Jobs. No monitoring stack is required.

The instrumented strategy is not the semi-overlap pipeline:

1. Warm up all authorized providers.
2. Initialize the authority's MIFE/SIFE keys, initialize the parties in parallel
   (including the label server), then initialize the aggregator's weights.
3. Each round samples a batch, extracts party ciphertexts in parallel, generates
   MIFE decryption keys at the authority, decrypts features at the aggregator,
   calculates batch accuracy/logistic error at the server, generates the SIFE
   decryption key, then decrypts samples/updates weights at the aggregator.
4. Existing dynamic-policy events update the active-party mask. Membership
   additions trigger the existing reinitialization path at a round boundary.
5. Shut down all authorized providers.

For three feature clients plus the label server, a normal round has **10 HTTP
calls across seven stages**. Warmup, initialization and shutdown each have six
calls with the current six-provider configuration, excluding warmup retries.
Do not reuse semi-overlap Q/cycle/call-count assumptions. `results[].clients`
preserves this branch's active-party count, including the label server;
`results[].participants` records the membership snapshot used for that round.

### Fields and interpretation

- Each call records participant/stage, monotonic start/end offsets, round trip,
  JSON payload bytes and failures. Missing/invalid sidecar timing is explicitly
  unmeasured, not zero. Failed/invalid replies remain in timing diagnostics.
- Sidecars preserve a correlation token until the reply, even if dispatch
  returns first; timers expire after 60 seconds. Chained sidecar measurements
  accumulate rather than overwrite upstream durations.
- `algorithm_ms` / wire metadata `dynamos_algorithm_ms` mean **handler-to-reply
  elapsed time**, including worker setup waits, serialization and handler work.
  They are not pure encryption/decryption CPU measurements.
- `communication_ms` is measured-call RTT minus handler time. It includes
  transport, queues and middleware, not just network latency.
- `phase_order` and `phases` retain the actual stage identifiers. Run keys are
  prefixed with lifecycle or `round/<index>` to prevent overwritten rounds.
  Reinitialization has a separate summary within its round.
- Stage elapsed sums, interval unions, overlap and `outside_stage_ms` are
  separate. The residual is gateway time outside stage envelopes, not a pure
  coordination CPU measurement. Participant request-active time uses interval
  unions; no outstanding request does not prove idle compute.
- `reply_barrier_wait_ms` sums early participants' waits for the last reply in
  a parallel stage; it is not additional wall time.
- Lifecycle summaries separate approval, warmup, initialization, training and
  shutdown. `total_ms` includes approval; the root `duration_ms` starts after
  approval. `training_ms` includes round reinitializations and policy mutation
  requests. The final accuracy is the last round's batch snapshot, calculated
  before that round's final sample-decryption/weight-update stage, not a
  separate final full-model evaluation. Accuracy retains this worker's fraction
  scale `[0,1]`; multiply by 100 only when displaying percentages.
- `requested_learning_rate` records the parsed request value, not a verified
  effective optimizer setting; this branch's existing gateway does not apply
  that variable to the worker requests. Timing instrumentation does not change it.
- `policy_actions` measures only the HTTP removal/reintroduction requests.
  Event delivery, reasoning and asynchronous policy handling are **not**
  separately measured; `policy_check_measured=false` does not mean zero cost.
  These records do not prove the event was applied before the next stage.
- `background_completion_verified=false`: reply completion is not an
  independent proof of all possible background work finishing.

Initialization, training-stage and shutdown errors fail the run explicitly;
missing accuracy is not accepted as a zero-accuracy success. Parallel response
collection is synchronized, polling uses stable result snapshots, and policy
subscriptions stop when the run finishes.

```bash
make -C go api-gateway sidecar
# Local regression checks:
cd go
go test ./cmd/api-gateway ./cmd/sidecar
go vet ./cmd/api-gateway ./cmd/sidecar
```

Replace existing gateway/sidecar Pods after publishing and ensure newly created
Jobs use the rebuilt sidecar. Retained `IfNotPresent` policies require explicit
cache/image management. The encrypted runner is documented below; a dedicated plotter and live timing
verification are still pending.

## Encrypted experiment runner (no monitoring stack)

Use the already-deployed installation with the current gateway and job-sidecar
images and reloaded `requestor` agreements. This does not install/uninstall,
restart services, or cancel submitted cluster work. Python's standard library
is sufficient; no extra packages are needed.

```bash
# Start with one real encrypted round:
python3 scripts/run_fed_encrypt_experiment.py --cycles 1
# Default: one run, 12 actual rounds, batch=64, policy changes disabled:
python3 scripts/run_fed_encrypt_experiment.py
# Repeated baseline runs:
python3 scripts/run_fed_encrypt_experiment.py --runs 3
# Use an existing externally forwarded gateway:
python3 scripts/run_fed_encrypt_experiment.py --api-base-url http://localhost:8080/api/v1 --no-port-forward
# Optional policy mutation test; round indices are zero-based:
python3 scripts/run_fed_encrypt_experiment.py --cycles 12 --policy-removal 4 --policy-reintroduction 8
```

The default is 12 encrypted rounds (one tenth of the previous 120-round
default); specify `--cycles 120` or another value for a longer run. The runner
probes the gateway and, only for the default URL when unavailable,
starts an owned `kubectl port-forward`. It checks readiness and stops only that
process on exit. Custom URLs must already be reachable. Requests use all six
providers and `requestor`/`GUID`. Unlike semi-overlap, `--cycles` is the actual
number of encrypted training rounds; there is no Q option. The runner does not
send a learning-rate override because the current gateway does not apply it.

Default polling is every five seconds with a three-hour deadline per run and
a 30-second timeout per HTTP request. Override with `--poll-interval`,
`--timeout` and `--http-timeout`. A timeout or transport error does **not** prove
the request stopped: inspect the saved request ID and gateway status before
resubmitting. Submissions are never automatically retried, and a failed run
stops later repetitions.

Each unique directory under `experiments/fed-encrypt-dynamic/` contains:

- `experiment.json`: parameters, status, timestamps and failure information.
- An owned port-forward log when one was needed.
- Per-run request/submission JSON, `request.log` and the latest valid
  `result.json` (saved on every poll, including pending status).
- `metrics/validation.json`, raw timing JSON and CSVs for the run, rounds,
  phases, calls, participants and policy mutation requests, plus PNG plots for
  accuracy, per-round duration, per-stage duration, active participants and
  lifecycle duration. Install the plotting dependency with
  `python3 -m pip install -r scripts/requirements-experiment.txt` if needed.

Validation checks the ordered rounds, accuracy fraction `[0,1]`, active-party
snapshots, seven-stage/ten-call round schedule, initialization/shutdown,
sidecar coverage and timing/byte arithmetic. Run and nested phase metrics must
agree. Recovered warmup failures produce warnings; missing successful-call
timing and training/shutdown failures are not silently accepted. The initial
configuration must have the three feature clients, label server, authority and
aggregator; arbitrary new participant topologies are not supported by this
runner's validator.

Policy mutation tests are limited to one run at a time: they change persistent
agreements, so restore/reload the intended agreements before repeating.
Successful mutation acknowledgments do not prove when the active mask changed;
inspect saved membership snapshots and worker/gateway logs. Startup retries and
safe late-response fixes from the previous branch remain pending compatibility
work here; a failed warmup must be investigated, not automatically resubmitted.

Revalidate/export a saved result without contacting the cluster:

```bash
python3 scripts/run_fed_encrypt_experiment.py --export-result experiments/fed-encrypt-dynamic/<experiment>/run-001/result.json
python3 -m unittest discover -s scripts/tests -p test_fed_encrypt_experiment.py
```

Regenerate CSVs and plots without contacting the cluster:

```bash
python3 scripts/run_fed_encrypt_experiment.py --export-result experiments/fed-encrypt-dynamic/<experiment>/run-001/result.json
```

Regenerate just the plots from a validated saved result:

```bash
python3 scripts/run_fed_encrypt_experiment.py --plot-result experiments/fed-encrypt-dynamic/<experiment>/run-001/result.json
```

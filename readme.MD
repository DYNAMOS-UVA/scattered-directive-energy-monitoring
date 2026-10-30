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

### Local or FABRIC deployment from Bash

From the repository root, with Docker, Helm and kubectl configured:

```bash
make -C go all
make -C python vfl
bash configuration/dynamos-configuration-no-monitoring.sh local
```

Use `fabric` instead of `local` for FABRIC charts. To install monitoring too,
use `bash configuration/dynamos-configuration.sh local`. The no-monitoring
wrapper skips monitoring installation without removing an existing stack.

`dynamos.conf` detects this checkout automatically. If an exported
`DYNAMOS_ROOT` points to another checkout, unset it before installing.
The installer selects Docker Hub account `dynamos1` and image tag
`semi-overlap-fed-bcd` for application services and dynamically spawned workers,
including job sidecars. Override the deployment tag with `DYNAMOS_IMAGE_TAG`.
Per-service `*_TAG` agent environment overrides take precedence over the shared
`MICROSERVICE_TAG`; rebuild the agent image to deploy this tag-selection change.

Both Makefiles build and push the current Git branch tag by default.
`BUILD_BOTH=1` additionally publishes `latest`; `PUSH=0` disables pushing.
Build and push errors stop execution, preventing a failed build from pushing
an older local image. Ensure Docker/WSL integration and Docker Hub credentials
are working and that your account can push to `dynamos1`. Never put credentials
in source or logs. For the first deployment, publish all Go services and the
two VFL Python images; later rebuilds can target only changed services.
Ensure the cluster does not reuse cached older images with the same tag.

To uninstall:

```bash
bash configuration/uninstall-dynamos.sh
```

This is destructive: it deletes agent Jobs, uninstalls application, monitoring
and NGINX releases, then removes the namespace release and its namespaces/PVCs.
Missing releases are skipped; other Helm/kubectl errors stop cleanup.
Application releases are looked up in `${HELM_NAMESPACE:-default}`.
The agreements file is required to discover agent namespaces for Job deletion.
The local etcd chart omits the obsolete initialization Job; the existing PVC
helpers populate configuration instead.

These changes do not import eFLINT or serialize the semi-overlap training pipeline.
The experiment runner and plotter use gateway/sidecar results only;
see [the replication checklist](REPLICATION-CHECKLIST.md) for deployment checks.

### Semi-overlap gateway and sidecar timing

Timing is included in the existing training-status JSON; no monitoring stack is
required. Rebuild/publish the two changed Go images and redeploy the gateway
before starting a new job:

```bash
make -C go api-gateway sidecar
```

The default image tag is `semi-overlap-fed-bcd`. Existing worker Jobs keep their
old sidecars; use newly created Jobs and ensure the cluster pulls the rebuilt
images rather than reusing cached images with the same tag. No Python image
changes are needed for this instrumentation.

The gateway keeps the branch's four pipelined stages: `server_sample`,
`client_forward`, `server_aggregate` and `client_update_dispatch`. Different
stages and cycles can overlap. `cycles` still specifies requested local steps:
the gateway schedules `floor(cycles / communication_frequency)` synchronization
cycles (for example, 120 steps with Q=10 produces 12 cycle results). Metadata
records both the requested and scheduled work.

Each result contains `timing` from sampling start through all client update
**replies**, including queue waits. Run-level `metadata.timing` includes:

- Pipeline `duration_ms`, stage/cycle identifiers, start/end offsets and per-stage
  participant calls, payload bytes, round trips and failure/measurement coverage.
- `stage_elapsed_sum_ms` (sum, potentially larger than pipeline wall time),
  `stage_union_ms` (wall time covered by at least one stage),
  `stage_overlap_ms` (sum minus union), and `outside_stage_ms` (wall minus union).
  The overlap value counts extra concurrency, not necessarily distinct wall
  time with overlap. Outside-stage time is **not** gateway CPU time or a
  sequential coordination-overhead estimate.
- Participant `request_active_ms` (union of outstanding HTTP-call intervals),
  `request_idle_ms` (time with no outstanding gateway request), and
  `request_elapsed_sum_ms`. These are request activity, **not** compute activity:
  workers can train in the background when no request is outstanding.
- Per-phase `barrier_wait_ms` and run-level `reply_barrier_wait_ms`: summed
  early-reply waiting until the final participant reply in each phase.
  These participant-time sums are not additive pipeline wall time.
- `enqueue_ms` through submission of the final sample, `drain_ms` from then to
  the last update reply, and `fill_ms` through the first cycle's update replies.
  Fill can overlap enqueue or drain; do not stack all three as separate costs.
- Separate `approval_ms`, `warmup`, `final_accuracy`, `shutdown` and lifecycle
  `total_ms`. Pipeline call/byte totals exclude those separate lifecycle phases.
  Shutdown reply failures are visible in its per-participant failure counters.

The sidecar correlates the local handoff with the worker's later reply using a
unique metadata token. A successful dispatch RPC does not discard the timer:
Python handlers return immediately after spawning threads. Unanswered timers
expire after the agent's 60-second HTTP deadline. Chained sidecars accumulate
local handler-to-reply durations so a final relay does not overwrite the earlier
training handler's timing. Python's existing reply helper preserves metadata.

For compatibility, the duration is named `dynamos_algorithm_ms` in replies and
`algorithm_ms` in summaries. It measures **handler-to-reply latency, not pure CPU
time or necessarily all local training**. `communication_ms` is the HTTP round
trip minus that measured latency; it includes middleware, queues and transport.
These are sums over measured calls, not a decomposition of overlapped wall time.
Missing or invalid timing is counted as `unmeasured_calls`, not zero compute.
Policies remain event-driven: `policy_check_measured=false` means their
background work is not timed, not that policy enforcement costs nothing.

**Completion limitation:** client descent replies acknowledge asynchronous
dispatch. The server replies with gradients before its remaining local updates,
and final accuracy retrieval reads a snapshot without joining that background
thread. Draining the gateway pipeline therefore does not prove that all training
has finished. This instrumentation preserves that protocol rather than adding
new synchronization. Missing final cycle accuracies or stage errors now produce
`failed` status and explicit diagnostics; even `done` confirms reply processing
and available accuracies, not independently verified background completion.

Focused Go tests cover overlap accounting, real pipeline concurrency, gradient
correlation, reply barriers, failed-stage draining, final accuracy validation,
concurrent status polling and delayed sidecar replies. Live-cluster timing
coverage and training-completion validation remain deployment checks.

### Run an experiment and generate plots

From the repository root in Bash/WSL, with Python 3, curl, kubectl and the
timing-enabled deployment available:

```bash
python3 -m pip install -r plots/requirements.txt
python3 scripts/run_semi_overlap_experiment.py
```

The default is **one run, 120 local steps, Q=10, batch size 64**: 12
synchronization-cycle results and **96 measured pipeline calls**
(`12 * (1 sample + 3 forward + 1 aggregate + 3 update dispatch)`).
Approval, warmup, final accuracy retrieval and shutdown are separate lifecycle
measurements, not part of those 96 calls.

The runner probes the API and starts a temporary gateway port-forward only if
the default localhost endpoint is unavailable; it stops only the port-forward
it created. For an existing endpoint, override `API_BASE_URL`, for example:

```bash
API_BASE_URL=http://localhost:8080/api/v1 python3 scripts/run_semi_overlap_experiment.py
```

For just a short smoke request without CSV export or plotting:

```bash
bash scripts/single_request.sh
```

Its default is 10 local steps/Q=10 (one synchronization cycle), saving
`experiments/semi-overlap-fed-bcd/smoke-result.json`. Environment variables
`CYCLES`, `COMMUNICATION_FREQUENCY`, `SAMPLE_BATCH_SIZE` and `OUTPUT_FILE` override
those settings. `POLL_INTERVAL_SECONDS` and `MAX_POLLS` control polling; the
runner defaults to 2160 polls, five seconds apart. Each HTTP call also has its
own timeout, so that poll count is not an exact wall-clock deadline.

It uses the `requestor` identity (user ID `GUID`) from
[the deployed agreements](configuration/etcd_launch_files/agreements.json).
After switching from the previous email identity, rebuild/publish and redeploy
the orchestrator (plain-username Job naming) and gateway (policy reintroduction
payload), and reload the updated agreements into the running configuration.
Editing the local JSON alone does not update a running installation.
The warmup timeout/late-response fix also requires a rebuilt agent and both VFL
Python images; see the recovery steps below and
[the replication checklist](REPLICATION-CHECKLIST.md).
It does not install/uninstall anything or collect Prometheus/energy metrics.
To change the work requested:

```bash
python3 scripts/run_semi_overlap_experiment.py \
  --cycles 120 --communication-frequency 10 --sample-batch-size 64
```

Q must be at least 2: the server's `Q-1` local-update loop does not produce an
accuracy at Q=1. The experiment runner requires local steps to be an exact
positive multiple of Q, rather than silently truncating the request as the
gateway would. `--runs` selects repeated sequential runs; the default is 1.
The runner waits for gateway terminal status, **not an additional compute
completion barrier**.

Output is kept under a unique
`experiments/semi-overlap-fed-bcd/<UTC-timestamp>-<suffix>/` folder (Git-ignored):

- `experiment.json`: parameters, timestamps, status and completion caveat.
- `run-001/request.log` and `result.json`: request/polling log and full gateway
  status. Each valid poll saves the latest snapshot, including pending status
  if a run times out.
- `run-001/metrics/`: `run.json`, `run.csv`, `rounds.csv`, `phases.csv`,
  `calls.csv`, `participants.csv` and `validation.json`. Stage/call CSVs use
  run-level phases, retaining failed cycles absent from completed results and
  avoiding duplicate copies of each cycle's calls. `scope` distinguishes
  pipeline/lifecycle rows or pipeline/cycle participant summaries.
- `run-001/plots/pipeline-timeline.png`: real stage intervals on the gateway's
  clock, showing overlap between synchronization cycles.
- `run-001/plots/timing-summary.png`: nonoverlapping wall coverage, lifecycle
  components, cycle reply latencies and final accuracy snapshots.
- `run-001/plots/request-metrics.png`: request activity, measured handler/
  middleware sums, summed stage time and JSON payload sizes.

Validation checks the requested schedule, cycle/stage IDs, all three clients,
final accuracies, expected calls, timing coverage, stage interval arithmetic
and lifecycle failures. Successful validation still explicitly records
`background_completion_verified=false`. It does not claim that replies measure
all background training. No synchronous policy cost is fabricated.
Recovered warmup failures are retained in counters and recorded as explicit
validation warnings when a subsequent attempt has a measured reply. Missing
timing on successful calls, unrecovered warmup failures, or any failed training,
final-accuracy or shutdown call still fail validation.

On failures the runner exits nonzero and preserves available JSON/CSV
diagnostics and the failure manifest. Inspect `request.log` and
`metrics/validation.json`; a request timeout does **not** cancel the cluster job.
Do not blindly resubmit while that job may still be active. A missing final
accuracy can expose the existing asynchronous snapshot limitation, not an
installation failure.

Regenerate metrics without submitting another request:

```bash
python3 scripts/run_semi_overlap_experiment.py --export-result path/to/result.json
```

Generate plots directly from an existing result:

```bash
python3 plots/plot_semi_overlap_timing.py path/to/result.json
```

The plot-only command can visualize available failed-run data, clearly labeled
with its status; it is not a replacement for validation. A result without any
completed cycle/accuracy data cannot be plotted. Both commands default to sibling
`metrics/` or `plots/` folders beside the saved JSON.

### Recover from cold-start warmup timeouts

The agent now registers a buffered response channel before dispatch, removes
the correlation on every HTTP-handler exit, and delivers replies without a
blocking send. Late or duplicate replies are logged rather than stalling the
shared message consumer. The Python library now waits up to 120 seconds for
peer health instead of giving up after seven one-second retries; health RPCs
have their own deadline bounded by the remaining startup time. An unhealthy
peer still causes an explicit startup error once that deadline expires.
The agent HTTP deadline remains 60 seconds; a cold startup can therefore need
another warmup attempt, without poisoning the agent's reply consumer.

Build/publish the fixes from Bash/WSL:

```bash
make -C go agent
make -C python vfl
```

Both Python images embed the shared library, so both need rebuilding. Redeploy
the agents and start fresh worker Jobs using the rebuilt
`semi-overlap-fed-bcd` images, ensuring cached mutable tags are not reused.
Already-blocked agents cannot recover merely by publishing an image: they need
replacement after the current experiment has ended. The `requestor` change
additionally requires the orchestrator/gateway rebuild and agreement reload
described above. These source changes do not automatically deploy anything.

Then run the one-cycle smoke request before the full experiment. The fixes were
validated locally with repeated agent response-routing tests, Python startup
tests and real gRPC health/deadline tests; live cold-start verification remains
necessary. The agent package initializes an in-cluster Kubernetes client on
startup, so local response-routing tests were run in isolation from that
initialization; the full production agent build and `go vet` were also checked.

Practically everything is automated. The only thing you need to do is open the FABRIC Jupyter Notebook, clone this
repository into there, open `fabric/dynamos.ipynb` configure the preferred configuration of nodes and execute.

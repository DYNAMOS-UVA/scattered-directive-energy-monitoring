<div align="right">
  <a href="https://github.com/Apipilikas/scattered-directive-energy-monitoring/tree/main">
    <strong>🔙 Return to <code>main</code> branch</strong>
  </a>
</div>

<div align="center">

# Scattered Directive Energy Monitoring
### Branch: `fed-bcd`

*This branch contains the Fed-BCD implementation.*

</div>

---

## Overview

### Installing and uninstalling this branch

Run from the repository root in Bash/WSL with Docker, Helm and kubectl configured.
Build and publish this branch's images before deploying:

```bash
make -C go all
make -C python vfl
bash configuration/dynamos-configuration-no-monitoring.sh local
```

Use `fabric` instead of `local` for FABRIC charts. To include the monitoring
stack, use `bash configuration/dynamos-configuration.sh local` instead.
The no-monitoring wrapper skips monitoring installation; it does not remove
an existing stack.

`dynamos.conf` detects this checkout automatically. Unset an old `DYNAMOS_ROOT`
override if it points elsewhere. Docker Hub defaults to `dynamos1`, and the
installer sets application and dynamically spawned worker images to `fed-bcd`.
`DYNAMOS_IMAGE_TAG` overrides that deployment tag; builds use the current Git
branch name. Per-service `*_TAG` agent overrides take precedence over the shared
`MICROSERVICE_TAG`. Rebuild the agent image to deploy this tag-selection change.
No eFLINT or baseline training logic is imported by these installer changes.

Both Makefiles build and push only the current branch tag by default.
`BUILD_BOTH=1` additionally builds/pushes `latest`; `PUSH=0` disables pushing.
Build failures stop immediately without pushing an older local image, and push
failures are propagated. If Docker reports `error getting credentials` under
WSL, check that Docker Desktop is running with integration enabled for that
WSL distribution, then run `docker login` in the same WSL terminal and verify
your account has push permission for `dynamos1`. Do not paste credentials into
logs or source files.

To uninstall:

```bash
bash configuration/uninstall-dynamos.sh
```

This destructively removes agent Jobs, application releases, monitoring/NGINX
releases and finally namespaces (including their PVCs) in the current cluster.
Missing releases are skipped; other Helm/kubectl errors stop the script.
Application releases are looked up in `${HELM_NAMESPACE:-default}`.
The agreements file is required to discover agent namespaces for Job cleanup.
The local etcd PVC chart no longer contains the obsolete initialization Job;
configuration is populated by the existing PVC helper.

Here we provide a brief overview of the Fed-BCD implementation. More details can be found in thesis document.

### FedBCD Architecture
FedBCD targets feature-partitioned FL scenarios, where multiple parties collaborate to train a shared model without exposing their raw data. The vanilla stochastic gradient descent algorithm requires constant communication of intermediate results at the end of every iteration, creating communication congestion, especially when the number of participants is very large. To resolve this, FedBCD proposes that each party performs Q consecutive local gradient updates before synchronizing their results with the party K.

### Api-gateway Modifications
First of all, two new parameters were introduced on the `data_request` payload of `requestApproval` request: `communication_frequency` and `sample_batch_size`. The api-gateway acts as the main orchestrator of the VFL pipeline, serving as a proxy between the clients and the server to ensure no direct peer-to-peer communication. 

The FedBCD execution pipeline consists of the following steps:

1. **Mini-batch sampling :** The api-gateway sends a `vflSampleBatchRequest` request to the server to randomly select a mini-batch sample for the current iteration. The server returns only an array of the sampled indexes, eliminating possible raw data exposure.
2. **Local forward pass :** The api-gateway sends a `vflTrainRequest` request to each client in parallel based on the provided indexes to calculate their local intermediate embeddings.
3. **Loss function derivatives :** The api-gateway collects the received embeddings into a unified strictly ordered list based on a predetermined client list order and transmits the data via a `vflAggregateRequest` request to the server to compute the global loss. 
4. **Local gradient descent :** The api-gateway sends a `vflGradientDescentRequest` request containing the respective gradients to each client. Each client performs Q local backward propagation based on the `staled` gradients.
5. **Local update :** Finally, the api-gateway, in parallel with clients, dispatches a `vflLocalUpdateRequest` to the server to perform Q-1 forward passes based on the intermediate `staled` client embeddings.

### Training Services Modifications
In Scattered Directive, VFL training process is abstracted into two distinct training services: `vfl-train` and `vfl-train-model`.

In `vfl-train`, a dedicated function was implemented to extract the data based on the batch sample indexes. Additionally, local gradient descent is executed Q consecutive times within an iteration loop. 

In `vfl-train-model`, two additional functions were implemented. The first is responsible for the generation of the mini-batch. Furthermore, a local update routine was implemented to simply perform loss function over Q-1 times.

### Gateway and sidecar timing metrics

The training status JSON now includes `metadata.timing` for the whole run and
`results[].timing` for each FedBCD synchronization round. No monitoring stack
is required. Rebuild and publish the two changed Go services:

```bash
make -C go api-gateway sidecar
```

Deploy the updated gateway and sidecar images before measuring. Local charts
use `IfNotPresent`, so ensure the cluster uses the rebuilt `fed-bcd` images
rather than a cached older image with the same tag. The new job sidecars must
also use that image; rebuilding Python services is not required for these metrics.

Each round records these non-overlapping wall-clock phases:

- `server_sample`: obtain mini-batch indexes.
- `client_forward`: collect client embeddings in parallel.
- `server_aggregate`: compute gradients.
- `local_update`: client gradient descent and server local updates, concurrently.
  They share one phase/barrier so overlapping work is not counted twice.

Measurements include phase/round durations, payload bytes, per-participant
active/idle durations and summed early-finisher barrier wait. Sidecars measure
elapsed time between handing a request to the Python chain and its reply,
using local monotonic clocks and a per-call token. The gateway reports this as
`algorithm_ms`; `communication_ms` is the remaining measured HTTP round trip,
including transport, queueing and middleware, not pure network time.
Missing or invalid sidecar measurements are counted in `unmeasured_calls`
rather than reported as zero computation. Run-level byte and algorithm totals
cover training rounds; `warmup` and `shutdown` have separate timing summaries.

`coordination_overhead_ms` is a wall-clock residual, calculated as
`round_ms - policy_check_ms - sum(phase.duration_ms)`. It measures time outside
the instrumented phase boundaries, not all gateway work or gateway CPU time.
Request preparation, serialization, DNS resolution and dispatch performed
inside a phase are included in that phase's duration. Result publication and
other work after the round timer stops are not included in this residual.

`total_rounds` retains its existing meaning (requested local cycles).
`synchronization_rounds` records `cycles / communication_frequency`, truncated
as in the existing execution loop. `scheduled_local_steps` and
`completed_local_steps` distinguish scheduled work from completed rounds.
For example, 120 cycles at communication frequency 10 produce 12 result entries,
not 120. Each entry retains its final `accuracy` and also includes the local
update `accuracies` returned by the server.

This branch uses asynchronous policy updates, not baseline's per-round policy
rechecks. Accordingly `policy_checks` and `policy_check_ms` are zero; background
policy reasoning is not separately timed and is not included in gateway call
totals. Participant times are summed across parallel calls and can exceed
wall-clock time; payload bytes are not wire-level traffic.

The timing hooks keep the FedBCD parallel schedule. Request payloads and
gradient indexes are captured before goroutines start, preventing concurrent
mutation of the shared request map. Client order is deterministic within rounds.
HTTP/phase failures are logged and surfaced rather than attempting to read
accuracy from an empty result. Failed-round timing is saved in
`metadata.failed_round`. Gateway logs show compact summaries instead of full
result/embedding JSON; the complete metrics remain in the status response.

### Running an experiment and generating plots

After deploying the rebuilt gateway and sidecar, run from the repository root
in Bash/WSL:

```bash
python3 -m pip install -r plots/requirements.txt
python3 scripts/run_fed_bcd_experiment.py
```

Defaults are one request of 120 local cycles, communication frequency Q=10,
and batch size 64: 12 synchronization rounds. For another configuration:

```bash
python3 scripts/run_fed_bcd_experiment.py --cycles 120 \
  --communication-frequency 10 --sample-batch-size 64 --runs 1
```

The runner uses the existing agreement identity
`evangelos.pipilikas@student.uva.nl`, not the baseline branch's `requestor`.
No agreement or orchestrator changes are required. The current worker returns
Q-1 local-update accuracies, so Q must be at least 2. The runner requires cycles
to be a multiple of Q to avoid silently truncating requested work.

Each invocation creates a timestamped directory under `experiments/fed-bcd/`
(ignored by Git). Each `run-001/` contains:

- `result.json`: full enriched gateway status response.
- `request.log`: submission, polling and errors.
- `metrics/run.json`, `run.csv`, `rounds.csv`, `phases.csv`, `calls.csv`,
  `participants.csv`, and `validation.json` under `metrics/`.
- `plots/result-timing.png`: wall-clock training/policy/coordination time,
  summed measured algorithm/communication time, barrier wait and payload volume.

Plots use synchronization rounds, not individual local steps. The
`local_update` phase contains concurrent client and server work; it is not
counted as two sequential phases. The gateway's event-driven policy updates
are not baseline-style per-round rechecks. Zero policy-check time means no
synchronous recheck measurement, not that background policy processing is free.

#### Interpreting the timing plot

The first panel's "Training phases" category is elapsed time inside training
phase boundaries, not pure model computation. It includes computation,
transport, queueing, middleware and gateway work inside those boundaries.
"Gateway coordination" is the outside-phase residual described above. A very
small coordination bar therefore does not demonstrate negligible total gateway
overhead.

The "Policy recheck" category is zero by construction on this event-driven
branch: no synchronous per-round recheck is performed. It must not be interpreted
as a measured zero-cost policy evaluation. Initial approval is recorded separately
as `metadata.timing.approval_ms`.

The plot covers synchronization rounds only. It excludes approval, worker
warmup/startup and shutdown, which remain available in `metadata.timing`.
Use `metadata.timing.total_ms` for the overall approval/training lifecycle;
do not infer total experiment duration from the round bars. That lifecycle
timer also excludes runner-side polling and plot/export generation.
The second panel shows summed measured participant call time, not another
wall-clock breakdown of the first panel; parallel participant times can exceed
the round's elapsed duration.

The runner checks completed round counts, three participating clients,
measurement coverage, call failures and requested execution parameters before
plotting. It does not install/uninstall DYNAMOS or contact a monitoring stack.
A failed request or validation/plotting error returns nonzero and keeps
available diagnostics. A timeout does not cancel training in the cluster.
It auto-forwards the API gateway when needed and stops only its own forward.
Custom `API_BASE_URL`, `API_PORT`, `POLL_INTERVAL_SECONDS` and `MAX_POLLS` are
supported; the default limit is 2160 polls at five-second intervals.

To re-export metrics or regenerate plots without training:

```bash
RUN_DIR="experiments/fed-bcd/REPLACE_WITH_EXPERIMENT_FOLDER/run-001"
python3 scripts/run_fed_bcd_experiment.py --export-result "$RUN_DIR/result.json"
python3 plots/plot_vfl_timing.py "$RUN_DIR/result.json" \
  --labels FedBCD --output-dir "$RUN_DIR/plots"
```

Use the full `result.json`, not `metrics/run.json`, as plot input. Supply multiple
result paths and matching `--labels` to generate `comparison.png` as well.
Use `--skip-rounds 1` to omit the first synchronization round from plots.

## References

- **Y. Liu et al.**, "FedBCD: A Communication-Efficient Collaborative Learning Framework for Distributed Features," in IEEE Transactions on Signal Processing, vol. 70, pp. 4277-4290, 2022, doi: 10.1109/TSP.2022.3198176. keywords: {Stochastic processes;Data models;Collaborative work;Signal processing algorithms;Distributed databases;Security;Data privacy;Federated learning;data privacy;federated stochastic block coordinate descent;cross-silo federated learning;distributed features}
[![DOI:10.1109/TSP.2022.3198176](https://zenodo.org/badge/DOI/10.1007/978-3-319-76207-4_15.svg)](https://doi.org/10.1109/TSP.2022.3198176)
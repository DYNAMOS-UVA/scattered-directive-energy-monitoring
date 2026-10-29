<div align="right">
  <a href="https://github.com/Apipilikas/scattered-directive-energy-monitoring/tree/main">
    <strong>🔙 Return to <code>main</code> branch</strong>
  </a>
</div>

<div align="center">

# Scattered Directive Energy Monitoring
### Branch: `baseline`

*This branch contains the basic configuration to deploy the pipeline, execute unsupervised experiments, analyze data, and generate plots.*

</div>

---

## Deployment

Run these commands from the repository root in Bash (Linux, macOS, or WSL), with Docker, Helm,
kubectl, and access to the target cluster configured.

The repository root is detected automatically by [dynamos.conf](dynamos.conf).
An exported `DYNAMOS_ROOT` overrides detection. If installation resolves files
from an old checkout, run `unset DYNAMOS_ROOT` before retrying; the installer
checks that the selected root contains the required configuration files.
This branch uses Docker Hub account `dynamos1` and image tag `baseline` for
the gateway, orchestrator, legacy policy enforcer, agents, sidecars, and
dynamically created training jobs. No eFLINT components are installed.

Install with the energy-monitoring stack:

```bash
bash configuration/dynamos-configuration.sh <mode>
```

Or install without Prometheus, Grafana, Loki, Kepler, and cAdvisor:

```bash
bash configuration/dynamos-configuration-no-monitoring.sh <mode>
```

Here `<mode>` is `local` or `fabric`. The no-monitoring installer uses the
same installation steps with the monitoring-stack step disabled. It does
not uninstall an existing monitoring stack or disable gateway/sidecar
timing instrumentation; the existing core tracing/logging configuration
also remains enabled.

Local etcd configuration is copied by the PVC helper scripts, not by an
initialization Job. The local namespace chart no longer declares the obsolete
`init-etcd-pvc` Job, avoiding immutable Job-template errors on upgrades.
Helm removes the old release-managed Job during upgrade without deleting
the etcd PVC. The NGINX step also supports upgrading an existing release.

Build and publish the updated images before installing; an existing remote
`baseline` image will not contain these source changes:

```bash
make -C go all
make -C python vfl
# For regression-demo workers, also run:
make -C python vfl-demo
```

Python builds publish the current branch tag by default; `BUILD_BOTH=1`
also publishes `latest`, and `PUSH=0` builds without pushing. Deployment
uses `baseline` by default, not `latest`. An explicit
`DYNAMOS_IMAGE_TAG=<tag>` override applies to all DYNAMOS charts and spawned
jobs. Existing per-service `<SERVICE>_TAG` agent environment overrides
take precedence over the shared `MICROSERVICE_TAG`.

The Go Makefile also uses the current Git branch as the image tag and always
pushes both that tag and `latest`. Build from `baseline` to match this branch's
deployment defaults. The baseline request requires only `vfl-train` and
`vfl-train-model` from the Python services; demo and SQL workers are not needed.
After the client shutdown acknowledgment fix, rebuild `vfl-train` before the
next run so clients reply before stopping rather than leaving shutdown calls
to time out.

### Reusing changes on other VFL branches

Keep monitoring-only and deployment-only changes in separate focused commits,
without policy-engine changes. Apply them with `git cherry-pick -x <commit>`
on each target branch; the source commit reference helps track which branches
received the change. Use separate Git worktrees to test branches side by side
without repeatedly switching the working directory.

The deployment changes should transfer mostly unchanged (set each branch's
image-tag default). Timing hooks in the coordinator must be adapted and
tested against each method's schedule: asynchronous acknowledgement is not
proof that background local training has completed.

### Uninstalling

```bash
bash configuration/uninstall-dynamos.sh
```

This is a destructive reset of DYNAMOS in the current Kubernetes context:
it deletes agent Jobs and removes application releases before the namespace
release (which can delete namespaces and PVCs). Missing releases are skipped
using `helm list`, without requiring Helm's newer `--ignore-not-found` flag.
Other Helm/kubectl errors stop the script. Application releases are looked up
in `${HELM_NAMESPACE:-default}`; monitoring and NGINX use their installation
namespaces.
The script requires
[`configuration/etcd_launch_files/agreements.json`](configuration/etcd_launch_files/agreements.json)
to discover agent namespaces for Job deletion. It exits before uninstalling
if that file is missing; ensure `DYNAMOS_ROOT` points to the correct checkout.

## Experiments

### Quick baseline run

The orchestrator supports plain usernames such as `requestor` when generating
Job IDs, as well as email identities. Deploy the rebuilt orchestrator image
after updating this helper; older baseline images return an empty Job ID for
plain usernames.

After deployment and loading the `requestor` agreements, run:

```bash
CYCLES=3 bash scripts/single_request.sh
```

Based on the reference branch's trigger script, this starts a temporary
API-gateway port-forward if needed, submits training as `requestor`, polls
the status, and saves the terminal response to `baseline-smoke-result.json`.
It stops only its own port-forward on exit. A failed run, HTTP/JSON error,
or polling timeout returns a nonzero exit code. A timeout does not cancel
training in the cluster.

Defaults are 10 rounds, a 5-second poll interval, and 120 polls. Override
`CYCLES`, `API_PORT`, `API_BASE_URL`, `POLL_INTERVAL_SECONDS`, `MAX_POLLS`,
or `OUTPUT_FILE` as needed. A custom `API_BASE_URL` must already be reachable;
the script does not create a port-forward for custom URLs. Requires Bash,
curl, Python 3, and kubectl when starting a port-forward.

Check that the saved result has status `done`, the requested round count,
three clients per round, and timing data before starting longer experiments.
The API gateway logs compact run summaries rather than full request/result
JSON. Full timing results remain in the status response and saved result file.
Agent HTTP failures include the target endpoint and error; training Jobs are
created by agents when execution requests arrive, not by policy approval alone.

### Full baseline experiment (JSON timing only)

Use the existing installation without installing monitoring or resetting the
cluster. Install the plotting dependency in the same Python environment first:

```bash
python3 -m pip install -r plots/requirements.txt
python3 scripts/run_baseline_experiment.py
```

Each successful run automatically generates `run-001/plots/result-timing.png`
(with the corresponding run number for subsequent runs) using
the timing plotter ported from the reference branch. Plotting errors stop the
runner with a nonzero exit while preserving the results and CSV metrics.

Defaults are one run of 120 rounds as `requestor`. Override with `--cycles 120`,
`--runs 3`, or `--output-dir /path/to/results`. Bash, curl, Python 3 and kubectl
are required as for the quick run. Existing `API_BASE_URL`, `API_PORT`,
`POLL_INTERVAL_SECONDS`, and `MAX_POLLS` overrides are supported; this runner
defaults to 2160 polls (about three hours at the default interval).

Each invocation creates a unique timestamped folder under `experiments/baseline/`
(ignored by Git). It contains `experiment.json` and one `run-001/` folder per run:

- `result.json`: the complete enriched API gateway status response, retaining
  all gateway and sidecar-derived timing fields.
- `request.log`: request submission, polling progress and errors.
- `metrics/run.json` and `metrics/run.csv`: run-level timing.
- `metrics/rounds.csv`: accuracy, clients and gateway round timing.
- `metrics/phases.csv`, `metrics/calls.csv`, `metrics/participants.csv`:
  phase, per-target call and participant timing, including sidecar-derived
  algorithm time and gateway-derived communication estimates where available.
- `metrics/validation.json`: checks for completion, round/client counts,
  sidecar measurement coverage and recorded call failures.
- `plots/result-timing.png`: four panels for round wall-clock time, measured
  algorithm/communication time, synchronization wait and gateway payload volume.

Metrics are exported automatically after each run. To regenerate them from an
existing full result without submitting training:

```bash
RUN_DIR="experiments/baseline/REPLACE_WITH_EXPERIMENT_FOLDER/run-001"
python3 scripts/run_baseline_experiment.py --export-result "$RUN_DIR/result.json"
```

This refreshes the sibling `metrics/` files, leaving `result.json` unchanged.
The expected round count comes from the saved result's `metadata.total_rounds`.
Set `RUN_DIR` to the actual run directory printed by the experiment runner.
Export-only mode does not generate plots.

To generate plots alone from an existing result:

```bash
python3 plots/plot_vfl_timing.py "$RUN_DIR/result.json" \
  --labels baseline --output-dir "$RUN_DIR/plots"
```

Use the full `result.json` as plot input, not `metrics/run.json`: the latter
contains only run-level timing and does not include the per-round results.

The runner stops with a nonzero exit on a failed or incomplete run, retaining
available results and diagnostics. Missing timing is not replaced with zero.
No Prometheus, Kepler, energy or carbon measurements are collected. Call-time
sums can exceed wall-clock duration because clients execute concurrently.
Training timeout does not cancel the cluster run; inspect it before retrying.
The older `run_experiment.sh`/`execute_experiments.sh` pipeline performs
deployment resets and monitoring collection; it is not used by this runner.

New JSON-timing experiments are saved under `experiments/baseline/`, as described
above. Historical monitoring-pipeline results are stored separately under
[`energy-monitoring-pipeline/experiments`](energy-monitoring-pipeline/experiments),
with two environment folders:
* [`local`](https://github.com/Apipilikas/scattered-directive-energy-monitoring/tree/baseline/energy-monitoring-pipeline/experiments/local) : contains results from local environment
* [`fabric`](https://github.com/Apipilikas/scattered-directive-energy-monitoring/tree/baseline/energy-monitoring-pipeline/experiments/fabric) : contains results from FABRIC environment

## Legacy Monitoring Data Analysis

This section uses Prometheus/Kepler metrics and requires a monitoring deployment.
It is separate from the JSON-only baseline runner and does not analyze its
gateway/sidecar timing exports.

To support Data Analysis, a pipeline has been implemented that integrates both Anomaly Detection (AD) algorithm and Root Cause Analysis (RCA). You can run these steps manually or use the condensed pipeline script:

### Option A : Manual execution

* **Retrieve data** : Run the `collect_metrics.py` script to collect the metrics. Data are stored in a csv in `energy-monitoring-pipeline/output` directory.
* **Analyze data** : Run the `analyze_metrics.py -ad -rca` script to detect the anomalies and the underlying root causes. Relevant flag parameters `-ad` and `-rca` can be passed to perform anomaly detection and root cause analysis respectively. For the latter, a proper baseline dataset should be provided under the `baseline_data_metrics.csv` name.

### Option B : Automated pipeline execution

The entire metrics collection and data analysis can be run by using the following command:

```
execute_pipeline.py -cm -ad- rca
```

Relevant flag parameters `-cm`, `-ad` and `-rca` indicate collect metrics, anomaly detection and root cause analysis routine.

## Retrieve Logs

Logs can be retrieved by running the following command:

```
./scripts/retrieve_logs.sh <namespace> <pod-search> <container>
```

The parameters that should be provided are the following:
*   `namespace` : The Kubernetes namespace where the pod is located.
*   `pod-search` : The starting string/prefix of the pod's name.
*   `container` : The specific container name within the pod.

## Legacy Monitoring Experiment Pipeline

This is the older monitoring workflow, not the recommended JSON-only baseline
runner above. It installs/uninstalls DYNAMOS between repetitions, depends on the
monitoring stack, and retains an older request identity. It needs adaptation
before use with the current `requestor` agreements. Do not use it to run the
no-monitoring experiment described above.

To execute experiments unsupervised, run the following command:

```
./scripts/execute_experiments.sh <mode>
```

where `<mode>` can be either local to deploy Scattered Directive locally or fabric for the FABRIC configuration. 

To configure the experiments, modify the `experiments_configuration.json` file. You can configure the experiment name, number of iterations, repetitions, and whether policy-aware changes are applied.

> [!WARNING] 
> In this legacy configuration, `iterations` counts complete training requests,
> not training rounds. The JSON-only runner instead uses `--runs` for requests
> and `--cycles` for rounds per request; its defaults are one request of 120 rounds.

An example of the file is the following:

```json
[
    {
        "name": "baseline",
        "iterations": 10,
        "repetitions": 2,
        "policy_aware": false
    },
    {
        "name": "pa_baseline",
        "iterations": 10,
        "repetitions": 2,
        "policy_aware": true
    }
]
```

## VFL Timing Plots

The gateway adds per-round timing data to VFL result exports. Install the plot
dependency from the repository root, then plot one run or compare multiple
exports:

```bash
python -m pip install -r plots/requirements.txt
python plots/plot_vfl_timing.py results/vfl-scenarios/local.json \
  --labels Local --output-dir results/vfl-scenarios/plots
python plots/plot_vfl_timing.py results/vfl-scenarios/local.json \
  results/vfl-scenarios/fabric.json --labels Local Fabric \
  --skip-rounds 1 --output-dir results/vfl-scenarios/plots
```

The positional arguments are scenario JSON files; `--labels` takes one label
per file, `--skip-rounds` omits the first N recorded rounds, and `--output-dir`
selects the destination. A single input produces a `<file>-timing.png`; multiple
inputs also produce `comparison.png`.

Each export must contain a `results` list with `train_round` and `timing` in
each plotted round. Required timing values are `round_ms`, `policy_check_ms`,
`coordination_overhead_ms`, `barrier_wait_ms`, `bytes_sent`, and
`bytes_received`. Sidecar breakdowns, when available, include `measured_calls`,
`unmeasured_calls`, `algorithm_ms`, and `communication_ms`. Older exports
without gateway timing cannot be plotted; missing sidecar measurements are
shown as unavailable, not zero.

Run the plot tests from `plots/` with:

```bash
python -m unittest test_plot_vfl_timing.py
```

Timing is monitoring data, not a policy or training behavior change. Gateway
round timings include the policy check and coordination; the plots cover
training rounds only, excluding approval, warm-up, and shutdown. Participant
durations are summed participant-time, not wall-clock time; the reported
communication portion includes transport, queueing, and middleware, and
gateway payload sizes are not wire-level traffic. Sidecar algorithm time is
available only for calls with valid measurements, so check the reported
measurement coverage before comparing runs.
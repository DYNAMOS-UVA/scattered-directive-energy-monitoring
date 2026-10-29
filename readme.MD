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

### Line endings on Windows / WSL

Bash scripts must use **LF**, not Windows **CRLF** line endings. CRLF can cause
`$'\r': command not found` and misleading `No such file or directory` errors.
The repository's `.gitattributes` enforces LF for shell scripts, configuration,
and Linux build inputs, including when Git uses `core.autocrlf=true`.

The VS Code workspace setting `files.eol` is already set to `\n` (LF) for new
files. For an existing CRLF file, click **CRLF** in the bottom-right status bar,
select **LF**, and save. `.editorconfig` supplies the same default to compatible
editors. No global VS Code or Git setting change is required.

The scenario scripts are in the repository's `scripts/` directory. The policy models are stored and updated through the policy-enforcer/orchestrator path; the repository copies of `.eflint` files are not modified by a normal scenario run.

### Install DYNAMOS

For a lightweight local demonstration without monitoring:

```bash
bash ./configuration/dynamos-configuration-no-monitoring.sh local
```

Both installation scripts use `ORCHESTRATOR_IMAGE_TAG` from `dynamos.conf`,
defaulting to `latest`, for the orchestrator chart's `orchestrator`,
`policy-enforcer`, and `sidecar` images. This keeps the orchestrator sidecar on
the same version as every other component. The tag can be overridden through
the environment before running an installer (for example
`ORCHESTRATOR_IMAGE_TAG=baseline-eflint-2`), but then the orchestrator runs a
different sidecar version from the rest of DYNAMOS.

To update only the orchestrator release in an existing local installation,
run these commands from the repository root (the release is in `default`,
while its workloads are in `orchestrator`):

```bash
source ./dynamos.conf
helm upgrade orchestrator ./charts/orchestrator --namespace default --reuse-values \
  --set-string "dockerArtifactAccount=${DOCKERHUB_ACCOUNT}" \
  --set-string "branchNameTag=${ORCHESTRATOR_IMAGE_TAG}"
kubectl -n orchestrator rollout status deployment/policy-enforcer --timeout=300s
kubectl -n orchestrator rollout status deployment/orchestrator --timeout=300s
kubectl -n orchestrator get pods
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

Scenario status parsing reads JSON through standard input, so large responses
from longer runs do not hit the operating system's command-line argument limit.
Run the parsing regression test with
`bash scripts/tests/vfl-scenario-json-field-test.sh`.

### Timing and communication metrics

The API gateway drives every phase of a round, so it records timings from its own
(single, monotonic) clock. Each `results[i].timing` contains:

| Field | Meaning |
|---|---|
| `round_ms` | Wall time of the round, including its policy check. |
| `policy_check_ms` | Policy re-verification (reasoning) before the round; 0 when no check ran. |
| `phases.<name>` | `client_forward`, `server_aggregate`, `client_update`: wall time, `barrier_wait_ms`, `spread_ms`, bytes, and per-participant round trips (`calls`). |
| `barrier_wait_ms` | Synchronisation cost: summed time participants that finished a parallel phase early waited for the slowest one. |
| `coordination_overhead_ms` | Gateway work between phases (round − policy check − phases). |
| `participants.<name>` | `active_ms` (own round trips) and `idle_ms` (rest of the round). |

`metadata.timing` aggregates the run: `approval_ms` (initial approval and job
composition), `warmup` (ping phase), summed round/policy/barrier/overhead times,
total bytes, `shutdown`, and `total_ms`. A round trip includes transport, queueing
and compute. Updated job sidecars provide `algorithm_ms`, allowing the gateway
to report the remaining `communication_ms` (communication and middleware).
`measured_calls` and `unmeasured_calls` indicate coverage; absent measurements
are not treated as zero. See [the metric definitions and deployment notes](results/info/timing-metrics.md)
for the exact boundaries, aggregation rules, and deployment notes. All
components must run the same sidecar image.

### Visualizing scenario results

Install the plotting dependency once, then generate a timing dashboard:

```bash
python3 -m pip install -r plots/requirements.txt
python3 plots/plot_vfl_timing.py results/vfl-scenarios/S1-20261021-162842.json
```

The PNG is saved under `results/vfl-scenarios/plots/` (gitignored). The four
panels show round wall time split into training/policy/coordination,
algorithm versus communication-and-middleware time, synchronization wait,
and gateway payload volume. Only training rounds are included: approval,
warm-up and shutdown are excluded. Policy time includes messaging and
reasoning, not just the reasoner.

For deployment comparisons, supply multiple JSON files and optional labels:

```bash
python3 plots/plot_vfl_timing.py local.json distributed.json \
  --labels Local Distributed
```

This produces a dashboard per file and `comparison.png`. Comparisons use
means per round, except algorithm/communication time, which uses means per
measured call. Use matching algorithm, participant count, batch size, round
count and policy-check interval for meaningful comparisons. The first round
is included and can contain algorithm initialization.

For steady-state plots without the first round's initialization spike, add
`--skip-rounds 1`. This applies to every input and recomputes all summaries
from the remaining rounds, preserving the exported round indices:

```bash
python3 plots/plot_vfl_timing.py results/vfl-scenarios/S1-20261021-162842.json \
  --skip-rounds 1 --output-dir results/vfl-scenarios/plots/steady
```

Communication and synchronization are summed **participant times**, not
additive parts of wall time. Communication includes middleware and queueing;
payload bytes are gateway request/response bodies, not total network traffic.
Missing sidecar measurements are shown as unavailable, not zero; partial
coverage is labelled and emits a warning. Input files without gateway timing
are rejected. Failed or partial runs show their recorded rounds and status.

Use `--output-dir <directory>` to change the destination. The script runs
headlessly, so it also works in WSL and on remote machines. Run its tests with:

```bash
python3 -m unittest discover -s plots -p test_plot_vfl_timing.py
```

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


-----------------------

# Scattered Directive Energy Monitoring
### Branch: `baseline`

*This branch contains the basic configuration to deploy the pipeline, execute unsupervised experiments, analyze data, and generate plots.*

</div>

---

## Deployment 
To deploy Scattered Directive, run the following command:
```
./configuration/configure-dynamos.sh <mode>
```

where `<mode>` can be either local to deploy Scattered Directive locally or fabric for the FABRIC configuration.

## Experiments

All the executed experiments can be found in [`energy-monitoring-pipeline/experiments`](https://github.com/Apipilikas/scattered-directive-energy-monitoring/tree/baseline/energy-monitoring-pipeline/experiments) directory. To keep workspace organized, results are seperated into two distinct folders:
* [`local`](https://github.com/Apipilikas/scattered-directive-energy-monitoring/tree/baseline/energy-monitoring-pipeline/experiments/local) : contains results from local environment
* [`fabric`](https://github.com/Apipilikas/scattered-directive-energy-monitoring/tree/baseline/energy-monitoring-pipeline/experiments/fabric) : contains results from FABRIC environment

## Data Analysis

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

## Execute Experiments Pipeline

To execute experiments unsupervised, run the following command:

```
./scripts/execute_experiments.sh <mode>
```

where `<mode>` can be either local to deploy Scattered Directive locally or fabric for the FABRIC configuration. 

To configure the experiments, modify the `experiments_configuration.json` file. You can configure the experiment name, number of iterations, repetitions, and whether policy-aware changes are applied.

> [!WARNING] 
> We strongly recommend keeping the iterations count to 10 or fewer when running in the local environment, as the Scattered Directive can become highly unstable locally.

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
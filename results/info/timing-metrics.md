# Timing and communication metrics

This document describes the timing metrics exported by the API gateway in VFL
scenario results such as `results/vfl-scenarios/S1-*.json`. It also records the
sidecar measurement of algorithm-chain elapsed time and the gateway's estimate
of communication and middleware delay. New measurements require updated job
sidecars and an updated API gateway; older result files are unchanged.

## Measurement model

The API gateway coordinates every VFL phase, so it measures:

- elapsed time for each phase;
- round trips to each participant (server or client);
- request and response payload sizes;
- policy-check (reasoning) time;
- idle time and synchronisation (barrier) waiting.

Gateway durations use the **API gateway clock**, indicated by
`"clock": "api-gateway"`. Algorithm durations use the job sidecar's own monotonic
clock. Only elapsed durations are exchanged, never cross-node timestamps.
Values exported by the gateway are in milliseconds with microsecond precision.
Cross-node clock offsets therefore do not affect these measurements.

A gateway round trip includes:

- HTTP transfer between gateway and agent;
- agent and sidecar forwarding;
- RabbitMQ publishing, queueing, and consumption;
- network transport between nodes;
- algorithm computation;
- the return path.

The gateway alone **cannot distinguish computation from communication**. The
sidecar extension below addresses this.

## Result structure

```text
metadata
├── policy_* / total_rounds / training_backtrack   (existing scenario configuration)
├── stop_reason / stopped_before_round              (only when policy stops a run)
└── timing                                          (run-level timing)
    ├── warmup                                      (phase summary)
    └── shutdown                                    (phase summary)
results[]                                           (one entry per executed round)
├── train_round / accuracy / clients / timestamp    (existing fields)
└── timing                                          (round-level timing)
    ├── participants.<name>
    └── phases.<phase>                              (phase summary)
        └── calls.<participant>
```

The timing fields are additive. Existing consumers that read `train_round`,
`accuracy`, `clients`, or the existing metadata fields remain compatible.

## Run-level metrics: `metadata.timing`

| Field | Unit | Meaning |
|---|---:|---|
| `clock` | — | Clock used for measurements. Currently always `api-gateway`. |
| `approval_ms` | ms | Initial request approval, from sending the approval request until the gateway receives the response. Includes orchestrator processing, policy reasoning, and related messaging. |
| `warmup` | object | Phase summary for `vflPingRequest`, sent to all participants before training. Includes job-pod startup, so it represents deployment readiness rather than steady-state communication. |
| `rounds_ms` | ms | Sum of all executed `round_ms` values. Use this as the main training duration when comparing runs. |
| `policy_check_ms` | ms | Total time spent on in-run policy rechecks. Includes request/response messaging and eFLINT reasoning. |
| `policy_checks` | count | Number of in-run policy rechecks performed. Initial approval is reported separately in `approval_ms`. |
| `barrier_wait_ms` | ms | Total of all round-level `barrier_wait_ms` values. Warm-up and shutdown are excluded. |
| `coordination_overhead_ms` | ms | Total of all round-level `coordination_overhead_ms` values. |
| `bytes_sent` | bytes | Total request-body bytes sent by the gateway during training rounds. Warm-up and shutdown are excluded. |
| `bytes_received` | bytes | Total response-body bytes received by the gateway during training rounds. Warm-up and shutdown are excluded. |
| `shutdown` | object | Phase summary for `vflShutdownRequest`. |
| `total_ms` | ms | Initial approval plus all work from training start until shutdown completes, including warm-up and shutdown. |

## Round-level metrics: `results[].timing`

| Field | Unit | Meaning |
|---|---:|---|
| `round_ms` | ms | Total elapsed time for the round, including any policy check before training. |
| `policy_check_ms` | ms | Policy-recheck time before this round. It is `0` when no check ran, such as round 0. |
| `phase_order` | list | Phases in execution order. For VFL: `client_forward`, `server_aggregate`, and `client_update`. |
| `phases` | object | Summary for each phase in `phase_order`. |
| `barrier_wait_ms` | ms | Total of `barrier_wait_ms` across this round's phases. This is the primary **synchronisation cost** metric. |
| `coordination_overhead_ms` | ms | `round_ms - policy_check_ms - sum(phase duration_ms)`. Gateway work between phases, such as request preparation, policy-change submission, and bookkeeping. |
| `bytes_sent` | bytes | Total request-body bytes sent by the gateway during this round. |
| `bytes_received` | bytes | Total response-body bytes received by the gateway during this round. |
| `participants.<name>.active_ms` | ms | Total gateway-observed round-trip time for this participant during the round. It includes communication and computation, not computation alone. |
| `participants.<name>.idle_ms` | ms | `round_ms - active_ms`. Time with no outstanding gateway request for that participant, including policy checks and waiting for other participants or phases. |

## Phase summaries

Phase summaries appear in `results[].timing.phases.<phase>`,
`metadata.timing.warmup`, and `metadata.timing.shutdown`.

| Field | Unit | Meaning |
|---|---:|---|
| `duration_ms` | ms | Elapsed phase time, from phase start until the gateway records completion. Includes all parallel calls and gateway work inside the phase, such as response parsing. |
| `barrier_wait_ms` | ms | For each participant, the time from its last call completing until the phase's slowest participant completes, summed across participants. A single-participant phase reports `0`. |
| `spread_ms` | ms | Difference between the slowest and fastest participants' total call duration in the phase. Indicates imbalance. |
| `bytes_sent` | bytes | Total request-body bytes sent in this phase. |
| `bytes_received` | bytes | Total response-body bytes received in this phase. |
| `calls.<participant>` | object | Gateway calls to that participant during the phase. |

### Calls: `calls.<participant>`

| Field | Unit | Meaning |
|---|---:|---|
| `duration_ms` | ms | Total round-trip time for all attempts to this participant in the phase. |
| `bytes_sent` | bytes | Total request-body bytes sent to this participant. |
| `bytes_received` | bytes | Total response-body bytes received from this participant. |
| `attempts` | count | Present only when the gateway made more than one attempt. |
| `failures` | count | Present only when one or more attempts failed. |

## VFL phases

| Phase | Participants | Purpose |
|---|---|---|
| `client_forward` | Active clients | Clients compute and return embeddings. |
| `server_aggregate` | Server | Server aggregates embeddings, computes loss and accuracy, and returns gradients. |
| `client_update` | Active clients | Clients apply gradients. |
| `warmup` | All authorised participants | Pings participants and starts or wakes job pods. |
| `shutdown` | All authorised participants | Requests job shutdown. |

Other algorithms should use descriptive phase names while keeping the same
summary fields. That allows comparison across algorithms without fixing a
VFL-specific phase structure.

## Interpretation notes

1. **Round trips still include both processing and communication.** Each
   `duration_ms` and `active_ms` value includes both. Optional sidecar metrics
   below expose a split for calls with valid measurements.
2. **Byte counts are application payload sizes.** They are HTTP JSON body sizes
   seen by the gateway, not network bytes. They exclude HTTP headers and
   RabbitMQ framing. They also do not measure internal hops, such as agent to
   job pod.
3. **Response sizes include message envelopes.** Agents return the full
   `MicroserviceCommunication` JSON, including request metadata and the original
   request. Use byte counts to compare algorithms and deployments, not as exact
   transmitted model-data sizes.
4. **Round 0 includes first-use effects.** Initialisation, caches, and runtime
   warm-up can make round 0 slower. Consider reporting it separately or
   excluding it from steady-state averages.
5. **Policy time includes reasoning and messaging.** `policy_check_ms` and
   `approval_ms` currently cannot separate eFLINT reasoning from DYNAMOS
   transport.
6. **Shutdown can dominate `total_ms`.** In
   `S1-20261021-150459.json`, client shutdown calls waited about 60 seconds each
   until timeout. VFL clients stop without replying, while the server replies.
   Prefer `rounds_ms` until shutdown is acknowledged.
7. **Barrier wait measures synchronisation, not total idle time.**
   `barrier_wait_ms` captures waiting for the slowest participant in a parallel
   phase. `idle_ms` also includes sequential phases, such as clients waiting
   while the server aggregates. These are gateway-observed scheduling proxies,
   not measurements of CPU inactivity at the participants.

## Sidecar communication metrics

Implemented in the sidecar and gateway source. Deploy both updated images to
collect the new measurements.

### Goal

Split each successfully measured gateway call into:

```text
round_trip_ms = algorithm_ms + communication_ms
```

where:

- `algorithm_ms` is time between the job sidecar handing a message to the
  algorithm chain and receiving the chain's reply;
- `communication_ms` is the rest of the gateway round trip: gateway–agent HTTP,
  agent/sidecar forwarding, RabbitMQ, network transport, and the return path.

This enables comparing communication and middleware delay across algorithms and
deployment locations. `algorithm_ms` is elapsed request handling, not CPU time:
it includes local gRPC, serialization, model loading, sleeps, and any I/O or
waiting performed inside the algorithm chain. It does not prove pure compute
time or pure network latency.

### Measurement points

Use timestamps from the same job-pod sidecar only:

1. **Handoff:** in
   [`ChainConsume`](../../go/cmd/sidecar/rabbit_ms_chain.go), immediately
   before calling `msClient.SendData`, store `time.Now()` in a sidecar-local map.
   Pass a unique `dynamos_algorithm_timing_token` through message metadata.
   The stored Go time retains its monotonic component.
2. **Reply received:** at the start of the sidecar
   [`SendData`](../../go/cmd/sidecar/grpc_server.go) handler, **before**
   publishing to RabbitMQ, calculate the elapsed time and write it to
   `metadata["dynamos_algorithm_ms"]`.
3. **Cleanup:** delete the token and local timer on completion. Also discard
   timers when the synchronous chain RPC returns without a reply or with an
   error. No unbounded history of completed requests is retained.

The second stamp must be taken before publishing because
`msClient.SendData` blocks while the algorithm runs and publishes its reply.
Timing around that outer call would include RabbitMQ publishing.

Both measurements are in the same sidecar process. The interval ends before
RabbitMQ publishing, so publish and subsequent transport remain in the
communication remainder. Any communication initiated by the algorithm itself
inside the measured interval remains part of `algorithm_ms`.

### Gateway reporting

The agent already returns the complete `MicroserviceCommunication` JSON,
including `metadata`, to the gateway. The gateway can read
`dynamos_algorithm_ms` and add these fields:

| Location | Field | Meaning |
|---|---|---|
| `calls.<participant>` | `algorithm_ms` | Sum of valid sidecar durations for measured attempts to this participant. |
| `calls.<participant>` | `communication_ms` | Measured round trips minus their algorithm durations; communication and middleware time. |
| Phase summary | `algorithm_ms`, `communication_ms` | Totals across participants in the phase. |
| `participants.<name>` | `algorithm_ms`, `communication_ms` | Round totals for each participant. |
| Round and run timing | `algorithm_ms`, `communication_ms` | Totals for comparing algorithms and deployments. |

Each call, phase, participant, round, and run summary also exports:

| Field | Meaning |
|---|---|
| `measured_calls` | Number of successful call attempts with valid sidecar timing. |
| `unmeasured_calls` | Number of attempts with missing or invalid timing, or an HTTP/transport failure. Retries count as separate attempts. |
| `measured_round_trip_ms` | Sum of gateway round trips for measured attempts only. Present when `measured_calls > 0`. |

`algorithm_ms` and `communication_ms` are omitted when no attempts were
measured. Missing timing is never inferred as zero. Negative, non-finite,
malformed, or longer-than-round-trip algorithm durations are rejected with a
gateway warning; response forwarding and existing metrics still work.

For partial coverage, these totals cover **only measured attempts**. In
particular, do not subtract `algorithm_ms` from a call's full `duration_ms` when
some retry attempts were unmeasured. Instead:

```text
algorithm_ms + communication_ms = measured_round_trip_ms
```

Allow up to 0.002 ms difference from exported rounding. Phase, round, and run
totals sum call durations across participants; parallel calls overlap, so these
are participant-milliseconds, not wall-clock phase or training time. Do not add
these totals to barrier wait or policy time to reconstruct `rounds_ms`.
Warm-up and shutdown summaries have their own metrics, excluded from run
training totals, just like the existing byte counters.

The name is `communication_ms`, but it means communication **and
middleware** time, not pure network latency.

### Compatibility and impact

- **Long-running sidecars:** agents, orchestrator, policy enforcer, and API
  gateway do not start timers through the job-chain `ChainConsume` path.
  Messages without a local timing token are forwarded unchanged by `SendData`.
- **Job pods:** all job-pod sidecars use the changed code. Adding keys to
  `MicroserviceCommunication.metadata` (`map<string, string>`) is
  backward-compatible and requires no protobuf changes.
- **Algorithms:** no changes are required when an algorithm returns the
  received message through the DYNAMOS Python library
  `send_data(msComm, data, metadata)`; it preserves existing metadata. A
  service that discards the token will produce no measurement, without
  failing. No algorithm or agent image rebuild is required.
- **Multi-service chains:** the first service receives from the sidecar,
  and the last service returns through the sidecar. The value covers the
  whole synchronous, in-pod chain, not one service. Legacy streaming consumers
  are not measured. Asynchronous replies after the handoff RPC has returned
  are not measured, and unmatched tokens are removed with a warning.
- **Rollout:** there is one sidecar image for every component. The timing code
  is inert outside the job-chain path, so the same new image can run next to
  the agents, orchestrator, policy enforcer, API gateway, and job pods. Do not
  deploy different sidecar versions per component.

### Deploying

Every sidecar, including those of the agents, orchestrator, policy enforcer,
API gateway, and job pods, must run the same image. No chart or agent needs
a sidecar-specific setting. Chart-managed sidecars use
`sidecar:<branchNameTag>` of their chart, and job-pod sidecars use
`sidecar:latest` (the agent default when `SIDECAR_TAG` is unset). Therefore,
all charts must resolve to the same sidecar tag, which is `latest`:

1. Build and push from this branch (the Makefile tags each image with both the
   branch name and `latest`):

   ```bash
   cd go
   make sidecar api-gateway orchestrator policy-enforcer
   ```

   The agent image does not change, so `agent:latest` is reused. The
   policy enforcer built from this branch serves `/api/v1/readyz`.
2. Deploy the orchestrator chart with `latest` instead of `baseline-eflint-2`
   (`ORCHESTRATOR_IMAGE_TAG=latest`). Otherwise, the orchestrator and policy
   enforcer keep the old `sidecar:baseline-eflint-2`, which is a different
   sidecar version.
3. Restart the long-running pods. The API gateway and orchestrator charts use
   `imagePullPolicy: Always`. The agent charts use `IfNotPresent`, so a node
   that has an old `sidecar:latest` cached keeps it after a restart. Pull
   the new image on every node first (on Docker Desktop, the local `make` build
   already updates it). Job pods use the Kubernetes default for `:latest`,
   which is `Always`. New job pods get the new sidecar, while running jobs
   keep their old image.
4. Verify that all sidecar containers run the same image digest, then run a
   scenario and check `measured_calls` coverage.

Pushing `latest` overwrites the shared `dynamos1/*:latest` tags. To avoid this,
build **all** images, including the agent, under one dedicated tag and
install every chart with that `branchNameTag`. Because the agent sets no
`SIDECAR_TAG`, job pods would still use `sidecar:latest`. Therefore, push the
sidecar under that tag and also as `latest`, or set `SIDECAR_TAG` to the same
tag for every agent.
### Validation plan

1. Unit tests cover protobuf metadata preservation, concurrent requests,
   per-call cleanup, missing/unknown tokens, and forwarding errors.
2. Gateway tests cover validation, missing data, zero measurements, retries,
   coverage counts, aggregation, and HTTP response integration.
3. After deploying, run S1 and check measurement coverage and
   `algorithm_ms + communication_ms = measured_round_trip_ms`.
4. Repeat S1 across deployment configurations. Compare compute resources too:
   algorithm elapsed time can change with CPU contention or different hardware.
   End-to-end cluster validation is separate from the local tests.

### Remaining limitations

- `communication_ms` cannot separate RabbitMQ queueing from network
  transport. RabbitMQ metrics or Jaeger traces can support diagnosis.
- Policy reasoning remains combined with policy messaging.
- The VFL client shutdown timeout must be fixed before `total_ms` is a
  reliable deployment comparison metric.

## Plotting exported results

The helper [`plot_vfl_timing.py`](../../plots/plot_vfl_timing.py) creates a
four-panel PNG focused on the main performance metrics: round wall time split
into training, policy checks, and gateway coordination; measured algorithm
versus communication-and-middleware time; synchronization wait; and gateway
payload bytes. It excludes approval, warm-up, and shutdown.

Install Matplotlib once and plot a result from the repository root:

```bash
python3 -m pip install -r plots/requirements.txt
python3 plots/plot_vfl_timing.py \
  results/vfl-scenarios/S1-20261021-162842.json
```

The PNG is written to `results/vfl-scenarios/plots/`, which is gitignored.
For a steady-state view that omits the first round (which may include
initialization), add `--skip-rounds 1`. To compare runs, provide multiple JSON
files and an optional label for each:

```bash
python3 plots/plot_vfl_timing.py local.json distributed.json \
  --labels Local Distributed --skip-rounds 1
```

This writes one dashboard per run and a `comparison.png`. Comparison charts
show average round metrics and average measured-call times; use runs with
matching algorithms, participant counts, batch sizes, and policy-check
intervals. The communication and synchronization metrics sum participant
times and are **not** additive components of wall time. Communication includes
middleware and queueing, while payload sizes count gateway request/response
bodies rather than all network traffic. Missing sidecar measurements are
labelled unavailable; partial coverage is shown and reported as a warning.
The script and its tests are in `plots/`; run them with:

```bash
python3 -m unittest discover -s plots -p test_plot_vfl_timing.py
```

## Source references

- Gateway timing implementation: [`timing.go`](../../go/cmd/api-gateway/timing.go)
- VFL timing integration: [`requests.go`](../../go/cmd/api-gateway/requests.go)
- Sidecar chain consumer: [`rabbit_ms_chain.go`](../../go/cmd/sidecar/rabbit_ms_chain.go)
- Sidecar reply handler: [`grpc_server.go`](../../go/cmd/sidecar/grpc_server.go)
- Example result: [`S1-20261021-150459.json`](../vfl-scenarios/S1-20261021-150459.json)

# Timing and communication metrics

This document describes the timing metrics exported by the API gateway in VFL
scenario results such as `results/vfl-scenarios/S1-*.json`. It also records the
planned sidecar extension for separating algorithm computation from
communication. The sidecar extension is **planned, not implemented**.

## Measurement model

The API gateway coordinates every VFL phase, so it measures:

- elapsed time for each phase;
- round trips to each participant (server or client);
- request and response payload sizes;
- policy-check (reasoning) time;
- idle time and synchronisation (barrier) waiting.

All current values use the **API gateway clock**, indicated by
`"clock": "api-gateway"`. Durations are calculated from Go's monotonic clock and
reported in milliseconds with microsecond precision. Clock skew between nodes
therefore does not affect current metrics.

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

1. **Communication cost is not yet separated from computation.** Each
   `duration_ms` and `active_ms` value includes both. Planned sidecar metrics
   will expose that split.
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
   while the server aggregates.

## Planned sidecar metrics

> **Status: planned, not implemented.** No sidecar code has changed yet.

### Goal

Split each gateway call into:

```text
round_trip_ms = algorithm_ms + communication_ms
```

where:

- `algorithm_ms` is time between the job sidecar handing a message to the
  algorithm chain and receiving the chain's reply;
- `communication_ms` is the rest of the gateway round trip: gateway–agent HTTP,
  agent/sidecar forwarding, RabbitMQ, network transport, and the return path.

This enables comparing communication delay across algorithms and deployment
locations while keeping algorithm computation separate.

### Measurement points

Use timestamps from the same job-pod sidecar only:

1. **Handoff:** in
   [`ChainConsume`](../../go/cmd/sidecar/rabbit_ms_chain.go), immediately
   before calling `msClient.SendData`, write the sidecar receipt timestamp to
   message metadata.
2. **Reply received:** at the start of the sidecar
   [`SendData`](../../go/cmd/sidecar/grpc_server.go) handler, **before**
   publishing to RabbitMQ, calculate the elapsed time and write it to
   `metadata["dynamos_algorithm_ms"]`.
3. **Cleanup:** remove the handoff timestamp before publishing, so only the
   derived duration leaves the pod.

The second stamp must be taken before publishing because
`msClient.SendData` blocks while the algorithm runs and publishes its reply.
Timing around that outer call would include RabbitMQ publishing.

`algorithm_ms` includes the algorithm or service chain and local gRPC
handoff inside the pod, normally a few milliseconds. It contains no
cross-node network or RabbitMQ time. The handoff and reply stamps come
from the same pod and process, so cross-node clock skew does not affect
it.

### Planned gateway reporting

The agent already returns the complete `MicroserviceCommunication` JSON,
including `metadata`, to the gateway. The gateway can read
`dynamos_algorithm_ms` and add these optional fields:

| Location | Planned field | Meaning |
|---|---|---|
| `calls.<participant>` | `algorithm_ms` | Total sidecar-measured algorithm time for that participant in the phase. |
| `calls.<participant>` | `communication_ms` | `duration_ms - algorithm_ms`; communication and middleware time. |
| Phase summary | `algorithm_ms`, `communication_ms` | Totals across participants in the phase. |
| `participants.<name>` | `algorithm_ms`, `communication_ms` | Round totals for each participant. |
| Round and run timing | `algorithm_ms`, `communication_ms` | Totals for comparing algorithms and deployments. |

If a response lacks the metadata key, the gateway should omit the new fields
rather than report zero. Existing timing fields remain unchanged.

The planned name is `communication_ms`, but it means communication **and
middleware** time, not pure network latency.

### Compatibility and impact

- **Long-running sidecars:** agents, orchestrator, policy enforcer, and API
  gateway do not use the job-chain `ChainConsume` path, so their behaviour is
  unaffected.
- **Job pods:** all job-pod sidecars use the changed code. Adding keys to
  `MicroserviceCommunication.metadata` (`map<string, string>`) is
  backward-compatible and requires no protobuf changes.
- **Algorithms:** no changes are required when an algorithm returns the
  received message through the DYNAMOS Python library
  `send_data(msComm, data, metadata)`; it preserves existing metadata. A
  service that builds a new message instead will produce no measurement,
  without failing.
- **Multi-service chains:** the first service receives from the sidecar,
  and the last service returns through the sidecar. The value covers the
  whole chain, not one service.
- **Agents:** no rebuild is needed. Agents create job sidecars using
  `dynamos1/sidecar:${SIDECAR_TAG}`, falling back to `latest`. Deploy a
  dedicated sidecar tag and set `SIDECAR_TAG` in the agent charts, rather
  than overwriting the shared `latest` image.

### Validation plan

1. Add Go unit tests for stamping, duration calculation, cleanup,
   missing or malformed metadata, and preserving existing metadata.
2. Add gateway tests for reading the value, omitting missing values,
   calculating totals, and preventing negative `communication_ms`.
3. Run S1 locally and check that, for each call,
   `algorithm_ms + communication_ms = duration_ms`.
4. Repeat S1 across deployment configurations. Expect `algorithm_ms` to
   stay comparable while `communication_ms` reflects the changed placement.

### Remaining limitations

- `communication_ms` cannot separate RabbitMQ queueing from network
  transport. RabbitMQ metrics or Jaeger traces can support diagnosis.
- Policy reasoning remains combined with policy messaging.
- The VFL client shutdown timeout must be fixed before `total_ms` is a
  reliable deployment comparison metric.

## Source references

- Gateway timing implementation: [`timing.go`](../../go/cmd/api-gateway/timing.go)
- VFL timing integration: [`requests.go`](../../go/cmd/api-gateway/requests.go)
- Sidecar chain consumer: [`rabbit_ms_chain.go`](../../go/cmd/sidecar/rabbit_ms_chain.go)
- Sidecar reply handler: [`grpc_server.go`](../../go/cmd/sidecar/grpc_server.go)
- Example result: [`S1-20261021-150459.json`](../vfl-scenarios/S1-20261021-150459.json)

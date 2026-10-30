#!/usr/bin/env python3
"""Run encrypted VFL against an existing installation; no monitoring dependencies."""

import argparse
import csv
import json
import math
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
STRATEGY = "fed-encrypt-dynamic"
PARTIES = {"server", "clientone", "clienttwo", "clientthree"}
PROVIDERS = PARTIES | {"authority", "aggregator"}
STAGES = {
    "vflSampleBatchRequest": {"server"},
    "vflExtractCiphertextsRequest": PARTIES,
    "vflMIFEDKGenerationRequest": {"authority"},
    "vflFeaturesDecryptionRequest": {"aggregator"},
    "vflCalculateAccuracyRequest": {"server"},
    "vflSIFEDKGenerationRequest": {"authority"},
    "vflSamplesDecryptionRequest": {"aggregator"},
}
INITIALIZATION = {
    "authority_initialize": {"authority"}, "parties_initialize": PARTIES,
    "aggregator_initialize": {"aggregator"},
}
WARNING = (
    "Timing describes gateway/sidecar reply processing, not pure cryptographic CPU "
    "time or independently verified background completion. Accuracy is a batch fraction [0,1] "
    "before the final weight update. Policy mutation latency does not measure event propagation."
)
HEADERS = {"Host": "api-gateway.api-gateway.svc.cluster.local", "Content-Type": "application/json"}


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, allow_nan=False) + "\n", encoding="utf-8")
    temporary.replace(path)


def number(obj, key):
    value = obj[key]
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0:
        raise ValueError(f"{key}: expected a finite nonnegative number, got {value!r}")
    return value


def close(actual, expected, context):
    if not math.isclose(actual, expected, rel_tol=1e-6, abs_tol=0.05):
        raise ValueError(f"{context}: {actual} != {expected}")


def union(intervals):
    end = None
    total = 0
    for start, finish in sorted(intervals):
        total += max(0, finish - max(start, end if end is not None else start))
        end = max(finish, end if end is not None else finish)
    return total


def check_calls(summary, calls, context, recovered=False):
    measured = [call for call in calls if not call["failed"] and "algorithm_ms" in call]
    failures = sum(call["failed"] for call in calls)
    for key, expected in {
        "attempts": len(calls), "failures": failures,
        "measured_calls": len(measured), "unmeasured_calls": len(calls) - len(measured),
        "bytes_sent": sum(number(call, "bytes_sent") for call in calls),
        "bytes_received": sum(number(call, "bytes_received") for call in calls),
    }.items():
        if number(summary, key) != expected:
            raise ValueError(f"{context}/{key}: expected {expected}")
    close(number(summary, "request_elapsed_sum_ms"), sum(number(call, "duration_ms") for call in calls), context)
    if len(calls) - len(measured) != failures:
        raise ValueError(f"{context}: successful calls have missing handler timing")
    if failures and not recovered:
        raise ValueError(f"{context}: failed calls")
    if measured:
        algorithm = sum(number(call, "algorithm_ms") for call in measured)
        rtt = sum(call["duration_ms"] for call in measured)
        close(number(summary, "algorithm_ms"), algorithm, context)
        close(number(summary, "measured_round_trip_ms"), rtt, context)
        close(number(summary, "communication_ms"), rtt - algorithm, context)


def validate_summary(summary, expected=None, recovered=False):
    phases = summary["phases"]
    order = summary["phase_order"]
    if len(order) != len(set(order)) or set(order) != set(phases):
        raise ValueError("phase_order does not match unique phases")
    if expected is not None and order != list(expected):
        raise ValueError(f"unexpected stage order: {order}")
    all_calls, intervals = [], []
    for stage in order:
        phase = phases[stage]
        start, end = number(phase, "start_ms"), number(phase, "end_ms")
        if end < start or end > number(summary, "duration_ms") + 0.05:
            raise ValueError(f"{stage}: invalid stage interval")
        close(number(phase, "duration_ms"), end - start, stage)
        calls = phase["calls"]
        if not calls:
            raise ValueError(f"{stage}: empty calls")
        names = {call["participant"] for call in calls}
        if expected is not None:
            if names != expected[stage] or (not recovered and len(calls) != len(names)):
                raise ValueError(f"{stage}: missing, duplicate or unexpected participants")
        for call in calls:
            if type(call["failed"]) is not bool or call["stage"] != stage:
                raise ValueError(f"{stage}: invalid call status/stage")
            begin, finish = number(call, "start_ms"), number(call, "end_ms")
            if begin < start - 0.05 or finish > end + 0.05 or finish < begin:
                raise ValueError(f"{stage}: call interval outside stage")
            close(number(call, "duration_ms"), finish - begin, stage)
            if "algorithm_ms" in call and number(call, "algorithm_ms") > call["duration_ms"] + 0.001:
                raise ValueError(f"{stage}: handler timing exceeds RTT")
        check_calls(phase, calls, stage, recovered)
        close(start, min(call["start_ms"] for call in calls), stage)
        close(end, max(call["end_ms"] for call in calls), stage)
        replies = [max(call["end_ms"] for call in calls if call["participant"] == name) for name in names]
        close(number(phase, "barrier_wait_ms"), sum(end - reply for reply in replies), stage)
        if recovered and any(not any(not call["failed"] for call in calls if call["participant"] == name) for name in names):
            raise ValueError(f"{stage}: participant never recovered")
        all_calls.extend(calls)
        intervals.append((start, end))
    check_calls(summary, all_calls, "summary", recovered)
    covered = union(intervals)
    stage_sum = sum(end - start for start, end in intervals)
    close(number(summary, "stage_union_ms"), covered, "stage union")
    close(number(summary, "stage_elapsed_sum_ms"), stage_sum, "stage sum")
    close(number(summary, "stage_overlap_ms"), stage_sum - covered, "stage overlap")
    close(number(summary, "outside_stage_ms"), summary["duration_ms"] - covered, "outside stages")
    close(number(summary, "reply_barrier_wait_ms"), sum(phases[name]["barrier_wait_ms"] for name in order), "barrier")
    participants = summary["participants"]
    if set(participants) != {call["participant"] for call in all_calls}:
        raise ValueError("participant summary coverage mismatch")
    for name, participant in participants.items():
        group = [call for call in all_calls if call["participant"] == name]
        check_calls(participant, group, name, recovered)
        active = union([(call["start_ms"], call["end_ms"]) for call in group])
        close(number(participant, "request_active_ms"), active, name)
        close(number(participant, "no_outstanding_request_ms"), summary["duration_ms"] - active, name)
    return all_calls


def validate_result(result, expected=None):
    issues, warnings = [], [WARNING]
    try:
        if result["status"] != "done" or not isinstance(result["request_id"], str) or not result["request_id"]:
            raise ValueError(f"run did not complete successfully: {result.get('status')!r}")
        metadata = result["metadata"]
        if metadata["strategy"] != STRATEGY or metadata.get("errors"):
            raise ValueError("wrong strategy or gateway reported errors")
        rounds = metadata["total_rounds"]
        if type(rounds) is not int or rounds < 1:
            raise ValueError("invalid round count")
        if expected:
            for key, value in expected.items():
                if metadata.get(key) != value:
                    raise ValueError(f"metadata {key} does not match requested {value}")
        results, timing = result["results"], metadata["timing"]
        if [entry["train_round"] for entry in results] != list(range(rounds)):
            raise ValueError("missing, duplicate or unordered result rounds")
        if metadata["completed_rounds"] != rounds or metadata["attempted_rounds"] != rounds:
            raise ValueError("incomplete completed/attempted round counts")
        validate_summary(timing["warmup"], {"warmup": PROVIDERS}, recovered=True)
        if timing["warmup"]["failures"]:
            warnings.append(f"Warmup recovered from {timing['warmup']['failures']} failed attempt(s); inspect gateway/worker logs.")
        validate_summary(timing["initialization"], INITIALIZATION)
        validate_summary(timing["shutdown"], {"shutdown": PROVIDERS})
        if len(timing["rounds"]) != rounds:
            raise ValueError("incomplete round timing")
        round_ms = 0
        for index, (entry, measured) in enumerate(zip(results, timing["rounds"])):
            if entry["timing"] != measured or measured["train_round"] != index or measured["failed"]:
                raise ValueError(f"round {index}: result/timing mismatch or failure")
            accuracy = number(entry, "accuracy")
            if accuracy > 1:
                raise ValueError("accuracy must be a fraction, not percent")
            members = entry["participants"]
            names = [member["Auth"] for member in members]
            if len(names) != len(set(names)) or set(names) != PARTIES:
                raise ValueError("unexpected party membership (this runner expects the configured four parties)")
            if any(type(member["Active"]) is not bool for member in members):
                raise ValueError("invalid active-party flag")
            if entry["clients"] != sum(member["Active"] for member in members):
                raise ValueError("active-party count mismatch")
            validate_summary(measured, STAGES)
            if "reinitialization" in measured:
                validate_summary(measured["reinitialization"], INITIALIZATION)
            if number(measured, "round_ms") + 0.05 < measured["duration_ms"]:
                raise ValueError("round shorter than training stages")
            round_ms += measured["round_ms"]
        if timing["training_ms"] + 0.05 < round_ms:
            raise ValueError("training duration shorter than rounds")
        expected_actions = [
            (metadata[key], action) for key, action in
            [("policy_removal", "removal"), ("policy_reintroduction", "reintroduction")]
            if metadata[key] >= 0
        ]
        if [(action["train_round"], action["action"]) for action in timing["policy_actions"]] != expected_actions:
            raise ValueError("missing or unexpected policy mutation actions")
        for action in timing["policy_actions"]:
            number(action, "duration_ms")
            if action["failed"]:
                raise ValueError("policy mutation failed")
        if expected_actions:
            warnings.append("Policy mutations were acknowledged, but event-application timing and effective mask changes are not independently verified.")
        expected_root = {"warmup/warmup": PROVIDERS}
        expected_root.update({"initialization/" + key: value for key, value in INITIALIZATION.items()})
        for index, measured in enumerate(timing["rounds"]):
            if "reinitialization" in measured:
                expected_root.update({f"round/{index}/reinitialization/{key}": value for key, value in INITIALIZATION.items()})
            expected_root.update({f"round/{index}/{key}": value for key, value in STAGES.items()})
        expected_root["shutdown/shutdown"] = PROVIDERS
        all_calls = validate_summary(timing, expected_root, recovered=True)
        nested_phases = [("warmup", timing["warmup"]), ("initialization", timing["initialization"]),
                         ("shutdown", timing["shutdown"])]
        for index, measured in enumerate(timing["rounds"]):
            nested_phases.append((f"round/{index}", measured))
            if "reinitialization" in measured:
                nested_phases.append((f"round/{index}/reinitialization", measured["reinitialization"]))
        for prefix, measured in nested_phases:
            for name, phase in measured["phases"].items():
                root_phase = timing["phases"][prefix + "/" + name]
                close(root_phase["duration_ms"], phase["duration_ms"], prefix + "/" + name)
                def signatures(calls):
                    return sorted(json.dumps({key: value for key, value in call.items()
                                              if key not in {"stage", "start_ms", "end_ms"}}, sort_keys=True)
                                  for call in calls)
                if signatures(root_phase["calls"]) != signatures(phase["calls"]):
                    raise ValueError(f"{prefix}/{name}: run/nested call metrics disagree")
        nested = [timing["warmup"], timing["initialization"], timing["shutdown"], *timing["rounds"]]
        nested += [entry["reinitialization"] for entry in timing["rounds"] if "reinitialization" in entry]
        if timing["attempts"] != sum(item["attempts"] for item in nested):
            raise ValueError("lifecycle/run call count mismatch")
        if len(all_calls) != 18 + 10 * rounds + timing["warmup"]["attempts"] - 6 + 6 * sum("reinitialization" in entry for entry in timing["rounds"]):
            raise ValueError("unexpected encrypted-strategy call count")
        minimum = timing["warmup"]["duration_ms"] + timing["initialization"]["duration_ms"] + timing["training_ms"] + timing["shutdown"]["duration_ms"]
        if timing["duration_ms"] + 0.05 < minimum:
            raise ValueError("lifecycle duration shorter than components")
        if number(timing, "total_ms") + 0.05 < number(timing, "approval_ms") + timing["duration_ms"]:
            raise ValueError("total lifecycle is shorter than approval plus execution")
        if timing["policy_check_measured"] is not False or timing["background_completion_verified"] is not False:
            raise ValueError("unsupported policy/background timing claims")
    except (KeyError, TypeError, ValueError, AttributeError, IndexError) as error:
        issues.append(str(error))
    return {"valid": not issues, "issues": issues, "warnings": warnings, "background_completion_verified": False}


def write_csv(path, rows):
    fields = list(dict.fromkeys(key for row in rows for key in row))
    with path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        writer.writerows(rows)


def scalars(value):
    return {key: item for key, item in value.items() if not isinstance(item, (dict, list))}


def plot_metrics(result, directory):
    try:
        import matplotlib
    except ImportError as error:
        raise RuntimeError(
            "Plot generation requires Matplotlib; install scripts/requirements-experiment.txt"
        ) from error

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    directory.mkdir(parents=True, exist_ok=True)
    results = result["results"]
    rounds = [entry["train_round"] for entry in results]
    accuracy = [entry["accuracy"] for entry in results]
    durations = [entry["timing"]["round_ms"] / 1000 for entry in results]
    active_parties = [sum(party["Active"] for party in entry["participants"]) for entry in results]
    timing = result["metadata"]["timing"]

    def save(name, title, xlabel, ylabel):
        plt.title(title)
        plt.xlabel(xlabel)
        plt.ylabel(ylabel)
        plt.grid(True, alpha=0.3)
        plt.tight_layout()
        plt.savefig(directory / name, dpi=150)
        plt.close()

    plt.figure(figsize=(10, 5))
    plt.plot(rounds, accuracy, marker=".", linewidth=1)
    plt.ylim(0, 1)
    save("accuracy.png", "Encrypted VFL accuracy by round", "Training round", "Accuracy (fraction)")

    plt.figure(figsize=(10, 5))
    plt.plot(rounds, durations, marker=".", linewidth=1)
    save("round-duration.png", "Encrypted VFL round duration", "Training round", "Round duration (seconds)")

    stage_series = {}
    for entry in results:
        round_timing = entry["timing"]
        for stage, phase in round_timing["phases"].items():
            stage_series.setdefault(stage, {})[entry["train_round"]] = phase["duration_ms"] / 1000
        reinitialization = round_timing.get("reinitialization", {})
        for stage, phase in reinitialization.get("phases", {}).items():
            stage_series.setdefault("reinitialization: " + stage, {})[entry["train_round"]] = phase["duration_ms"] / 1000
    plt.figure(figsize=(12, 6))
    for stage, values in sorted(stage_series.items()):
        plt.plot(rounds, [values.get(round_number, float("nan")) for round_number in rounds],
                 marker=".", linewidth=1, label=stage)
    plt.legend(fontsize="small", ncol=2)
    save("stage-duration.png", "Encrypted VFL stage duration by round", "Training round", "Stage duration (seconds)")

    plt.figure(figsize=(10, 5))
    plt.step(rounds, active_parties, where="mid", marker=".")
    save("active-participants.png", "Active VFL participants by round", "Training round", "Active participants")

    lifecycle = [
        ("Approval", timing["approval_ms"]),
        ("Warm-up", timing["warmup"]["duration_ms"]),
        ("Initialization", timing["initialization"]["duration_ms"]),
        ("Training", timing["training_ms"]),
        ("Shutdown", timing["shutdown"]["duration_ms"]),
        ("Total", timing["total_ms"]),
    ]
    plt.figure(figsize=(10, 5))
    plt.bar([name for name, _ in lifecycle], [duration / 1000 for _, duration in lifecycle])
    save("lifecycle-duration.png", "Encrypted VFL lifecycle duration", "Lifecycle phase", "Duration (seconds)")


def export_metrics(result, directory, expected=None):
    directory.mkdir(parents=True, exist_ok=True)
    validation = validate_result(result, expected)
    write_json(directory / "validation.json", validation)
    timing = result.get("metadata", {}).get("timing", {})
    write_json(directory / "run.json", timing)
    write_csv(directory / "run.csv", [{"request_id": result.get("request_id"), "status": result.get("status"), **scalars(timing)}])
    write_csv(directory / "rounds.csv", [
        {**scalars(entry), **scalars(entry.get("timing", {}))} for entry in result.get("results", [])
    ])
    phases, calls = [], []
    for stage, phase in timing.get("phases", {}).items():
        phases.append({"phase_key": stage, **scalars(phase)})
        calls.extend({"phase_key": stage, **scalars(call)} for call in phase["calls"])
    write_csv(directory / "phases.csv", phases)
    write_csv(directory / "calls.csv", calls)
    write_csv(directory / "participants.csv", [
        {"participant": name, **scalars(value)} for name, value in timing.get("participants", {}).items()
    ])
    write_csv(directory / "policy-actions.csv", timing.get("policy_actions", []))
    if validation["valid"]:
        plot_metrics(result, directory / "plots")
    return validation


def http_json(base, path, timeout, body=None):
    data = None if body is None else json.dumps(body).encode("utf-8")
    request = urllib.request.Request(base + path, data=data, headers=HEADERS)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {error.code} for {path}: {detail[:1000]}") from error


@contextmanager
def gateway(args, directory):
    def ready():
        try:
            return isinstance(http_json(args.api_base_url, "/getAvailableProviders", min(args.http_timeout, 5)), dict)
        except (OSError, ValueError, RuntimeError) as error:
            return str(error)

    if ready() is True:
        yield
        return
    if args.no_port_forward or args.api_base_url != "http://localhost:8080/api/v1":
        raise RuntimeError(f"Gateway unavailable at {args.api_base_url}; automatic port-forward is only for the default URL")
    with (directory / "port-forward.log").open("w", encoding="utf-8") as log:
        process = subprocess.Popen(
            ["kubectl", "port-forward", "svc/api-gateway", "8080:8080", "-n", "api-gateway"],
            stdout=log, stderr=subprocess.STDOUT,
        )
        try:
            deadline = time.monotonic() + 30
            while process.poll() is None and time.monotonic() < deadline:
                if ready() is True:
                    break
                time.sleep(1)
            else:
                raise RuntimeError(f"Port-forward failed readiness; inspect {directory / 'port-forward.log'}")
            yield
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


def payload(args):
    return {
        "type": "vflTrainModelRequest", "user": {"id": "GUID", "userName": "requestor"},
        "dataProviders": sorted(PROVIDERS),
        "data_request": {
            "type": "vflTrainModelRequest", "requestMetadata": {},
            "data": {"cycles": args.cycles, "sample_batch_size": args.sample_batch_size,
                     "policy_removal": args.policy_removal, "policy_reintroduction": args.policy_reintroduction,
                     "training_backtrack": 0},
        },
    }


def run_request(args, directory):
    request = payload(args)
    write_json(directory / "request.json", request)
    with (directory / "request.log").open("w", encoding="utf-8") as log:
        def report(message):
            print(message, flush=True)
            print(message, file=log, flush=True)
        report(f"Submitting {args.cycles} encrypted VFL rounds, batch={args.sample_batch_size}")
        # A failed submission can still have reached the gateway. Never retry it.
        submission = http_json(args.api_base_url, "/requestApproval", args.http_timeout, request)
        write_json(directory / "submission.json", submission)
        request_id = submission.get("request_id")
        if not isinstance(request_id, str) or not request_id or submission.get("status") != "pending":
            raise RuntimeError(f"Unexpected submission response: {submission}")
        report(f"Request ID: {request_id}")
        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            result = http_json(args.api_base_url, "/getTrainingStatus?id=" + urllib.parse.quote(request_id, safe=""),
                               min(args.http_timeout, max(0.001, deadline - time.monotonic())))
            if result.get("request_id") != request_id or result.get("status") not in {"pending", "done", "failed"}:
                raise RuntimeError("Unexpected polling response or request ID")
            write_json(directory / "result.json", result)
            report(f"Status: {result['status']}")
            if result["status"] in {"done", "failed"}:
                return result
            time.sleep(min(args.poll_interval, max(0, deadline - time.monotonic())))
        raise TimeoutError(f"Request {request_id} exceeded {args.timeout}s; latest snapshot saved")


def positive_int(value):
    parsed = int(value)
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be a positive integer")
    return parsed


def positive_float(value):
    parsed = float(value)
    if not math.isfinite(parsed) or parsed <= 0:
        raise argparse.ArgumentTypeError("must be finite and positive")
    return parsed


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cycles", type=positive_int, default=12, help="actual encrypted training rounds, not local steps/Q")
    parser.add_argument("--runs", type=positive_int, default=1)
    parser.add_argument("--sample-batch-size", type=positive_int, default=64)
    parser.add_argument("--policy-removal", type=int, default=-1, help="clientthree removal round; -1 disables")
    parser.add_argument("--policy-reintroduction", type=int, default=-1, help="clientthree reintroduction round; -1 disables")
    parser.add_argument("--timeout", type=positive_float, default=10800, help="polling deadline in seconds per run")
    parser.add_argument("--poll-interval", type=positive_float, default=5)
    parser.add_argument("--http-timeout", type=positive_float, default=30)
    parser.add_argument("--api-base-url", default="http://localhost:8080/api/v1")
    parser.add_argument("--no-port-forward", action="store_true")
    parser.add_argument("--output-dir", type=Path, default=ROOT / "experiments" / STRATEGY)
    output = parser.add_mutually_exclusive_group()
    output.add_argument("--export-result", type=Path, help="validate/export saved JSON and plots without contacting the cluster")
    output.add_argument("--plot-result", type=Path, help="generate plots from a validated saved result without contacting the cluster")
    args = parser.parse_args(argv)
    args.api_base_url = args.api_base_url.rstrip("/")
    if any(value < -1 or value >= args.cycles for value in (args.policy_removal, args.policy_reintroduction)):
        parser.error("policy round must be -1 or in [0, cycles)")
    if args.policy_reintroduction >= 0 and not 0 <= args.policy_removal < args.policy_reintroduction:
        parser.error("reintroduction requires an earlier removal round")
    if args.runs > 1 and args.policy_removal >= 0:
        parser.error("policy mutation runs require --runs=1; restore agreements before repeating")
    return args


def main(argv=None):
    args = parse_args(argv)
    if args.plot_result:
        result = json.loads(args.plot_result.read_text(encoding="utf-8"))
        validation = validate_result(result)
        if not validation["valid"]:
            raise RuntimeError("; ".join(validation["issues"]))
        plot_metrics(result, args.plot_result.parent / "metrics" / "plots")
        print(f"Plots saved to {args.plot_result.parent / 'metrics' / 'plots'}")
        return
    if args.export_result:
        result = json.loads(args.export_result.read_text(encoding="utf-8"))
        validation = export_metrics(result, args.export_result.parent / "metrics")
        for warning in validation["warnings"]:
            print(f"WARNING: {warning}", flush=True)
        if not validation["valid"]:
            raise RuntimeError("; ".join(validation["issues"]))
        print(f"Metrics exported to {args.export_result.parent / 'metrics'}")
        return
    args.output_dir.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix=datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ-"), dir=args.output_dir))
    manifest = {
        "strategy": STRATEGY, "started_at": datetime.now(timezone.utc).isoformat(), "status": "running",
        "parameters": payload(args), "runs": args.runs, "timeout_seconds": args.timeout,
        "api_base_url": args.api_base_url, "metrics_source": "gateway JSON and job-sidecar timing; no monitoring stack",
        "background_completion_verified": False, "warning": WARNING,
    }
    write_json(directory / "experiment.json", manifest)
    print(f"Experiment folder: {directory}", flush=True)
    print(f"WARNING: {WARNING}", flush=True)
    try:
        with gateway(args, directory):
            for index in range(1, args.runs + 1):
                run = directory / f"run-{index:03d}"
                run.mkdir()
                result = run_request(args, run)
                expected = {"total_rounds": args.cycles, "sample_batch_size": args.sample_batch_size,
                            "policy_removal": args.policy_removal, "policy_reintroduction": args.policy_reintroduction}
                validation = export_metrics(result, run / "metrics", expected)
                for warning in validation["warnings"][1:]:
                    print(f"WARNING: {warning}", flush=True)
                if not validation["valid"]:
                    raise RuntimeError(f"Run {index} failed validation: {'; '.join(validation['issues'])}. Inspect {run}")
        manifest["status"] = "done"
        print(f"Validated results and CSVs saved: {directory}", flush=True)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError, KeyboardInterrupt) as error:
        manifest["status"], manifest["error"] = "failed", str(error)
        raise RuntimeError(
            f"{error}. Evidence: {directory}. A submitted run may still be active; "
            "do not blindly resubmit. This runner does not cancel cluster work."
        ) from error
    finally:
        manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
        write_json(directory / "experiment.json", manifest)


if __name__ == "__main__":
    main()

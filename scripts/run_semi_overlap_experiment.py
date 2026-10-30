"""Run semi-overlap FedBCD; export gateway/sidecar metrics and overlap-aware plots."""

import argparse
import csv
from datetime import datetime, timezone
import importlib.util
import json
import math
import os
from pathlib import Path
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
STRATEGY = "semi-overlap-fed-bcd"
STAGES = ("server_sample", "client_forward", "server_aggregate", "client_update_dispatch")
CLIENTS = {"clientone", "clienttwo", "clientthree"}
COMPLETION_WARNING = (
    "Gateway replies and accuracy snapshots do not independently confirm completion "
    "of background training. Handler timing is not pure compute time."
)


def write_json(path, data):
    with path.open("w", encoding="utf-8") as destination:
        json.dump(data, destination, indent=2, allow_nan=False)
        destination.write("\n")


def scalars(data):
    return {key: value for key, value in data.items() if not isinstance(value, (dict, list))}


def write_csv(path, rows):
    columns = list(dict.fromkeys(key for row in rows for key in row))
    with path.open("w", newline="", encoding="utf-8") as destination:
        writer = csv.DictWriter(destination, fieldnames=columns)
        writer.writeheader()
        writer.writerows(rows)


def number(data, key, context):
    value = data.get(key)
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{context}: {key} must be a number")
    if not math.isfinite(value) or value < 0:
        raise ValueError(f"{context}: {key} must be finite and nonnegative")
    return value


def integer(data, key, context):
    value = number(data, key, context)
    if int(value) != value:
        raise ValueError(f"{context}: {key} must be an integer")
    return int(value)


def close(actual, expected, context, tolerance=0.02):
    if abs(actual - expected) > tolerance:
        raise ValueError(f"{context}: {actual} does not match {expected}")


def interval_union(intervals):
    end = 0
    total = 0
    for left, right in sorted(intervals):
        total += max(0, right - max(left, end))
        end = max(end, right)
    return total


def validate_phase(phase, context, participants, retry=False):
    number(phase, "duration_ms", context)
    number(phase, "barrier_wait_ms", context)
    calls = phase["calls"]
    if set(calls) != participants:
        raise ValueError(f"{context}: expected participants {sorted(participants)}, got {sorted(calls)}")
    measured = unmeasured = 0
    sent = received = algorithm = communication = rtt = 0
    for name, call in calls.items():
        label = f"{context}/{name}"
        attempts = integer(call, "attempts", label) if "attempts" in call else 1
        if attempts < 1 or (not retry and attempts != 1):
            raise ValueError(f"{label}: unexpected attempt count {attempts}")
        count = integer(call, "measured_calls", label)
        failures = integer(call, "failures", label) if "failures" in call else 0
        missing = integer(call, "unmeasured_calls", label)
        if failures >= attempts or (failures and not retry):
            raise ValueError(f"{label}: failed calls without successful warmup recovery")
        if count != attempts - failures or missing != failures:
            raise ValueError(f"{label}: incomplete sidecar timing coverage")
        elapsed = number(call, "duration_ms", label)
        handler = number(call, "algorithm_ms", label)
        middleware = number(call, "communication_ms", label)
        trip = number(call, "measured_round_trip_ms", label)
        close(handler + middleware, trip, label)
        if failures:
            if trip > elapsed + 0.02:
                raise ValueError(f"{label}: measured RTT exceeds total attempted time")
        else:
            close(elapsed, trip, label)
        if elapsed > phase["duration_ms"] + 0.02:
            raise ValueError(f"{label}: request exceeds its phase duration")
        sent += integer(call, "bytes_sent", label)
        received += integer(call, "bytes_received", label)
        algorithm += handler
        communication += middleware
        rtt += trip
        measured += count
        unmeasured += missing
    for key, expected in {
        "measured_calls": measured, "unmeasured_calls": unmeasured,
        "bytes_sent": sent, "bytes_received": received,
        "algorithm_ms": algorithm, "communication_ms": communication,
        "measured_round_trip_ms": rtt,
    }.items():
        if key in ("measured_calls", "unmeasured_calls", "bytes_sent", "bytes_received"):
            if integer(phase, key, context) != expected:
                raise ValueError(f"{context}/{key}: incorrect total")
        else:
            close(number(phase, key, context), expected, f"{context}/{key}")


def validate_summary(timing, context, rounds):
    duration = number(timing, "duration_ms", context)
    phases = timing["phases"]
    expected_keys = {f"{cycle}/{stage}" for cycle in rounds for stage in STAGES}
    if set(phases) != expected_keys:
        raise ValueError(f"{context}: missing or unexpected cycle/stage identifiers")
    if set(timing["phase_order"]) != expected_keys or len(timing["phase_order"]) != len(expected_keys):
        raise ValueError(f"{context}: invalid phase_order")
    intervals = []
    sums = {key: 0 for key in (
        "measured_calls", "unmeasured_calls", "bytes_sent", "bytes_received",
        "algorithm_ms", "communication_ms", "measured_round_trip_ms",
    )}
    for key, phase in phases.items():
        cycle, stage = key.split("/", 1)
        if phase["cycle"] != int(cycle):
            raise ValueError(f"{context}/{key}: cycle correlation mismatch")
        participants = {"server"} if stage.startswith("server_") else CLIENTS
        validate_phase(phase, f"{context}/{key}", participants)
        left = number(phase, "start_offset_ms", key)
        right = number(phase, "end_offset_ms", key)
        if right < left or right > duration + 0.02:
            raise ValueError(f"{context}/{key}: stage interval outside summary")
        close(right - left, phase["duration_ms"], key)
        intervals.append((left, right))
        for field in sums:
            sums[field] += phase[field]
    tolerance = max(0.02, len(phases) * 0.003)
    union = interval_union(intervals)
    elapsed_sum = sum(right - left for left, right in intervals)
    for field, expected in {
        **sums, "stage_union_ms": union, "stage_elapsed_sum_ms": elapsed_sum,
        "stage_overlap_ms": elapsed_sum - union, "outside_stage_ms": duration - union,
    }.items():
        if field in ("measured_calls", "unmeasured_calls", "bytes_sent", "bytes_received"):
            if integer(timing, field, context) != expected:
                raise ValueError(f"{context}/{field}: incorrect total")
        else:
            close(number(timing, field, context), expected, f"{context}/{field}", tolerance)
    if timing.get("policy_check_measured") is not False:
        raise ValueError(f"{context}: expected unmeasured event-driven policy semantics")
    if set(timing["participants"]) != CLIENTS | {"server"}:
        raise ValueError(f"{context}: missing participant activity")
    for name, participant in timing["participants"].items():
        active = number(participant, "request_active_ms", name)
        idle = number(participant, "request_idle_ms", name)
        close(active + idle, duration, name)
        if active > number(participant, "request_elapsed_sum_ms", name) + 0.02:
            raise ValueError(f"{context}/{name}: request union exceeds summed time")
        calls = [phase["calls"][name] for phase in phases.values() if name in phase["calls"]]
        for field in ("measured_calls", "unmeasured_calls"):
            if integer(participant, field, name) != sum(call[field] for call in calls):
                raise ValueError(f"{context}/{name}: incorrect {field}")
        for field in ("algorithm_ms", "communication_ms", "measured_round_trip_ms"):
            close(number(participant, field, name), sum(call[field] for call in calls),
                  f"{context}/{name}/{field}", tolerance)
        close(participant["request_elapsed_sum_ms"], sum(call["duration_ms"] for call in calls),
              f"{context}/{name}/request_elapsed_sum_ms", tolerance)
    close(number(timing, "reply_barrier_wait_ms", context),
          sum(phase["barrier_wait_ms"] for phase in phases.values()),
          f"{context}/reply_barrier_wait_ms", tolerance)


def validate_result(result, expected=None):
    issues = []
    try:
        if not isinstance(result["request_id"], str) or not result["request_id"]:
            issues.append("Missing request ID.")
        if result["status"] != "done":
            issues.append(f"Training status is {result['status']!r}, not 'done'.")
        metadata = result["metadata"]
        if metadata["strategy"] != STRATEGY:
            issues.append(f"Expected strategy {STRATEGY}.")
        cycles = integer(metadata, "total_rounds", "metadata")
        frequency = integer(metadata, "communication_frequency", "metadata")
        if integer(metadata, "sample_batch_size", "metadata") < 1:
            issues.append("Sample batch size must be positive.")
        if frequency < 2 or cycles < frequency or cycles % frequency:
            raise ValueError("Invalid local-step/Q schedule")
        rounds = cycles // frequency
        if integer(metadata, "synchronization_rounds", "metadata") != rounds or integer(metadata, "scheduled_local_steps", "metadata") != cycles:
            issues.append("Scheduled work does not match the requested local steps/Q.")
        if expected:
            for key, value in expected.items():
                if metadata.get(key) != value:
                    issues.append(f"Result metadata {key} does not match requested value {value}.")
        results = result["results"]
        if [entry["train_round"] for entry in results] != list(range(rounds)):
            issues.append(f"Expected unique ordered cycles 0..{rounds - 1}; got incomplete/duplicate results.")
        if integer(metadata, "acknowledged_cycles", "metadata") != rounds or len(results) != rounds:
            issues.append(f"Expected {rounds} acknowledged cycles; got {len(results)}.")
        for entry in results:
            cycle = integer(entry, "train_round", "result")
            if integer(entry, "clients", f"cycle {cycle}") != 3:
                issues.append(f"Cycle {cycle}: expected all three clients.")
            if number(entry, "accuracy", f"cycle {cycle}") > 100:
                issues.append(f"Cycle {cycle}: accuracy exceeds 100 percent.")
            validate_summary(entry["timing"], f"cycle {cycle}", [cycle])
            close(number(entry["timing"], "cycle_latency_ms", str(cycle)),
                  entry["timing"]["duration_ms"], f"cycle {cycle} latency")
        timing = metadata["timing"]
        validate_summary(timing, "pipeline", range(rounds))
        for entry in results:
            for key, phase in entry["timing"]["phases"].items():
                run_phase = timing["phases"][key]
                if phase["calls"] != run_phase["calls"]:
                    issues.append(f"{key}: cycle/run call metrics disagree.")
                close(phase["duration_ms"], run_phase["duration_ms"], key)
        for name in ("warmup", "shutdown", "final_accuracy"):
            validate_phase(timing[name], name, {"server"} if name == "final_accuracy" else CLIENTS | {"server"}, retry=name == "warmup")
        close(number(timing, "enqueue_ms", "pipeline") + number(timing, "drain_ms", "pipeline"),
              timing["duration_ms"], "enqueue + drain")
        if number(timing, "fill_ms", "pipeline") > timing["duration_ms"] + 0.02:
            issues.append("Pipeline fill exceeds its wall duration.")
        lifecycle = sum(number(timing[name], "duration_ms", name) for name in ("warmup", "final_accuracy", "shutdown"))
        minimum = timing["duration_ms"] + number(timing, "approval_ms", "pipeline") + lifecycle
        if number(timing, "total_ms", "lifecycle") + 0.02 < minimum:
            issues.append("Lifecycle total is shorter than its nonoverlapping components.")
        if metadata.get("pipeline_errors") or metadata.get("accuracy_error"):
            issues.append("Gateway reported pipeline or accuracy errors.")
    except (KeyError, TypeError, ValueError, AttributeError) as error:
        issues.append(f"Invalid or incomplete result metrics: {error}")
    return issues


def export_metrics(result, directory, expected=None):
    """Run phases are authoritative, including failed cycles absent from results."""
    directory.mkdir(parents=True, exist_ok=True)
    issues = validate_result(result, expected)
    timing = result.get("metadata", {}).get("timing", {})
    warnings = [COMPLETION_WARNING]
    for participant, call in timing.get("warmup", {}).get("calls", {}).items():
        if call.get("failures", 0) and call.get("measured_calls", 0):
            warning = f"Warmup for {participant} had {call['failures']} failed attempt(s) before a measured reply; inspect request/gateway logs."
            warnings.append(warning)
            print(f"WARNING: {warning}", flush=True)
    write_json(directory / "validation.json", {
        "valid": not issues, "issues": issues, "warnings": warnings,
        "background_completion_verified": False,
    })
    write_json(directory / "run.json", timing)
    write_csv(directory / "run.csv", [{
        "request_id": result.get("request_id"), "status": result.get("status"), **scalars(timing),
    }])
    rounds, phases, calls, participants = [], [], [], []

    def add_phase(scope, key, phase):
        context = {"scope": scope, "phase_key": key, "train_round": phase.get("cycle", ""),
                   "phase": key.split("/", 1)[-1]}
        phases.append({**context, **scalars(phase)})
        for participant, call in phase.get("calls", {}).items():
            calls.append({**context, "participant": participant, **scalars(call)})

    for key, phase in timing.get("phases", {}).items():
        add_phase("pipeline", key, phase)
    for name in ("warmup", "final_accuracy", "shutdown"):
        if name in timing:
            add_phase("lifecycle", name, timing[name])
    for entry in result.get("results", []):
        cycle = entry["train_round"]
        rounds.append({**scalars(entry), **scalars(entry.get("timing", {}))})
        for participant, values in entry.get("timing", {}).get("participants", {}).items():
            participants.append({"scope": "cycle", "train_round": cycle, "participant": participant, **scalars(values)})
    for participant, values in timing.get("participants", {}).items():
        participants.append({"scope": "pipeline", "train_round": "", "participant": participant, **scalars(values)})
    for name, rows in (("rounds", rounds), ("phases", phases), ("calls", calls), ("participants", participants)):
        write_csv(directory / f"{name}.csv", rows)
    return issues


def generate_plots(path):
    subprocess.run([
        sys.executable, str(ROOT / "plots" / "plot_semi_overlap_timing.py"),
        str(path), "--output-dir", str(path.parent / "plots"),
    ], check=True)


def run_request(directory, cycles, communication_frequency, sample_batch_size):
    environment = os.environ.copy()
    environment.update(
        CYCLES=str(cycles), COMMUNICATION_FREQUENCY=str(communication_frequency),
        SAMPLE_BATCH_SIZE=str(sample_batch_size), OUTPUT_FILE=str(directory / "result.json"),
    )
    environment.setdefault("MAX_POLLS", "2160")
    with (directory / "request.log").open("w", encoding="utf-8") as log:
        return subprocess.run(
            ["bash", str(ROOT / "scripts" / "single_request.sh")],
            env=environment, stdout=log, stderr=subprocess.STDOUT, check=False,
        ).returncode


def positive_integer(value):
    number = int(value)
    if number < 1:
        raise argparse.ArgumentTypeError("must be a positive integer")
    return number


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cycles", type=positive_integer, default=120, help="requested local steps")
    parser.add_argument("--runs", type=positive_integer, default=1)
    parser.add_argument("--communication-frequency", type=positive_integer, default=10)
    parser.add_argument("--sample-batch-size", type=positive_integer, default=64)
    parser.add_argument("--output-dir", type=Path, default=ROOT / "experiments" / STRATEGY)
    parser.add_argument("--export-result", type=Path, help="export saved JSON without submitting training")
    args = parser.parse_args()
    if args.export_result:
        path = args.export_result.resolve()
        issues = export_metrics(json.loads(path.read_text(encoding="utf-8")), path.parent / "metrics")
        print(f"Metrics exported to {path.parent / 'metrics'}")
        if issues:
            raise RuntimeError("Result validation failed: " + "; ".join(issues))
        return
    if args.communication_frequency < 2 or args.cycles < args.communication_frequency or args.cycles % args.communication_frequency:
        parser.error("--communication-frequency must be >= 2; --cycles must be a positive multiple of it")
    if importlib.util.find_spec("matplotlib") is None:
        parser.error("Install plot dependencies: python3 -m pip install -r plots/requirements.txt")
    args.output_dir = args.output_dir.resolve()
    args.output_dir.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    experiment = Path(tempfile.mkdtemp(prefix=f"{stamp}-", dir=args.output_dir))
    manifest = {
        "started_at": datetime.now(timezone.utc).isoformat(), "strategy": STRATEGY,
        "cycles": args.cycles, "runs": args.runs, "communication_frequency": args.communication_frequency,
        "sample_batch_size": args.sample_batch_size,
        "synchronization_rounds": args.cycles // args.communication_frequency,
        "learning_rate": 0.1, "training_backtrack": 0,
        "policy_removal": -1, "policy_reintroduction": -1,
        "metrics_source": "gateway JSON including sidecar timing; no monitoring stack",
        "background_completion_verified": False, "completion_warning": COMPLETION_WARNING,
        "status": "running",
    }
    write_json(experiment / "experiment.json", manifest)
    print(f"Experiment folder: {experiment}\nWARNING: {COMPLETION_WARNING}", flush=True)
    try:
        for number in range(1, args.runs + 1):
            directory = experiment / f"run-{number:03d}"
            directory.mkdir()
            print(f"Run {number}/{args.runs}: {args.cycles} local steps, "
                  f"{manifest['synchronization_rounds']} synchronization cycles; log: {directory / 'request.log'}", flush=True)
            code = run_request(directory, args.cycles, args.communication_frequency, args.sample_batch_size)
            path = directory / "result.json"
            issues = []
            if path.exists():
                result = json.loads(path.read_text(encoding="utf-8"))
                issues = export_metrics(result, directory / "metrics", {
                    "total_rounds": args.cycles, "communication_frequency": args.communication_frequency,
                    "sample_batch_size": args.sample_batch_size,
                })
            if code or not path.exists() or issues:
                raise RuntimeError(f"Run {number} failed (request exit {code}): {'; '.join(issues)}. "
                                   f"Inspect {directory}. A timed-out cluster run may still be active; do not blindly resubmit.")
            generate_plots(path)
        manifest["status"] = "done"
    except (Exception, KeyboardInterrupt) as error:
        manifest["status"] = "failed"
        manifest["error"] = str(error) or "Interrupted"
        raise
    finally:
        manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
        write_json(experiment / "experiment.json", manifest)
    print(f"Experiment exports completed: {experiment}", flush=True)


if __name__ == "__main__":
    main()

"""Run FedBCD training and export gateway/sidecar metrics and timing plots."""

import argparse
import csv
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]


def write_json(path, data):
    with path.open("w", encoding="utf-8") as destination:
        json.dump(data, destination, indent=2, allow_nan=False)
        destination.write("\n")


def scalars(data):
    return {
        key: value for key, value in data.items()
        if not isinstance(value, (dict, list))
    }


def write_csv(path, rows):
    columns = list(dict.fromkeys(key for row in rows for key in row))
    with path.open("w", newline="", encoding="utf-8") as destination:
        writer = csv.DictWriter(destination, fieldnames=columns)
        writer.writeheader()
        writer.writerows(rows)


def export_metrics(result, directory, cycles):
    """Keep nested JSON intact; CSVs expose each timing level separately."""
    directory.mkdir(parents=True, exist_ok=True)
    timing = result["metadata"]["timing"]
    write_json(directory / "run.json", timing)
    write_csv(directory / "run.csv", [{
        "request_id": result["request_id"],
        "status": result["status"],
        **scalars(timing),
    }])
    rounds, phases, calls, participants = [], [], [], []

    def add_phase(round_number, name, phase):
        context = {"train_round": round_number, "phase": name}
        phases.append({**context, **scalars(phase)})
        for participant, call in phase.get("calls", {}).items():
            calls.append({**context, "participant": participant, **scalars(call)})

    for name in ("warmup", "shutdown"):
        if name in timing:
            add_phase("", name, timing[name])
    for entry in result["results"]:
        round_number = entry["train_round"]
        round_timing = entry["timing"]
        rounds.append({**scalars(entry), **scalars(round_timing)})
        for name, phase in round_timing.get("phases", {}).items():
            add_phase(round_number, name, phase)
        for participant, values in round_timing.get("participants", {}).items():
            participants.append({
                "train_round": round_number,
                "participant": participant,
                **scalars(values),
            })
    for name, rows in (
        ("rounds", rounds), ("phases", phases),
        ("calls", calls), ("participants", participants),
    ):
        write_csv(directory / f"{name}.csv", rows)

    issues = []
    if result["status"] != "done":
        issues.append(f"Training status is {result['status']!r}, not 'done'.")
    if len(rounds) != cycles:
        issues.append(f"Expected {cycles} rounds; received {len(rounds)}.")
    if any(entry.get("clients") != 3 for entry in result["results"]):
        issues.append("At least one round did not include all three clients.")
    if not timing.get("measured_calls") or timing.get("unmeasured_calls") != 0:
        issues.append("Training calls have missing or invalid sidecar measurements.")
    if any(
        not entry["timing"].get("measured_calls")
        or entry["timing"].get("unmeasured_calls") != 0
        for entry in result["results"]
    ):
        issues.append("At least one round has incomplete sidecar measurement coverage.")
    for call in calls:
        if call.get("failures", 0):
            issues.append(
                f"{call['phase']}/{call['participant']} has failed calls."
            )
    write_json(directory / "validation.json", {"valid": not issues, "issues": issues})
    return issues


def export_result(path):
    with path.open(encoding="utf-8") as source:
        result = json.load(source)
    cycles = result["metadata"]["synchronization_rounds"]
    directory = path.parent / "metrics"
    issues = export_metrics(result, directory, cycles)
    print(f"Metrics exported to: {directory}", flush=True)
    if issues:
        raise RuntimeError("Result validation failed: " + "; ".join(issues))


def generate_plots(path):
    subprocess.run([
        sys.executable, str(ROOT / "plots" / "plot_vfl_timing.py"),
        str(path), "--labels", "FedBCD",
        "--output-dir", str(path.parent / "plots"),
    ], check=True)


def run_request(directory, cycles, communication_frequency, sample_batch_size):
    environment = os.environ.copy()
    environment.update(CYCLES=str(cycles), OUTPUT_FILE=str(directory / "result.json"))
    environment.update(
        COMMUNICATION_FREQUENCY=str(communication_frequency),
        SAMPLE_BATCH_SIZE=str(sample_batch_size),
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
    parser.add_argument("--cycles", type=positive_integer, default=120)
    parser.add_argument("--runs", type=positive_integer, default=1)
    parser.add_argument("--communication-frequency", type=positive_integer, default=10)
    parser.add_argument("--sample-batch-size", type=positive_integer, default=64)
    parser.add_argument("--output-dir", type=Path, default=ROOT / "experiments" / "fed-bcd")
    parser.add_argument(
        "--export-result", type=Path, metavar="RESULT_JSON",
        help="regenerate metrics beside a saved result; do not start training",
    )
    args = parser.parse_args()
    if args.export_result is not None:
        export_result(args.export_result)
        return
    if args.communication_frequency < 2:
        parser.error("--communication-frequency must be at least 2 for the current FedBCD worker")
    if args.cycles % args.communication_frequency or args.cycles < args.communication_frequency:
        parser.error("--cycles must be a positive multiple of --communication-frequency")
    import importlib.util
    if importlib.util.find_spec("matplotlib") is None:
        parser.error("Install plotting dependencies first: python3 -m pip install -r plots/requirements.txt")
    args.output_dir.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    experiment = Path(tempfile.mkdtemp(prefix=f"{stamp}-", dir=args.output_dir))
    print(f"Experiment folder: {experiment}", flush=True)
    manifest = {
        "started_at": datetime.now(timezone.utc).isoformat(),
        "cycles": args.cycles, "runs": args.runs,
        "strategy": "fed-bcd",
        "communication_frequency": args.communication_frequency,
        "sample_batch_size": args.sample_batch_size,
        "synchronization_rounds": args.cycles // args.communication_frequency,
        "metrics_source": "gateway JSON including sidecar-derived timing; no monitoring stack",
        "status": "running",
    }
    write_json(experiment / "experiment.json", manifest)
    try:
        for number in range(1, args.runs + 1):
            directory = experiment / f"run-{number:03d}"
            directory.mkdir()
            print(f"Run {number}/{args.runs}: {args.cycles} local cycles, "
                  f"{manifest['synchronization_rounds']} synchronization rounds; log: {directory / 'request.log'}",
                  flush=True)
            code = run_request(directory, args.cycles, args.communication_frequency, args.sample_batch_size)
            result_path = directory / "result.json"
            issues = []
            if result_path.exists():
                with result_path.open(encoding="utf-8") as source:
                    result = json.load(source)
                issues = export_metrics(result, directory / "metrics", manifest["synchronization_rounds"])
                for key in ("total_rounds", "communication_frequency", "sample_batch_size"):
                    expected = {
                        "total_rounds": args.cycles,
                        "communication_frequency": args.communication_frequency,
                        "sample_batch_size": args.sample_batch_size,
                    }[key]
                    if result["metadata"].get(key) != expected:
                        issues.append(f"Result metadata {key} does not match requested value {expected}.")
                write_json(directory / "metrics" / "validation.json", {"valid": not issues, "issues": issues})
                print(f"Metrics exported to: {directory / 'metrics'}", flush=True)
            if code or not result_path.exists() or issues:
                raise RuntimeError(
                    f"Run {number} failed validation (request exit code {code}): "
                    f"{'; '.join(issues)}. Inspect {directory}."
                )
            generate_plots(result_path)
        manifest["status"] = "done"
    except (Exception, KeyboardInterrupt) as error:
        manifest["status"] = "failed"
        manifest["error"] = str(error) or "Interrupted"
        raise
    finally:
        manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
        write_json(experiment / "experiment.json", manifest)
    print(f"Experiment completed: {experiment}", flush=True)


if __name__ == "__main__":
    main()

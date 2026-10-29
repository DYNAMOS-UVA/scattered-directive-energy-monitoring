"""Plot training-round timing metrics from scenario JSON exports."""

import argparse
import json
import math
import warnings
from dataclasses import dataclass
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt


COLORS = ("#4477AA", "#EE7733", "#228833")
MIB = 1024 * 1024


def number(data: dict, key: str, context: str) -> float:
    value = data.get(key)
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{context}: {key} must be a number")
    if not math.isfinite(value) or value < 0:
        raise ValueError(f"{context}: {key} must be finite and non-negative")
    return float(value)


@dataclass
class Run:
    path: Path
    label: str
    status: str
    rounds: list
    timing: list
    skipped: int = 0
    strategy: str = "baseline"

    def values(self, key: str, scale: float = 1) -> list:
        return [entry[key] / scale for entry in self.timing]

    def coverage(self) -> str:
        if any(entry["measured_calls"] is None for entry in self.timing):
            return "coverage unavailable"
        measured = sum(entry["measured_calls"] for entry in self.timing)
        total = measured + sum(entry["unmeasured_calls"] for entry in self.timing)
        return f"{measured}/{total} calls measured"

    def mean(self, key: str, scale: float = 1) -> float:
        return sum(self.values(key, scale)) / len(self.timing)

    def per_measured_call(self, key: str) -> float:
        measured = sum(entry["measured_calls"] or 0 for entry in self.timing)
        if not measured:
            return math.nan
        return sum(entry[key] for entry in self.timing if entry["measured_calls"]) / measured


def load_run(path: Path, label: str, skip_rounds: int = 0) -> Run:
    if skip_rounds < 0:
        raise ValueError("skip_rounds must be non-negative")
    with path.open(encoding="utf-8") as source:
        data = json.load(source)
    if not isinstance(data, dict) or not isinstance(data.get("results"), list):
        raise ValueError(f"{path}: expected an object containing a results list")
    if not data["results"]:
        raise ValueError(f"{path}: no completed rounds to plot")
    if skip_rounds >= len(data["results"]):
        raise ValueError(f"{path}: --skip-rounds leaves no rounds to plot")
    rounds, timings = [], []
    for index, result in enumerate(data["results"][skip_rounds:], start=skip_rounds):
        context = f"{path}: results[{index}]"
        if not isinstance(result, dict) or not isinstance(result.get("timing"), dict):
            raise ValueError(f"{context}: missing gateway timing; use a timing-enabled export")
        raw = result["timing"]
        timing = {
            key: number(raw, key, context)
            for key in (
                "round_ms", "policy_check_ms", "coordination_overhead_ms",
                "barrier_wait_ms", "bytes_sent", "bytes_received",
            )
        }
        phase_ms = timing["round_ms"] - timing["policy_check_ms"] - timing["coordination_overhead_ms"]
        if phase_ms < -0.01:
            raise ValueError(f"{context}: policy and coordination time exceed round time")
        timing["training_ms"] = max(0, phase_ms)
        timing["measured_calls"] = None
        timing["unmeasured_calls"] = None
        timing["algorithm_ms"] = math.nan
        timing["communication_ms"] = math.nan
        if "measured_calls" in raw or "unmeasured_calls" in raw:
            for key in ("measured_calls", "unmeasured_calls"):
                value = number(raw, key, context)
                if not value.is_integer():
                    raise ValueError(f"{context}: {key} must be an integer")
                timing[key] = int(value)
            if timing["measured_calls"]:
                for key in ("algorithm_ms", "communication_ms"):
                    timing[key] = number(raw, key, context)
        rounds.append(number(result, "train_round", context))
        timings.append(timing)
    run = Run(path, label, str(data.get("status", "unknown")), rounds, timings, skip_rounds,
              data.get("metadata", {}).get("strategy", "baseline"))
    if any(entry["unmeasured_calls"] is None or entry["unmeasured_calls"] > 0 for entry in timings):
        warnings.warn(f"{path}: {run.coverage()}; communication plots include measured calls only")
    return run


def style_axis(axis, title: str, unit: str, xlabel: str = "Training round (exported index)"):
    axis.set_title(title)
    axis.set_xlabel(xlabel)
    axis.set_ylabel(unit)
    axis.grid(axis="y", alpha=0.25)
    axis.set_axisbelow(True)
    axis.set_ylim(bottom=0)


def stacked_bars(axis, x, series, labels):
    bottom = [0.0] * len(x)
    for values, label, color in zip(series, labels, COLORS):
        axis.bar(x, values, bottom=bottom, label=label, color=color)
        bottom = [base + value for base, value in zip(bottom, values)]
    axis.legend(fontsize=8)


def save_figure(fig, output: Path):
    fig.savefig(output, dpi=160)
    plt.close(fig)
    print(f"Saved {output}")


def plot_run(run: Run, output: Path):
    fig, axes = plt.subplots(2, 2, figsize=(14, 9), layout="constrained")
    fig.suptitle(
        f"{run.label} | status={run.status} | {len(run.rounds)} rounds"
        + (f" | first {run.skipped} omitted" if run.skipped else ""),
        fontsize=14,
    )
    wall, calls, sync, traffic = axes.flat
    stacked_bars(
        wall, run.rounds,
        [run.values(key, 1000) for key in ("training_ms", "policy_check_ms", "coordination_overhead_ms")],
        ["Training phases", "Policy recheck", "Gateway coordination"],
    )
    style_axis(wall, "Round wall-clock time", "Seconds")
    stacked_bars(
        calls, run.rounds,
        [run.values("algorithm_ms"), run.values("communication_ms")],
        ["Algorithm", "Communication + middleware"],
    )
    style_axis(calls, f"Measured call time | {run.coverage()}", "Summed participant-ms")
    if not any(entry["measured_calls"] for entry in run.timing):
        calls.text(0.5, 0.5, "Sidecar measurements unavailable", ha="center", transform=calls.transAxes)
    sync.plot(run.rounds, run.values("barrier_wait_ms"), color=COLORS[0])
    style_axis(sync, "Synchronization: early finishers waiting for slowest", "Summed participant-ms")
    traffic.plot(run.rounds, run.values("bytes_sent", MIB), label="Sent", color=COLORS[0])
    traffic.plot(run.rounds, run.values("bytes_received", MIB), label="Received", color=COLORS[1])
    traffic.legend(fontsize=8)
    style_axis(traffic, "Gateway payload volume (not wire-level traffic)", "MiB / round")
    if run.strategy == "fed-bcd":
        for axis in axes.flat:
            axis.set_xlabel("FedBCD synchronization round (exported index)")
    fig.supxlabel(
        ("FedBCD synchronization rounds only: " if run.strategy == "fed-bcd" else "Training rounds only: ")
        + "excludes approval, warm-up and shutdown. "
        "Participant time is not wall time; communication includes middleware.",
        fontsize=9,
    )
    save_figure(fig, output)


def plot_comparison(runs: list, output: Path):
    fig, axes = plt.subplots(2, 2, figsize=(14, 9), layout="constrained")
    fig.suptitle("Deployment comparison | normalized training metrics", fontsize=14)
    wall, calls, sync, traffic = axes.flat
    x = list(range(len(runs)))
    stacked_bars(
        wall, x,
        [[run.mean(key, 1000) for run in runs] for key in ("training_ms", "policy_check_ms", "coordination_overhead_ms")],
        ["Training phases", "Policy recheck", "Gateway coordination"],
    )
    style_axis(wall, "Mean round wall-clock time", "Seconds / round", "")
    stacked_bars(
        calls, x,
        [[run.per_measured_call(key) for run in runs] for key in ("algorithm_ms", "communication_ms")],
        ["Algorithm", "Communication + middleware"],
    )
    style_axis(calls, "Mean measured call time (not round wall time)", "ms / measured call", "")
    for index, run in enumerate(runs):
        if math.isnan(run.per_measured_call("communication_ms")):
            calls.text(index, 0, "Unavailable", ha="center", va="bottom", fontsize=8)
    sync.bar(x, [run.mean("barrier_wait_ms") for run in runs], color=COLORS[0])
    style_axis(sync, "Mean synchronization wait", "Participant-ms / round", "")
    stacked_bars(
        traffic, x,
        [[run.mean(key, MIB) for run in runs] for key in ("bytes_sent", "bytes_received")],
        ["Sent", "Received"],
    )
    style_axis(traffic, "Mean gateway payload volume", "MiB / round", "")
    for axis in axes.flat:
        axis.set_xticks(x, [run.label for run in runs], rotation=15, ha="right")
    fig.supxlabel(
        "\n".join(
            f"{run.label}: {len(run.rounds)} rounds, {run.status}, {run.coverage()}"
            + (f", first {run.skipped} omitted" if run.skipped else "")
            for run in runs
        )
        + "\nTraining only. Compare like-for-like algorithms, participants, batch sizes and policy-check intervals.",
        fontsize=9,
    )
    save_figure(fig, output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("files", nargs="+", type=Path, help="Scenario result JSON files")
    parser.add_argument("--labels", nargs="+", help="One deployment label per input file")
    parser.add_argument("--skip-rounds", type=int, default=0, help="Omit the first N recorded rounds in every input (default: 0)")
    parser.add_argument("--output-dir", type=Path, default=Path("results/vfl-scenarios/plots"))
    args = parser.parse_args()
    if args.labels and len(args.labels) != len(args.files):
        parser.error("--labels must contain one label per input file")
    if args.skip_rounds < 0:
        parser.error("--skip-rounds must be non-negative")
    labels = args.labels or [path.stem for path in args.files]
    try:
        runs = [load_run(path, label, args.skip_rounds) for path, label in zip(args.files, labels)]
    except (OSError, ValueError) as exc:
        parser.error(str(exc))
    args.output_dir.mkdir(parents=True, exist_ok=True)
    for index, run in enumerate(runs, 1):
        prefix = f"{index:02d}-" if len(runs) > 1 else ""
        plot_run(run, args.output_dir / f"{prefix}{run.path.stem}-timing.png")
        print(
            f"{run.label}: {run.coverage()}, mean round={run.mean('round_ms'):.1f} ms, "
            f"mean policy recheck={run.mean('policy_check_ms'):.1f} ms"
        )
    if len(runs) > 1:
        plot_comparison(runs, args.output_dir / "comparison.png")


if __name__ == "__main__":
    main()

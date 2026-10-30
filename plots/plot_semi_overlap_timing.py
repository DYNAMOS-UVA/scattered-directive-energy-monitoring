"""Plot saved semi-overlap gateway metrics without assuming sequential stages."""

import argparse
import json
import math
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.patches import Patch


STAGES = ("server_sample", "client_forward", "server_aggregate", "client_update_dispatch")
COLORS = ("#4477AA", "#EE7733", "#228833", "#CC6677")
NOTICE = "Reply latency, not pure compute. Background training completion is unverified."


def number(data, key):
    value = data.get(key)
    if isinstance(value, bool) or not isinstance(value, (float, int)) or not math.isfinite(value) or value < 0:
        raise ValueError(f"{key} must be finite and nonnegative")
    return value


def load_result(path):
    result = json.loads(path.read_text(encoding="utf-8"))
    if result["metadata"]["strategy"] != "semi-overlap-fed-bcd":
        raise ValueError("Use a semi-overlap-fed-bcd timing export")
    timing = result["metadata"]["timing"]
    if not timing.get("phases") or not result.get("results"):
        raise ValueError("No pipeline stages/cycle results to plot")
    for entry in result["results"]:
        number(entry, "train_round")
        number(entry, "accuracy")
        number(entry["timing"], "cycle_latency_ms")
    for key, phase in timing["phases"].items():
        stage = key.split("/", 1)[-1]
        if stage not in STAGES:
            raise ValueError(f"Unknown pipeline stage {key}")
        start = number(phase, "start_offset_ms")
        end = number(phase, "end_offset_ms")
        if end < start or end > number(timing, "duration_ms") + 0.02:
            raise ValueError(f"Invalid stage interval: {key}")
    tolerance = max(0.02, len(timing["phases"]) * 0.003)
    if abs(number(timing, "stage_union_ms") + number(timing, "outside_stage_ms") - timing["duration_ms"]) > tolerance:
        raise ValueError("Stage coverage does not match pipeline wall time")
    if abs(number(timing, "stage_elapsed_sum_ms") - number(timing, "stage_union_ms") - number(timing, "stage_overlap_ms")) > tolerance:
        raise ValueError("Invalid overlap accounting")
    if result["status"] != "done":
        print(f"WARNING: plotting {result['status']!r} result; incomplete cycles may be absent.")
    return result


def save(figure, path):
    figure.text(0.5, 0.01, NOTICE, ha="center", fontsize=8)
    figure.tight_layout(rect=(0, 0.04, 1, 0.98))
    figure.savefig(path, dpi=160)
    plt.close(figure)
    print(f"Saved {path}")


def plot_result(result, directory):
    directory.mkdir(parents=True, exist_ok=True)
    timing = result["metadata"]["timing"]
    results = result["results"]
    phases = timing["phases"]
    cycles = sorted({phase["cycle"] for phase in phases.values()})
    row = {cycle: index for index, cycle in enumerate(cycles)}
    figure, axis = plt.subplots(figsize=(12, max(4, min(18, len(cycles) * 0.45))))
    figure.suptitle(f"Semi-overlap FedBCD - status: {result['status']}")
    for key, phase in phases.items():
        stage = key.split("/", 1)[-1]
        left = number(phase, "start_offset_ms") / 1000
        width = (number(phase, "end_offset_ms") - number(phase, "start_offset_ms")) / 1000
        axis.broken_barh([(left, width)], (row[phase["cycle"]] - 0.35, 0.7),
                        facecolors=COLORS[STAGES.index(stage)])
    axis.set(yticks=list(row.values()), yticklabels=[str(cycle) for cycle in cycles],
             xlabel="Seconds since pipeline start (gateway clock)", ylabel="Synchronization cycle",
             title="Semi-overlap pipeline: actual stage intervals")
    axis.invert_yaxis()
    axis.legend(handles=[Patch(color=color, label=stage) for stage, color in zip(STAGES, COLORS)],
                loc="upper center", bbox_to_anchor=(0.5, 1.18), ncol=2, fontsize=8)
    axis.grid(axis="x", alpha=0.2)
    save(figure, directory / "pipeline-timeline.png")

    figure, axes = plt.subplots(2, 2, figsize=(13, 9))
    figure.suptitle(f"Semi-overlap FedBCD - status: {result['status']}")
    wall = number(timing, "duration_ms") / 1000
    union = number(timing, "stage_union_ms") / 1000
    outside = number(timing, "outside_stage_ms") / 1000
    axes[0, 0].barh(["Pipeline wall"], [union], label="Covered by >= 1 stage")
    axes[0, 0].barh(["Pipeline wall"], [outside], left=[union], label="Outside stages")
    axes[0, 0].set(title=f"Wall coverage ({wall:.3f}s); no overlapping sums", xlabel="Seconds")
    axes[0, 0].legend(fontsize=8)
    lifecycle = [("Approval", number(timing, "approval_ms")),
                 ("Warmup", number(timing["warmup"], "duration_ms")),
                 ("Pipeline", number(timing, "duration_ms")),
                 ("Final accuracy", number(timing["final_accuracy"], "duration_ms")),
                 ("Shutdown", number(timing["shutdown"], "duration_ms"))]
    axes[0, 1].bar([name for name, _ in lifecycle], [value / 1000 for _, value in lifecycle])
    axes[0, 1].tick_params(axis="x", labelrotation=15)
    axes[0, 1].set(title=f"Lifecycle components (total {number(timing, 'total_ms') / 1000:.3f}s)", ylabel="Seconds")
    axes[1, 0].plot([entry["train_round"] for entry in results],
                    [number(entry["timing"], "cycle_latency_ms") for entry in results], marker="o")
    axes[1, 0].set(title="Cycle reply latency (cycles overlap; do not sum)", xlabel="Cycle", ylabel="Milliseconds")
    axes[1, 1].plot([entry["train_round"] for entry in results],
                    [number(entry, "accuracy") for entry in results], marker="o")
    axes[1, 1].set(title="Final accuracy snapshots", xlabel="Cycle", ylabel="Accuracy (%)", ylim=(0, 100))
    for axis in axes.flat:
        axis.grid(axis="y", alpha=0.2)
    save(figure, directory / "timing-summary.png")

    figure, axes = plt.subplots(2, 2, figsize=(13, 9))
    figure.suptitle(f"Semi-overlap FedBCD - status: {result['status']}")
    names = list(timing["participants"])
    participants = timing["participants"]
    active = [number(participants[name], "request_active_ms") / 1000 for name in names]
    idle = [number(participants[name], "request_idle_ms") / 1000 for name in names]
    axes[0, 0].bar(names, active, label="Outstanding requests (union)")
    axes[0, 0].bar(names, idle, bottom=active, label="No outstanding request")
    axes[0, 0].set(title="Request activity, not compute activity", ylabel="Seconds")
    axes[0, 0].legend(fontsize=8)
    handler = [number(participants[name], "algorithm_ms") / 1000 if participants[name].get("measured_calls") else math.nan for name in names]
    communication = [number(participants[name], "communication_ms") / 1000 if participants[name].get("measured_calls") else math.nan for name in names]
    for index, name in enumerate(names):
        if not participants[name].get("measured_calls"):
            axes[0, 1].text(index, 0, "unmeasured", ha="center", va="bottom", fontsize=8)
    axes[0, 1].bar(names, handler, label="Handler-to-reply")
    axes[0, 1].bar(names, communication, bottom=handler, label="Transport/queue/middleware estimate")
    measured = int(number(timing, "measured_calls"))
    total = measured + int(number(timing, "unmeasured_calls"))
    axes[0, 1].set(title=f"Measured-call sums, not wall time ({measured}/{total} calls)", ylabel="Summed seconds")
    axes[0, 1].legend(fontsize=8)
    axes[1, 0].bar(STAGES, [sum(number(phase, "duration_ms") for key, phase in phases.items()
                                         if key.split("/", 1)[-1] == stage) / 1000 for stage in STAGES], color=COLORS)
    axes[1, 0].tick_params(axis="x", labelrotation=15)
    axes[1, 0].set(title=f"Stage sums (extra overlap {number(timing, 'stage_overlap_ms') / 1000:.3f}s)", ylabel="Summed seconds")
    axes[1, 1].bar(["Sent", "Received"], [number(timing, key) / 1048576 for key in ("bytes_sent", "bytes_received")])
    axes[1, 1].set(title="Pipeline JSON payloads (not network-wire bytes)", ylabel="MiB")
    for axis in axes.flat:
        axis.grid(axis="y", alpha=0.2)
    save(figure, directory / "request-metrics.png")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("result", type=Path)
    parser.add_argument("--output-dir", type=Path)
    args = parser.parse_args()
    plot_result(load_result(args.result), args.output_dir or args.result.parent / "plots")


if __name__ == "__main__":
    main()

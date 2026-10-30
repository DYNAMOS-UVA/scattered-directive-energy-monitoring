import copy
import csv
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("experiment", ROOT / "scripts" / "run_semi_overlap_experiment.py")
experiment = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(experiment)


def phase(cycle, stage, left):
    names = {"server"} if stage.startswith("server_") or stage == "final_accuracy" else experiment.CLIENTS
    if stage in ("warmup", "shutdown"):
        names = experiment.CLIENTS | {"server"}
    calls = {
        name: {"duration_ms": 10, "bytes_sent": 100, "bytes_received": 200,
               "measured_calls": 1, "unmeasured_calls": 0,
               "algorithm_ms": 4, "communication_ms": 6, "measured_round_trip_ms": 10}
        for name in sorted(names)
    }
    count = len(calls)
    return {
        "cycle": cycle, "duration_ms": 10, "start_offset_ms": left, "end_offset_ms": left + 10,
        "barrier_wait_ms": 0, "spread_ms": 0, "calls": calls,
        "bytes_sent": 100 * count, "bytes_received": 200 * count,
        "measured_calls": count, "unmeasured_calls": 0,
        "algorithm_ms": 4 * count, "communication_ms": 6 * count, "measured_round_trip_ms": 10 * count,
    }


def summary(phases, duration):
    intervals = [(value["start_offset_ms"], value["end_offset_ms"]) for value in phases.values()]
    union = experiment.interval_union(intervals)
    elapsed_sum = sum(value["duration_ms"] for value in phases.values())
    participants = {}
    for name in sorted(experiment.CLIENTS | {"server"}):
        requests = [(value["start_offset_ms"], value["end_offset_ms"])
                    for value in phases.values() if name in value["calls"]]
        active = experiment.interval_union(requests)
        count = len(requests)
        participants[name] = {
            "request_active_ms": active, "request_idle_ms": duration - active,
            "request_elapsed_sum_ms": count * 10, "measured_calls": count, "unmeasured_calls": 0,
            "algorithm_ms": 4 * count, "communication_ms": 6 * count, "measured_round_trip_ms": 10 * count,
        }
    result = {
        "clock": "api-gateway", "duration_ms": duration,
        "stage_elapsed_sum_ms": elapsed_sum, "stage_union_ms": union,
        "stage_overlap_ms": elapsed_sum - union, "outside_stage_ms": duration - union,
        "reply_barrier_wait_ms": 0, "policy_check_measured": False,
        "timing_semantics": "handler_reply_latency; background compute may continue after reply",
        "phases": phases, "phase_order": list(phases), "participants": participants,
    }
    for field in ("bytes_sent", "bytes_received", "measured_calls", "unmeasured_calls",
                  "algorithm_ms", "communication_ms", "measured_round_trip_ms"):
        result[field] = sum(value[field] for value in phases.values())
    return result


def result_fixture(rounds=12):
    run_phases = {}
    results = []
    for cycle in range(rounds):
        local = {f"{cycle}/{stage}": phase(cycle, stage, index * 10)
                 for index, stage in enumerate(experiment.STAGES)}
        timing = summary(local, 40)
        timing["cycle_latency_ms"] = 40
        results.append({"timestamp": "2026-10-30T12:00:00Z", "train_round": cycle,
                        "clients": 3, "accuracy": 50 + cycle, "timing": timing})
        for key, value in local.items():
            shifted = copy.deepcopy(value)
            for field in ("start_offset_ms", "end_offset_ms"):
                shifted[field] += 15 * cycle
            run_phases[key] = shifted
    wall = (rounds - 1) * 15 + 40
    timing = summary(run_phases, wall)
    timing.update(
        enqueue_ms=max(0, wall - 25), drain_ms=25, fill_ms=40, approval_ms=5,
        warmup=phase(0, "warmup", 0), shutdown=phase(0, "shutdown", 0),
        final_accuracy=phase(0, "final_accuracy", 0), total_ms=wall + 40,
    )
    return {
        "request_id": "test-run", "status": "done",
        "metadata": {
            "strategy": experiment.STRATEGY, "total_rounds": rounds * 10,
            "communication_frequency": 10, "sample_batch_size": 64,
            "synchronization_rounds": rounds, "scheduled_local_steps": rounds * 10,
            "acknowledged_cycles": rounds, "timing": timing,
        },
        "results": results,
    }


class ExperimentTests(unittest.TestCase):
    def test_requestor_agreements_match_experiment_identity(self):
        agreements = json.loads((ROOT / "configuration" / "etcd_launch_files" / "agreements.json").read_text())
        self.assertEqual({entry["name"] for entry in agreements}, experiment.CLIENTS | {"server"})
        for entry in agreements:
            self.assertEqual(set(entry["relations"]), {"requestor"})
            self.assertEqual(entry["relations"]["requestor"]["ID"], "GUID")
        trigger = (ROOT / "scripts" / "single_request.sh").read_text()
        self.assertIn(r'\"userName\": \"requestor\"', trigger)

    def test_exact_default_schedule_and_exports(self):
        result = result_fixture()
        self.assertEqual(experiment.validate_result(result), [])
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.assertEqual(experiment.export_metrics(result, directory), [])
            with (directory / "calls.csv").open(newline="", encoding="utf-8") as source:
                calls = list(csv.DictReader(source))
            self.assertEqual(len([call for call in calls if call["scope"] == "pipeline"]), 96)
            self.assertEqual(len(calls), 105)
            self.assertEqual(len((directory / "rounds.csv").read_text().splitlines()), 13)
            self.assertIn("start_offset_ms", (directory / "phases.csv").read_text())
            self.assertIn("request_active_ms", (directory / "participants.csv").read_text())
            validation = json.loads((directory / "validation.json").read_text())
            self.assertTrue(validation["valid"])
            self.assertFalse(validation["background_completion_verified"])
            self.assertTrue(validation["warnings"])

    def test_rejects_bad_results_without_count_proxy(self):
        mutations = [
            lambda r: r.update(status="failed"),
            lambda r: r["metadata"].update(strategy="fed-bcd"),
            lambda r: r["results"].pop(),
            lambda r: r["results"][1].update(train_round=0),
            lambda r: r["results"][0].update(clients=2),
            lambda r: r["results"][0].pop("accuracy"),
            lambda r: r["results"][0].update(accuracy=float("nan")),
            lambda r: r["metadata"]["timing"].update(stage_union_ms=999),
            lambda r: r["metadata"]["timing"].update(outside_stage_ms=-1),
            lambda r: r["metadata"]["timing"].update(measured_calls=108),
            lambda r: r["metadata"]["timing"].update(measured_calls=96.001),
            lambda r: r["metadata"]["timing"]["participants"]["clientone"].update(algorithm_ms=1),
            lambda r: r["metadata"]["timing"].update(reply_barrier_wait_ms=123),
            lambda r: r["metadata"]["timing"]["phases"]["0/client_forward"]["calls"].pop("clientone"),
            lambda r: r["metadata"]["timing"]["phases"]["0/client_forward"]["calls"]["clientone"].update(unmeasured_calls=1),
            lambda r: r["metadata"]["timing"]["phases"]["0/client_forward"]["calls"]["clientone"].update(algorithm_ms=-1),
            lambda r: r["metadata"]["timing"]["phases"]["0/client_forward"].update(cycle=1),
            lambda r: r["metadata"]["timing"]["shutdown"]["calls"]["server"].update(failures=1),
            lambda r: r["metadata"]["timing"]["final_accuracy"]["calls"]["server"].update(unmeasured_calls=1),
            lambda r: r["metadata"]["timing"]["warmup"]["calls"]["server"].update(failures=1),
            lambda r: r["metadata"]["timing"].update(total_ms=1),
            lambda r: r["metadata"].update(pipeline_errors=["cycle failed"]),
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                result = result_fixture()
                mutation(result)
                self.assertTrue(experiment.validate_result(result))
        self.assertTrue(experiment.validate_result(result_fixture(), {"sample_batch_size": 128}))

    def test_pending_snapshot_is_preserved_and_flagged(self):
        result = {"request_id": "pending", "status": "pending", "metadata": {"created_at": "now"}, "results": []}
        with tempfile.TemporaryDirectory() as temporary:
            issues = experiment.export_metrics(result, Path(temporary))
            self.assertTrue(issues)
            self.assertFalse(json.loads((Path(temporary) / "validation.json").read_text())["valid"])

    def test_recovered_warmup_timeout_is_explicit_warning_not_training_failure(self):
        result = result_fixture()
        warmup = result["metadata"]["timing"]["warmup"]
        warmup["duration_ms"] = 60010
        warmup["unmeasured_calls"] = 1
        warmup["bytes_sent"] += 100
        warmup["calls"]["server"].update(
            attempts=2, failures=1, unmeasured_calls=1, duration_ms=60010,
            bytes_sent=200,
        )
        result["metadata"]["timing"]["total_ms"] += 60000
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.assertEqual(experiment.export_metrics(result, directory), [])
            validation = json.loads((directory / "validation.json").read_text())
            self.assertTrue(validation["valid"])
            self.assertTrue(any("Warmup for server" in warning for warning in validation["warnings"]))
            self.assertIn("failures", (directory / "calls.csv").read_text())
        warmup["calls"]["server"]["measured_calls"] = 0
        self.assertTrue(experiment.validate_result(result))

    def test_failed_phase_is_exported_without_duplicate_cycle_calls(self):
        result = result_fixture()
        result["status"] = "failed"
        result["results"].pop()
        with tempfile.TemporaryDirectory() as temporary:
            experiment.export_metrics(result, Path(temporary))
            with (Path(temporary) / "calls.csv").open(newline="", encoding="utf-8") as source:
                calls = list(csv.DictReader(source))
            self.assertEqual(len(calls), 105)
            self.assertTrue(any(call["train_round"] == "11" for call in calls))

    def test_invalid_schedule_never_submits(self):
        for flags in (["--cycles", "121"], ["--communication-frequency", "1"],
                      ["--cycles", "5"], ["--runs", "0"], ["--sample-batch-size", "0"]):
            with self.subTest(flags=flags), patch("sys.argv", ["experiment", *flags]), \
                    patch.object(experiment, "run_request") as request:
                with self.assertRaises(SystemExit):
                    experiment.main()
                request.assert_not_called()

    def test_export_only_is_repeatable_and_never_submits(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "result.json"
            experiment.write_json(path, result_fixture())
            original = path.read_bytes()
            for _ in range(2):
                with patch("sys.argv", ["experiment", "--export-result", str(path)]), \
                        patch.object(experiment, "run_request") as request:
                    experiment.main()
                    request.assert_not_called()
            self.assertEqual(path.read_bytes(), original)
            self.assertTrue((path.parent / "metrics" / "validation.json").is_file())

    def test_runner_full_export_and_real_plot(self):
        result = result_fixture()

        def fake_request(directory, cycles, frequency, batch):
            self.assertEqual((cycles, frequency, batch), (120, 10, 64))
            experiment.write_json(directory / "result.json", result)
            return 0

        with tempfile.TemporaryDirectory() as temporary:
            with patch("sys.argv", ["experiment", "--output-dir", temporary]), \
                    patch.object(experiment, "run_request", side_effect=fake_request):
                experiment.main()
            folder = next(Path(temporary).iterdir())
            manifest = json.loads((folder / "experiment.json").read_text())
            self.assertEqual(manifest["status"], "done")
            self.assertEqual(manifest["synchronization_rounds"], 12)
            saved = folder / "run-001"
            self.assertEqual(json.loads((saved / "result.json").read_text()), result)
            for name in ("pipeline-timeline.png", "timing-summary.png", "request-metrics.png"):
                self.assertGreater((saved / "plots" / name).stat().st_size, 1000)

    def test_runner_retains_failure_and_does_not_plot(self):
        for code, result in ((1, None), (1, result_fixture()), (0, {"status": "pending", "metadata": {}, "results": []})):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary:
                def fake_request(directory, *args):
                    if result is not None:
                        experiment.write_json(directory / "result.json", result)
                    return code
                with patch("sys.argv", ["experiment", "--output-dir", temporary]), \
                        patch.object(experiment, "run_request", side_effect=fake_request), \
                        patch.object(experiment, "generate_plots") as plot:
                    with self.assertRaises(RuntimeError):
                        experiment.main()
                    plot.assert_not_called()
                manifest = json.loads((next(Path(temporary).iterdir()) / "experiment.json").read_text())
                self.assertEqual(manifest["status"], "failed")

    def test_subprocess_options_and_log_capture(self):
        with tempfile.TemporaryDirectory() as temporary, patch.dict("os.environ", {}, clear=True), \
                patch.object(experiment.subprocess, "run") as run:
            run.return_value.returncode = 7
            directory = Path(temporary)
            self.assertEqual(experiment.run_request(directory, 120, 10, 64), 7)
            options = run.call_args.kwargs
            self.assertEqual(options["env"]["MAX_POLLS"], "2160")
            self.assertEqual(options["env"]["CYCLES"], "120")
            self.assertEqual(options["env"]["OUTPUT_FILE"], str(directory / "result.json"))
            self.assertEqual(options["stderr"], subprocess.STDOUT)
            self.assertTrue((directory / "request.log").is_file())

    def test_plot_only_invalid_inputs_fail_explicitly(self):
        for mutation in (
            lambda r: r["metadata"].update(strategy="fed-bcd"),
            lambda r: r.update(results=[]),
            lambda r: r["metadata"]["timing"]["phases"]["0/server_sample"].update(end_offset_ms=-1),
        ):
            with tempfile.TemporaryDirectory() as temporary:
                result = result_fixture(1)
                mutation(result)
                path = Path(temporary) / "result.json"
                experiment.write_json(path, result)
                process = subprocess.run(
                    [sys.executable, str(ROOT / "plots" / "plot_semi_overlap_timing.py"), str(path)],
                    capture_output=True, text=True, check=False,
                )
                self.assertNotEqual(process.returncode, 0)
                self.assertIn("ValueError", process.stderr)


if __name__ == "__main__":
    unittest.main()

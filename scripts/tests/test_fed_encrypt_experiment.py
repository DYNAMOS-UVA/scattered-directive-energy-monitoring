import importlib.util
import json
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("encrypted_experiment", ROOT / "scripts" / "run_fed_encrypt_experiment.py")
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


def summary(schedule):
    phases, participants = {}, {}
    offset = 0
    for stage, names in schedule.items():
        calls = [
            {"stage": stage, "participant": name, "start_ms": offset, "end_ms": offset + 1,
             "duration_ms": 1, "bytes_sent": 10, "bytes_received": 20,
             "failed": False, "algorithm_ms": 0.25}
            for name in sorted(names)
        ]
        phase = counters(calls)
        phase.update(start_ms=offset, end_ms=offset + 1, duration_ms=1, barrier_wait_ms=0, calls=calls)
        phases[stage] = phase
        offset += 1
    all_calls = [call for phase in phases.values() for call in phase["calls"]]
    for name in {call["participant"] for call in all_calls}:
        group = [call for call in all_calls if call["participant"] == name]
        participants[name] = {**counters(group), "request_active_ms": len(group),
                              "no_outstanding_request_ms": offset - len(group)}
    return {
        **counters(all_calls), "duration_ms": offset, "phase_order": list(phases), "phases": phases,
        "stage_elapsed_sum_ms": offset, "stage_union_ms": offset, "stage_overlap_ms": 0,
        "outside_stage_ms": 0, "reply_barrier_wait_ms": 0, "participants": participants,
        "policy_check_measured": False, "background_completion_verified": False,
    }


def counters(calls):
    measured = [call for call in calls if not call["failed"] and "algorithm_ms" in call]
    result = {
        "attempts": len(calls), "failures": sum(call["failed"] for call in calls),
        "measured_calls": len(measured), "unmeasured_calls": len(calls) - len(measured),
        "bytes_sent": sum(call["bytes_sent"] for call in calls),
        "bytes_received": sum(call["bytes_received"] for call in calls),
        "request_elapsed_sum_ms": sum(call["duration_ms"] for call in calls),
    }
    if measured:
        result.update(algorithm_ms=sum(call["algorithm_ms"] for call in measured),
                      measured_round_trip_ms=sum(call["duration_ms"] for call in measured),
                      communication_ms=sum(call["duration_ms"] - call["algorithm_ms"] for call in measured))
    return result


def fixture(rounds=2, reinitialize=False):
    warmup = summary({"warmup": runner.PROVIDERS})
    initialization = summary(runner.INITIALIZATION)
    shutdown = summary({"shutdown": runner.PROVIDERS})
    results, measured = [], []
    schedule = {"warmup/warmup": runner.PROVIDERS}
    schedule.update({"initialization/" + key: value for key, value in runner.INITIALIZATION.items()})
    for cycle in range(rounds):
        timing = summary(runner.STAGES)
        timing.update(train_round=cycle, round_ms=7, failed=False)
        if reinitialize and cycle == 1:
            timing["reinitialization"] = summary(runner.INITIALIZATION)
            timing["round_ms"] += 3
            schedule.update({f"round/{cycle}/reinitialization/{key}": value
                             for key, value in runner.INITIALIZATION.items()})
        measured.append(timing)
        results.append({
            "train_round": cycle, "accuracy": 0.75, "clients": 4,
            "participants": [{"Auth": name, "Url": "unused", "Active": True} for name in sorted(runner.PARTIES)],
            "timing": timing,
        })
        schedule.update({f"round/{cycle}/{key}": value for key, value in runner.STAGES.items()})
    schedule["shutdown/shutdown"] = runner.PROVIDERS
    timing = summary(schedule)
    timing.update(warmup=warmup, initialization=initialization, shutdown=shutdown, rounds=measured,
                  training_ms=sum(item["round_ms"] for item in measured), approval_ms=1,
                  total_ms=6 + sum(item["round_ms"] for item in measured), policy_actions=[])
    return {
        "request_id": "test-request", "status": "done", "results": results,
        "metadata": {"strategy": runner.STRATEGY, "total_rounds": rounds, "sample_batch_size": 64,
                     "completed_rounds": rounds, "attempted_rounds": rounds, "policy_removal": -1,
                     "policy_reintroduction": -1, "errors": [], "timing": timing},
    }


class ExperimentTests(unittest.TestCase):
    def test_exact_shape_and_exports(self):
        result = fixture()
        self.assertTrue(runner.validate_result(result)["valid"])
        self.assertEqual(result["metadata"]["timing"]["attempts"], 38)
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary)
            self.assertTrue(runner.export_metrics(result, path)["valid"])
            self.assertEqual(len((path / "calls.csv").read_text().splitlines()), 39)
            self.assertEqual(len((path / "rounds.csv").read_text().splitlines()), 3)
            self.assertFalse(json.loads((path / "validation.json").read_text())["background_completion_verified"])
            self.assertTrue((path / "plots" / "accuracy.png").read_bytes().startswith(b"\x89PNG\r\n\x1a\n"))
            self.assertEqual(len(list((path / "plots").glob("*.png"))), 5)

    def test_missing_coverage_accuracy_rounds_and_participants_rejected(self):
        mutations = [
            lambda r: r.update(status="failed"),
            lambda r: r["results"].pop(),
            lambda r: r["results"][0].update(accuracy=75),
            lambda r: r["results"][0].update(clients=3),
            lambda r: r["metadata"]["timing"].update(stage_union_ms=999),
            lambda r: r["metadata"]["timing"].update(measured_calls=0),
            lambda r: r["metadata"]["timing"].update(policy_check_measured=True),
            lambda r: r["metadata"]["timing"]["rounds"][0]["phases"]["vflSampleBatchRequest"]["calls"][0].pop("algorithm_ms"),
            lambda r: r["metadata"]["timing"]["rounds"][0]["phases"]["vflSampleBatchRequest"]["calls"][0].update(failed=True),
            lambda r: r["results"][0]["participants"].pop(),
        ]
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                result = fixture()
                mutate(result)
                self.assertFalse(runner.validate_result(result)["valid"])

    def test_recovered_warmup_warning(self):
        result = fixture()
        # A failed attempt occupies the same stage envelope before the good reply.
        for metrics, key in [(result["metadata"]["timing"]["warmup"], "warmup"),
                             (result["metadata"]["timing"], "warmup/warmup")]:
            phase = metrics["phases"][key]
            failed = {**phase["calls"][0], "failed": True}
            failed.pop("algorithm_ms")
            phase["calls"].insert(0, failed)
            phase.update(counters(phase["calls"]))
            calls = [call for entry in metrics["phases"].values() for call in entry["calls"]]
            metrics.update(counters(calls))
            name = failed["participant"]
            metrics["participants"][name].update(counters([call for call in calls if call["participant"] == name]))
        validation = runner.validate_result(result)
        self.assertTrue(validation["valid"], validation)
        self.assertEqual(len(validation["warnings"]), 2)

    def test_nested_interval_union(self):
        self.assertEqual(runner.union([(0, 10), (2, 4), (9, 12), (20, 21)]), 13)

    def test_reinitialization_is_counted_once(self):
        result = fixture(reinitialize=True)
        self.assertEqual(result["metadata"]["timing"]["attempts"], 44)
        self.assertTrue(runner.validate_result(result)["valid"])

    def test_arguments_and_request_identity(self):
        for options in [["--cycles", "0"], ["--runs", "0"], ["--timeout", "nan"],
                        ["--policy-reintroduction", "2"], ["--cycles", "2", "--policy-removal", "2"],
                        ["--runs", "2", "--policy-removal", "1"]]:
            with self.subTest(options=options), self.assertRaises(SystemExit):
                runner.parse_args(options)
        args = runner.parse_args([])
        self.assertEqual(args.cycles, 12)
        body = runner.payload(args)
        self.assertEqual(body["user"], {"id": "GUID", "userName": "requestor"})
        self.assertEqual(set(body["dataProviders"]), runner.PROVIDERS)
        self.assertNotIn("communication_frequency", body["data_request"]["data"])
        self.assertNotIn("learning_rate", body["data_request"]["data"])

    def test_export_only_cli_never_contacts_cluster(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "result.json"
            path.write_text(json.dumps(fixture()))
            command = subprocess.run([sys.executable, str(ROOT / "scripts" / "run_fed_encrypt_experiment.py"),
                                      "--export-result", str(path)], capture_output=True, text=True)
            self.assertEqual(command.returncode, 0, command.stderr)
            self.assertTrue((path.parent / "metrics" / "validation.json").exists())
            self.assertTrue((path.parent / "metrics" / "plots" / "lifecycle-duration.png").exists())

    def test_plot_only_cli_uses_saved_result(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "result.json"
            path.write_text(json.dumps(fixture()))
            command = subprocess.run([sys.executable, str(ROOT / "scripts" / "run_fed_encrypt_experiment.py"),
                                      "--plot-result", str(path)], capture_output=True, text=True)
            self.assertEqual(command.returncode, 0, command.stderr)
            self.assertTrue((path.parent / "metrics" / "plots" / "active-participants.png").exists())

    def test_live_http_orchestration_and_evidence(self):
        result = fixture(rounds=1)
        received, polls = [], []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                received.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
                self.send_response(202)
                self.end_headers()
                self.wfile.write(b'{"request_id":"test-request","status":"pending"}')

            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                if "getAvailableProviders" in self.path:
                    self.wfile.write(b'{}')
                else:
                    polls.append(self.path)
                    self.wfile.write(json.dumps(result).encode())

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as temporary:
                runner.main(["--cycles", "1", "--output-dir", temporary, "--no-port-forward",
                             "--api-base-url", f"http://127.0.0.1:{server.server_port}/api/v1"])
                folder = next(Path(temporary).iterdir())
                self.assertEqual(json.loads((folder / "experiment.json").read_text())["status"], "done")
                self.assertTrue((folder / "run-001" / "submission.json").exists())
                self.assertTrue((folder / "run-001" / "metrics" / "calls.csv").exists())
                self.assertEqual(len(received), 1)
                self.assertEqual(len(polls), 1)
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_timeout_preserves_snapshot_and_never_resubmits(self):
        pending = {"request_id": "id", "status": "pending", "results": [], "metadata": {}}
        args = runner.parse_args(["--timeout", "0.01", "--poll-interval", "0.001"])
        with tempfile.TemporaryDirectory() as temporary, patch.object(
            runner, "http_json", side_effect=[{"request_id": "id", "status": "pending"}] + [pending] * 100
        ) as http:
            with self.assertRaises(TimeoutError):
                runner.run_request(args, Path(temporary))
            self.assertEqual(json.loads((Path(temporary) / "result.json").read_text())["status"], "pending")
            self.assertEqual(sum(call.args[1] == "/requestApproval" for call in http.call_args_list), 1)

    def test_custom_unavailable_gateway_does_not_start_kubectl(self):
        args = runner.parse_args(["--api-base-url", "http://example.invalid/api/v1"])
        with tempfile.TemporaryDirectory() as temporary, patch.object(
            runner, "http_json", side_effect=OSError("unavailable")
        ), patch.object(runner.subprocess, "Popen") as process:
            with self.assertRaises(RuntimeError):
                with runner.gateway(args, Path(temporary)):
                    self.fail("gateway should be unavailable")
            process.assert_not_called()

    def test_owned_port_forward_is_readied_and_cleaned(self):
        args = runner.parse_args([])
        with tempfile.TemporaryDirectory() as temporary, patch.object(
            runner, "http_json", side_effect=[OSError("not connected"), {}]
        ), patch.object(runner.subprocess, "Popen") as process:
            process.return_value.poll.return_value = None
            with runner.gateway(args, Path(temporary)):
                process.return_value.terminate.assert_not_called()
            process.return_value.terminate.assert_called_once()
            process.return_value.wait.assert_called_once_with(timeout=10)

    def test_policy_actions_are_not_reasoning_measurements(self):
        result = fixture(2)
        result["metadata"].update(policy_removal=0, policy_reintroduction=1)
        result["metadata"]["timing"]["policy_actions"] = [
            {"train_round": 0, "action": "removal", "duration_ms": 1, "failed": False},
            {"train_round": 1, "action": "reintroduction", "duration_ms": 1, "failed": False},
        ]
        result["results"][1]["participants"][0]["Active"] = False
        result["results"][1]["clients"] = 3
        validation = runner.validate_result(result)
        self.assertTrue(validation["valid"], validation)
        self.assertIn("event-application", validation["warnings"][-1])

    def test_failed_run_stops_repetitions_and_marks_manifest(self):
        result = fixture(1)
        result["status"] = "failed"
        with tempfile.TemporaryDirectory() as temporary, patch.object(
            runner, "http_json", return_value={}
        ), patch.object(runner, "run_request", return_value=result) as run:
            with self.assertRaisesRegex(RuntimeError, "do not blindly resubmit"):
                runner.main(["--cycles", "1", "--runs", "2", "--output-dir", temporary])
            self.assertEqual(run.call_count, 1)
            folder = next(Path(temporary).iterdir())
            self.assertEqual(json.loads((folder / "experiment.json").read_text())["status"], "failed")
            self.assertFalse(json.loads((folder / "run-001" / "metrics" / "validation.json").read_text())["valid"])


if __name__ == "__main__":
    unittest.main()

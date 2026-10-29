import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "experiment", Path(__file__).resolve().parents[1] / "run_baseline_experiment.py"
)
experiment = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(experiment)


class ExperimentTests(unittest.TestCase):
    def result(self):
        call = {"algorithm_ms": 2, "communication_ms": 3, "measured_calls": 1}
        return {
            "request_id": "test", "status": "done",
            "metadata": {"timing": {
                "total_ms": 10, "measured_calls": 1, "unmeasured_calls": 0,
                "shutdown": {"calls": {"clientone": dict(call)}},
            }},
            "results": [{
                "train_round": 0, "accuracy": 50, "clients": 3,
                "timing": {
                    "round_ms": 5,
                    "measured_calls": 1, "unmeasured_calls": 0,
                    "phases": {"client_forward": {"calls": {"clientone": call}}},
                    "participants": {"clientone": {"active_ms": 5}},
                },
            }],
        }

    def test_exports_gateway_and_sidecar_metrics(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "metrics"
            self.assertEqual(experiment.export_metrics(self.result(), directory, 1), [])
            self.assertIn("algorithm_ms", (directory / "calls.csv").read_text())
            self.assertIn("clientone", (directory / "participants.csv").read_text())
            self.assertIn("accuracy", (directory / "rounds.csv").read_text())
            self.assertEqual(json.loads((directory / "run.json").read_text())["total_ms"], 10)

    def test_failed_or_incomplete_runs_are_explicit(self):
        result = self.result()
        result["status"] = "failed"
        result["metadata"]["timing"]["unmeasured_calls"] = 1
        result["metadata"]["timing"]["shutdown"]["calls"]["clientone"]["failures"] = 1
        result["results"][0]["clients"] = 2
        with tempfile.TemporaryDirectory() as temporary:
            issues = experiment.export_metrics(result, Path(temporary) / "metrics", 120)
            self.assertEqual(len(issues), 5)

    def test_runner_retains_full_result_and_marks_completion(self):
        result = self.result()

        def fake_request(directory, cycles):
            self.assertEqual(cycles, 1)
            experiment.write_json(directory / "result.json", result)
            return 0

        with tempfile.TemporaryDirectory() as temporary:
            with patch("sys.argv", [
                "experiment", "--cycles", "1", "--output-dir", temporary,
            ]), patch.object(experiment, "run_request", side_effect=fake_request), \
                    patch.object(experiment, "generate_plots") as plot:
                experiment.main()
            folder = next(Path(temporary).iterdir())
            plot.assert_called_once_with(folder / "run-001" / "result.json")
            self.assertEqual(
                json.loads((folder / "experiment.json").read_text())["status"], "done"
            )
            self.assertEqual(
                json.loads((folder / "run-001" / "result.json").read_text()), result
            )

    def test_runner_retains_failure_manifest(self):
        with tempfile.TemporaryDirectory() as temporary:
            with patch("sys.argv", [
                "experiment", "--output-dir", temporary,
            ]), patch.object(experiment, "run_request", return_value=1):
                with self.assertRaises(RuntimeError):
                    experiment.main()
            folder = next(Path(temporary).iterdir())
            self.assertEqual(
                json.loads((folder / "experiment.json").read_text())["status"], "failed"
            )

    def test_export_only_can_regenerate_existing_metrics_without_training(self):
        result = self.result()
        result["metadata"]["total_rounds"] = 1
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "result.json"
            experiment.write_json(path, result)
            for attempt in range(2):
                with patch("sys.argv", [
                    "experiment", "--export-result", str(path),
                ]), patch.object(experiment, "run_request") as request:
                    experiment.main()
                    request.assert_not_called()
            self.assertTrue((path.parent / "metrics" / "rounds.csv").is_file())
            self.assertEqual(json.loads(path.read_text()), result)

    def test_plot_command_uses_saved_result_and_sibling_folder(self):
        path = Path("experiment") / "run-001" / "result.json"
        with patch.object(experiment.subprocess, "run") as run:
            experiment.generate_plots(path)
        run.assert_called_once_with([
            experiment.sys.executable,
            str(experiment.ROOT / "plots" / "plot_vfl_timing.py"),
            str(path), "--labels", "baseline",
            "--output-dir", str(path.parent / "plots"),
        ], check=True)


if __name__ == "__main__":
    unittest.main()

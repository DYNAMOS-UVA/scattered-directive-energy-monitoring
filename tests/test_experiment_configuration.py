import contextlib
import datetime
import io
import json
import os
import signal
import subprocess
import sys
import tempfile
import time
import unittest
import zipfile
from pathlib import Path
from types import SimpleNamespace


ROOT = Path(__file__).resolve().parents[1]
LAUNCHER = ROOT / "node_scripts" / "start_experiment.py"
EXPERIMENT = "20261113T145519Z-test"
RUNNER = """
import pathlib, sys, time
output = pathlib.Path(sys.argv[1])
time.sleep(float(sys.argv[2]))
experiment = output / sys.argv[3]
experiment.mkdir()
(experiment / "experiment.log").write_text("experiment log", encoding="utf-8")
(experiment / "run-001").mkdir()
time.sleep(float(sys.argv[4]))
sys.exit(int(sys.argv[5]))
"""


class ExperimentConfigurationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name) / "baseline-20261113-145519"
        self.output.mkdir()
        self.configuration = {"settings": {"method": "baseline", "runs": 1}}
        self.staged = self.output / "run-configuration.json"
        self.staged.write_text(json.dumps(self.configuration), encoding="utf-8")

    def launch(self, code, *args, **kwargs):
        process = subprocess.Popen(
            [sys.executable, str(LAUNCHER), str(self.output), sys.executable, "-c", code, *map(str, args)],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            **kwargs,
        )
        self.addCleanup(self.stop_process, process)
        return process

    @staticmethod
    def stop_process(process):
        if process.poll() is None:
            process.terminate()
        process.communicate(timeout=5)

    def wait_for(self, condition, timeout=5):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if condition():
                return
            time.sleep(0.05)
        self.fail("Timed out waiting for the experiment output")

    def test_snapshot_is_beside_runs_while_runner_is_active_and_in_download(self):
        (self.output / "runner.log").write_text("runner log", encoding="utf-8")
        process = self.launch(RUNNER, self.output, 0.2, EXPERIMENT, 1.5, 0)
        destination = self.output / EXPERIMENT / "run-configuration.json"
        self.wait_for(destination.is_file)

        self.assertIsNone(process.poll())
        self.assertEqual(json.loads(destination.read_text(encoding="utf-8")), self.configuration)
        self.assertFalse(self.staged.exists())
        self.assertTrue((destination.parent / "run-001").is_dir())
        self.assertTrue((destination.parent / "experiment.log").is_file())
        self.assertEqual((self.output / "runner.log").read_text(encoding="utf-8"), "runner log")
        output, _ = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 0, output)

        archive = Path(self.temporary.name) / "download.zip"
        subprocess.run(
            [sys.executable, "-m", "zipfile", "-c", str(archive), self.output.name],
            cwd=self.output.parent,
            check=True,
        )
        with zipfile.ZipFile(archive) as downloaded:
            path = f"{self.output.name}/{EXPERIMENT}/run-configuration.json"
            self.assertEqual(json.loads(downloaded.read(path)), self.configuration)

    def test_slow_runner_still_moves_snapshot_after_startup_check(self):
        process = self.launch(RUNNER, self.output, 5.3, EXPERIMENT, 0.2, 0)
        time.sleep(5.1)
        self.assertIsNone(process.poll())
        self.assertTrue(self.staged.is_file())
        output, _ = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 0, output)
        self.assertTrue((self.output / EXPERIMENT / "run-configuration.json").is_file())
        self.assertFalse(self.staged.exists())

    def test_runner_failing_after_creating_folder_still_gets_snapshot(self):
        process = self.launch(RUNNER, self.output, 0, EXPERIMENT, 0, 7)
        output, _ = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 7, output)
        self.assertTrue((self.output / EXPERIMENT / "run-configuration.json").is_file())
        self.assertFalse(self.staged.exists())

    def test_runner_failing_before_creating_folder_preserves_snapshot(self):
        process = self.launch("import sys; sys.exit(9)")
        output, _ = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 9, output)
        self.assertIn("Runner exited before creating an experiment folder", output)
        self.assertEqual(json.loads(self.staged.read_text(encoding="utf-8")), self.configuration)

    def test_move_failure_is_reported_and_stops_runner(self):
        runner = """
import pathlib, sys, time
experiment = pathlib.Path(sys.argv[1]) / "experiment"
experiment.mkdir()
(experiment / "run-configuration.json").mkdir()
time.sleep(20)
"""
        process = self.launch(runner, self.output)
        output, _ = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 1, output)
        self.assertIn("Cannot move run configuration", output)
        self.assertTrue(self.staged.is_file())

    def test_ambiguous_experiment_folders_are_reported(self):
        runner = """
import pathlib, sys, time
output = pathlib.Path(sys.argv[1])
(output / "first").mkdir()
(output / "second").mkdir()
time.sleep(20)
"""
        process = self.launch(runner, self.output)
        output, _ = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 1, output)
        self.assertIn("multiple directories exist", output)
        self.assertTrue(self.staged.is_file())

    @unittest.skipUnless(os.name == "posix", "POSIX process groups are required")
    def test_stop_signal_reaches_runner_and_keeps_snapshot(self):
        process = self.launch(RUNNER, self.output, 0, EXPERIMENT, 20, 0, start_new_session=True)
        destination = self.output / EXPERIMENT / "run-configuration.json"
        self.wait_for(destination.is_file)
        os.killpg(process.pid, signal.SIGTERM)
        output, _ = process.communicate(timeout=5)
        self.assertEqual(process.returncode, 128 + signal.SIGTERM, output)
        self.assertTrue(destination.is_file())


class NotebookLaunchTests(unittest.TestCase):
    def test_notebook_stages_snapshot_and_starts_launcher_in_own_session(self):
        notebook = json.loads((ROOT / "04_run_experiment.ipynb").read_text(encoding="utf-8"))
        cell = next(cell for cell in notebook["cells"] if cell.get("id") == "04-05")
        commands = []
        saved = []
        uploaded = []
        control = object()
        slice_ = object()

        def remote_run(node, command, quiet=False):
            self.assertIs(node, control)
            commands.append(command)
            return "no" if command.startswith("test -f ") else ""

        def configuration(actual_slice, actual_control, checkout, settings):
            self.assertIs(actual_slice, slice_)
            self.assertIs(actual_control, control)
            self.assertEqual(checkout, "sd-baseline")
            return {"settings": settings}

        def save(actual_control, directory, snapshot):
            self.assertIs(actual_control, control)
            saved.append((directory, snapshot))

        def upload(actual_control, local_path):
            self.assertIs(actual_control, control)
            uploaded.append(local_path)
            return "fabric-deployment/start_experiment.py"

        config = SimpleNamespace(
            METHODS={"baseline": {"runner": "scripts/run_baseline_experiment.py"}},
            CURRENT_RUN_FILE="fabric-deployment/current_run",
            RESULTS_DIR="experiment-results",
            checkout_dir=lambda method: f"sd-{method}",
        )
        context = {
            "METHOD": "baseline",
            "CYCLES": 20,
            "RUNS": 2,
            "EXTRA_ARGS": "--example value",
            "cfg": config,
            "fh": SimpleNamespace(run_configuration=configuration, save_run_configuration=save, upload_script=upload),
            "run": remote_run,
            "control": control,
            "slice_": slice_,
            "checkout": "sd-baseline",
            "python": "$HOME/experiment-venv/bin/python",
            "datetime": datetime,
        }
        with contextlib.redirect_stdout(io.StringIO()):
            exec(compile("".join(cell["source"]), "04-05", "exec"), context)

        run_id = context["run_id"]
        output_dir = f"$HOME/experiment-results/{run_id}"
        self.assertEqual(saved[0][0], output_dir)
        self.assertEqual(saved[0][1]["settings"]["runs"], 2)
        self.assertEqual(uploaded, ["node_scripts/start_experiment.py"])
        self.assertIn(f"mkdir -p $HOME/experiment-results && mkdir {output_dir}", commands)
        self.assertIn(
            f"nohup setsid $HOME/experiment-venv/bin/python $HOME/fabric-deployment/start_experiment.py "
            f"{output_dir} $HOME/experiment-venv/bin/python scripts/run_baseline_experiment.py "
            f"--runs 2 --output-dir {output_dir} --cycles 20 --example value",
            commands[-1],
        )
        self.assertIn(f"> {output_dir}/runner.log", commands[-1])
        self.assertIn("fabric-deployment/current_run", commands[-1])
        self.assertIn("kill -0 $!", commands[-1])


if __name__ == "__main__":
    unittest.main()

import copy
import json
import math
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from plot_vfl_timing import load_run, plot_comparison, plot_run
import matplotlib.pyplot as plt


class TimingPlotTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name) / "run.json"
        self.data = {
            "status": "done",
            "results": [{
                "train_round": 0,
                "timing": {
                    "round_ms": 100, "policy_check_ms": 20,
                    "coordination_overhead_ms": 1, "barrier_wait_ms": 30,
                    "bytes_sent": 1048576, "bytes_received": 2097152,
                    "measured_calls": 2, "unmeasured_calls": 0,
                    "algorithm_ms": 50, "communication_ms": 90,
                },
            }],
        }

    def load(self):
        self.path.write_text(json.dumps(self.data), encoding="utf-8")
        return load_run(self.path, "Local")

    def test_units_and_time_domains(self):
        run = self.load()
        self.assertEqual(run.values("training_ms"), [79])
        self.assertEqual(run.mean("round_ms", 1000), 0.1)
        self.assertEqual(run.mean("bytes_sent", 1048576), 1)
        self.assertEqual(run.per_measured_call("communication_ms"), 45)
        self.assertEqual(run.coverage(), "2/2 calls measured")
        self.assertGreater(run.mean("algorithm_ms") + run.mean("communication_ms"), run.mean("round_ms"))

    def test_partial_coverage_and_weighted_call_average(self):
        second = copy.deepcopy(self.data["results"][0])
        second["train_round"] = 1
        second["timing"].update(measured_calls=1, unmeasured_calls=2, communication_ms=30)
        self.data["results"].append(second)
        with self.assertWarnsRegex(UserWarning, "3/5 calls measured"):
            run = self.load()
        self.assertEqual(run.per_measured_call("communication_ms"), 40)

    def test_missing_sidecar_values_are_not_zero(self):
        timing = self.data["results"][0]["timing"]
        timing.update(measured_calls=0, unmeasured_calls=2)
        del timing["algorithm_ms"], timing["communication_ms"]
        with self.assertWarns(UserWarning):
            run = self.load()
        self.assertTrue(math.isnan(run.values("communication_ms")[0]))
        self.assertTrue(math.isnan(run.per_measured_call("communication_ms")))
        output = self.path.with_suffix(".png")
        plot_run(run, output)
        self.assertGreater(output.stat().st_size, 1000)

    def test_older_export_reports_unknown_coverage(self):
        timing = self.data["results"][0]["timing"]
        for key in ("measured_calls", "unmeasured_calls", "algorithm_ms", "communication_ms"):
            del timing[key]
        with self.assertWarnsRegex(UserWarning, "coverage unavailable"):
            self.assertEqual(self.load().coverage(), "coverage unavailable")

    def test_invalid_values_are_rejected(self):
        for value in (-1, math.inf, math.nan, "100", True, None):
            with self.subTest(value=value):
                self.data["results"][0]["timing"]["round_ms"] = value
                with self.assertRaisesRegex(ValueError, "round_ms"):
                    self.load()

    def test_skip_rounds_preserves_exported_indices_and_recomputes_metrics(self):
        second = copy.deepcopy(self.data["results"][0])
        second["train_round"] = 7
        second["timing"]["communication_ms"] = 30
        self.data["results"].append(second)
        self.load()
        run = load_run(self.path, "Steady", skip_rounds=1)
        self.assertEqual(run.rounds, [7])
        self.assertEqual(run.coverage(), "2/2 calls measured")
        self.assertEqual(run.per_measured_call("communication_ms"), 15)
        self.assertEqual(run.skipped, 1)
        for skipped in (-1, 2):
            with self.assertRaises(ValueError):
                load_run(self.path, "Steady", skip_rounds=skipped)

    def test_missing_timing_and_empty_results(self):
        del self.data["results"][0]["timing"]
        with self.assertRaisesRegex(ValueError, "missing gateway timing"):
            self.load()
        self.data["results"] = []
        with self.assertRaisesRegex(ValueError, "no completed rounds"):
            self.load()

    def test_plot_data_and_rendering(self):
        run = self.load()
        with patch("plot_vfl_timing.save_figure") as save:
            plot_run(run, self.path.with_suffix(".png"))
            figure = save.call_args.args[0]
            wall, calls, sync, traffic = figure.axes
            for bar, expected in zip(wall.patches, [0.079, 0.02, 0.001]):
                self.assertAlmostEqual(bar.get_height(), expected)
            self.assertEqual([bar.get_height() for bar in calls.patches], [50, 90])
            self.assertEqual(list(sync.lines[0].get_ydata()), [30])
            self.assertEqual(list(traffic.lines[0].get_ydata()), [1])
            plt.close(figure)
        for name, plotter, payload in (
            ("run.png", plot_run, run),
            ("comparison.png", plot_comparison, [run, run]),
        ):
            output = self.path.parent / name
            plotter(payload, output)
            self.assertGreater(output.stat().st_size, 1000)


if __name__ == "__main__":
    unittest.main()

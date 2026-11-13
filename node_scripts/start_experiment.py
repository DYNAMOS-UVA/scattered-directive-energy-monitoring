#!/usr/bin/env python3
"""Launch an experiment and place its configuration beside the runner's run folders."""

import os
import signal
import subprocess
import sys
import time
from pathlib import Path


def run_experiment(output_dir, command):
    configuration = output_dir / "run-configuration.json"
    runner = subprocess.Popen(command)

    def forward_signal(signum, _frame):
        runner.send_signal(signum)

    signal.signal(signal.SIGTERM, forward_signal)
    signal.signal(signal.SIGINT, forward_signal)

    def fail(message):
        print(f"{message} Configuration remains at {configuration}.", file=sys.stderr, flush=True)
        if runner.poll() is None:
            if os.name == "posix" and os.getpgrp() == os.getpid():
                os.killpg(os.getpgrp(), signal.SIGTERM)
            else:
                runner.terminate()
        runner.wait()
        return 1

    while True:
        status = runner.poll()
        try:
            with os.scandir(output_dir) as entries:
                experiments = [Path(entry.path) for entry in entries if entry.is_dir(follow_symlinks=False)]
        except OSError as error:
            return fail(f"Cannot inspect experiment output directory {output_dir}: {error}.")

        if len(experiments) > 1:
            return fail(f"Cannot identify the experiment folder in {output_dir}: multiple directories exist.")
        if experiments:
            destination = experiments[0] / configuration.name
            try:
                configuration.replace(destination)
            except OSError as error:
                return fail(f"Cannot move run configuration to {destination}: {error}.")
            print(f"Saved run configuration: {destination}", flush=True)
            status = runner.wait()
            return status if status >= 0 else 128 - status
        if status is not None:
            print(f"Runner exited before creating an experiment folder. Configuration remains at {configuration}.",
                  file=sys.stderr, flush=True)
            return (status if status >= 0 else 128 - status) or 1
        time.sleep(0.25)


if __name__ == "__main__":
    if len(sys.argv) < 3:
        raise SystemExit("Usage: start_experiment.py OUTPUT_DIR RUNNER [ARG ...]")
    sys.exit(run_experiment(Path(sys.argv[1]), sys.argv[2:]))

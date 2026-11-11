"""Single source of truth for the FABRIC deployment notebooks."""

SLICE_NAME = "S-Drive-VFL-methods"
IMAGE = "default_ubuntu_24"
LEASE_DAYS = 14

# Repository whose method branches are cloned onto the control VM.
REPO_URL = "https://github.com/DYNAMOS-UVA/scattered-directive-energy-monitoring.git"

# FABlib keys inside the FABRIC JupyterHub container.
SLICE_PRIVATE_KEY = "/home/fabric/work/fabric_config/slice_key"

# The slice must contain every node used by any method: agents are pinned to the
# Kubernetes node with the same name. Node names must match /[a-z][a-z0-9]+/.
# Remove "host" to let FABRIC choose a worker at the site.
NODES = [
    {"name": "control",     "role": "control",    "cores": 2, "ram": 8,  "disk": 100, "site": "AMST", "host": "amst-w3.fabric-testbed.net"},
    {"name": "dynamos",     "role": "dynamos",    "cores": 4, "ram": 16, "disk": 100, "site": "AMST", "host": "amst-w3.fabric-testbed.net"},
    {"name": "server",      "role": "agent",      "cores": 8, "ram": 16, "disk": 100, "site": "AMST", "host": "amst-w3.fabric-testbed.net"},
    {"name": "aggregator",  "role": "agent",      "cores": 8, "ram": 16, "disk": 100, "site": "AMST", "host": "amst-w3.fabric-testbed.net"},
    {"name": "authority",   "role": "agent",      "cores": 8, "ram": 16, "disk": 100, "site": "AMST", "host": "amst-w3.fabric-testbed.net"},
    {"name": "clientone",   "role": "agent",      "cores": 8, "ram": 16, "disk": 100, "site": "LOSA", "host": "losa-w2.fabric-testbed.net"},
    {"name": "clienttwo",   "role": "agent",      "cores": 8, "ram": 16, "disk": 100, "site": "HAWI", "host": "hawi-w3.fabric-testbed.net"},
    {"name": "clientthree", "role": "agent",      "cores": 8, "ram": 16, "disk": 100, "site": "AMST", "host": "amst-w3.fabric-testbed.net"},
    {"name": "surf",        "role": "thirdparty", "cores": 8, "ram": 16, "disk": 100, "site": "AMST", "host": "amst-w3.fabric-testbed.net"},
]

CONTROL_NODE = "control"
DYNAMOS_NODE = "dynamos"

# hostPath directories used by the DYNAMOS charts on the dynamos node (identical on all method branches).
DYNAMOS_DATA_DIRS = [
    "/mnt/rabbit-data",
    "/mnt/etcd-data",
    "/mnt/core-etcd-data-0",
    "/mnt/core-etcd-data-1",
    "/mnt/core-etcd-data-2",
]

# Remote locations on the control VM, relative to the ubuntu user's home directory.
REMOTE_TOOLS_DIR = "fabric-deployment"
EXPERIMENT_VENV = "experiment-venv"
RESULTS_DIR = "experiment-results"
LOGS_DIR = "experiment-logs"  # runner logs of older runs; new runs write runner.log inside their results folder
INSTALLED_METHOD_FILE = f"{REMOTE_TOOLS_DIR}/installed_method"
CURRENT_RUN_FILE = f"{REMOTE_TOOLS_DIR}/current_run"

# Per-method settings. Each branch's dynamos.conf supplies its default image tag.
METHODS = {
    "baseline": {
        "runner": "scripts/run_baseline_experiment.py",
        "requirements": "plots/requirements.txt",
        "smoke_test": "CYCLES=3 OUTPUT_FILE={results}/smoke-tests/baseline.json bash scripts/single_request.sh",
    },
    "fed-bcd": {
        "runner": "scripts/run_fed_bcd_experiment.py",
        "requirements": "plots/requirements.txt",
        "smoke_test": "CYCLES=4 COMMUNICATION_FREQUENCY=2 OUTPUT_FILE={results}/smoke-tests/fed-bcd.json bash scripts/single_request.sh",
    },
    "semi-overlap-fed-bcd": {
        "runner": "scripts/run_semi_overlap_experiment.py",
        "requirements": "plots/requirements.txt",
        "smoke_test": "CYCLES=4 COMMUNICATION_FREQUENCY=2 OUTPUT_FILE={results}/smoke-tests/semi-overlap-fed-bcd.json bash scripts/single_request.sh",
    },
    "fed-encrypt-dynamic": {
        "runner": "scripts/run_fed_encrypt_experiment.py",
        "requirements": "scripts/requirements-experiment.txt",
        "smoke_test": "{python} scripts/run_fed_encrypt_experiment.py --cycles 1 --output-dir {results}/smoke-tests/fed-encrypt-dynamic",
    },
}


def checkout_dir(branch):
    """Remote checkout directory for a method branch on the control VM (relative to ~)."""
    return f"sd-{branch}"

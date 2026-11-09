# Scattered Directive on FABRIC: VFL method deployment

Notebooks for running the four VFL methods of Scattered Directive / DYNAMOS on **one** FABRIC slice, one method at a time:

| Method | Branch | Images (`dynamos1/*`) | Experiment runner |
|---|---|---|---|
| Baseline | `baseline` | `:baseline` | `scripts/run_baseline_experiment.py` |
| FedBCD | `fed-bcd` | `:fed-bcd` | `scripts/run_fed_bcd_experiment.py` |
| Semi-overlap FedBCD | `semi-overlap-fed-bcd` | `:semi-overlap-fed-bcd` | `scripts/run_semi_overlap_experiment.py` |
| FedEncrypt (dynamic) | `fed-encrypt-dynamic` | `:fed-encrypt-dynamic` | `scripts/run_fed_encrypt_experiment.py` |

The slice and Kubernetes cluster are created once. Switching method only reinstalls DYNAMOS from the selected branch.

## Usage

Clone this branch in the FABRIC JupyterHub and open the notebooks from this folder:

```bash
git clone -b fabric-deployment --single-branch https://github.com/DYNAMOS-UVA/scattered-directive-energy-monitoring.git sd-fabric-deployment
```

| Notebook | When | What it does |
|---|---|---|
| `00_configure_fablib` | once per JupyterHub account | Sets the FABRIC project, validates/creates bastion and slice keys. |
| `01_create_slice` | once | Creates the slice, configures inter-site routing, checks connectivity. |
| `02_setup_cluster` | once | Base packages, Kubernetes (Kubespray + Flannel), Helm, Linkerd + Jaeger, Docker, k9s, experiment venv. |
| `03_install_method` | each method switch | Resets the previous install, clones/updates the branch on `control`, runs its installer, waits for readiness, smoke test. |
| `04_run_experiment` | per experiment | Starts the runner in the background on `control`, shows status, downloads results to `results/`. |
| `99_slice_management` | as needed | Lease renewal, SSH commands, network repair after reboot, slice deletion. |

Run the cells in order; do not use *Run All*. Destructive cells are guarded by flags (`RESET_CLUSTER`, `STOP`, `CONFIRM_DELETE`).

## Configuration

Everything is in [deployment_config.py](deployment_config.py):

- `SLICE_NAME`, `IMAGE`, `LEASE_DAYS`, `REPO_URL`.
- `NODES`: one VM per Kubernetes node (name, site, cores, RAM, disk, optional pinned `host`). Agents are pinned by
  Kubernetes node name, so the slice must contain every agent used by any method:
  `server`, `clientone`, `clienttwo`, `clientthree` and, for `fed-encrypt-dynamic`, `aggregator` and `authority`.
  `surf` hosts the third party, `dynamos` the core services (RabbitMQ, etcd, Linkerd), `control` the Kubernetes control plane.
  Remove `host` if a pinned worker has no capacity.
- `METHODS`: runner, Python requirements and smoke test per branch.

## How it works

- **Access**: FABlib (`fabrictestbed_extensions`) using the project token and SSH through the FABRIC bastion with the slice key.
  The slice key is also copied to `control` so Ansible (Kubespray) can reach the other VMs.
- **Network**: one FABNetv4 network per site; [fabric_helpers.py](fabric_helpers.py) assigns addresses and routes between them.
  Addresses are not persistent: after rebooting a VM, run *Repair networking* in `99_slice_management`.
- **Installing a method**: on `control`, the branch is checked out to `~/sd-<branch>` and its own
  `configuration/dynamos-configuration.sh fabric --no-monitoring` is run. Each branch's `dynamos.conf` selects
  `DOCKERHUB_ACCOUNT=dynamos1` and the image tag (default: the branch name; override with `IMAGE_TAG`).
  The installed method is recorded in `~/fabric-deployment/installed_method`.
- **Switching methods**: [node_scripts/reset_dynamos.sh](node_scripts/reset_dynamos.sh) removes all DYNAMOS Helm
  releases, namespaces and PersistentVolumes regardless of which branch installed them, and the notebook wipes the
  hostPath data (`/mnt/rabbit-data`, `/mnt/etcd-data`, `/mnt/core-etcd-data-*`) on `dynamos`. Kubernetes, Linkerd and
  the checkouts are kept.
- **Monitoring**: the Prometheus/Kepler stack is skipped (`INSTALL_MONITORING = False`); it is not needed for the VFL experiments.
- **Experiments**: started with `nohup setsid` on `control`; logs go to `~/experiment-logs/`, results to
  `~/experiment-results/<method>-<timestamp>/`. The download cell copies both to `results/` in JupyterHub.

## Notes

- `UPDATE_CHECKOUT = True` hard-resets `~/sd-<branch>` to `origin/<branch>`, discarding changes made on `control`.
- Renew the lease (`99_slice_management`) before it expires; an expired slice is deleted by FABRIC.
- Useful on `control`: `k9s`, `kubectl get pods -A -o wide`, `helm list -A`.

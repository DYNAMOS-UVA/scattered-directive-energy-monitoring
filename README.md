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
| `03_install_method` | each method switch | Resets the previous install, clones/updates the branch on `control`, runs its installer, waits for readiness, pre-pulls the job images on the agent nodes, smoke test. |
| `04_run_experiment` | per experiment | Starts the runner in the background on `control`, shows status, lists the stored runs and downloads one as a zip to `results/`. |
| `99_slice_management` | as needed | Lease renewal, SSH commands, network repair after reboot, slice deletion. |

Run the cells in order; do not use *Run All*. Destructive cells are guarded by flags (`RESET_CLUSTER`, `STOP`, `CONFIRM_DELETE`).

### Step by step

**One-time setup**

1. Push the branch so JupyterHub can clone it: `git push -u origin fabric-deployment`.
2. In the FABRIC JupyterHub, clone only this branch (command above) and open the notebooks from that folder.
3. If needed, edit [deployment_config.py](deployment_config.py): slice name, sites, VM sizes and pinned hosts.
4. Run **00_configure_fablib** with your project ID. It sets up your FABRIC keys and config; needed once per JupyterHub account.
5. Run **01_create_slice**. It creates the 9 VMs (about 10–20 min), connects the sites to each other and checks that `control` can reach every VM.
6. Run **02_setup_cluster**. It installs Kubernetes and the shared tools, so the cluster is ready for any method (about 30–60 min).

**For each method (`baseline`, `fed-bcd`, `semi-overlap-fed-bcd`, `fed-encrypt-dynamic`)**

7. Open **03_install_method**, set `METHOD` at the top and run the cells. It removes the previous method and wipes its data, clones the branch onto `control`, installs that branch's DYNAMOS images, pre-pulls the training job images on the agent nodes (agents give up after 60 s if they still have to download them) and runs a short smoke test.
8. Open **04_run_experiment**, set the same `METHOD` plus cycles/runs/extra arguments, and start the run. It keeps running on `control` even if you close Jupyter.
9. Rerun the status cell to follow progress. When it shows **FINISHED**, the run is kept on `control` in `~/experiment-results/<method>-<timestamp>/` (also reachable over SSH). Run the download cell to copy it into JupyterHub as `results/<method>-<timestamp>.zip` and its extracted folder.
10. Go back to step 7 for the next method. The slice and cluster are reused.

**Maintenance and teardown**

11. Use **99_slice_management** as needed:
    - renew the lease before it expires
    - get SSH commands
    - repair networking after a VM reboot
    - delete the slice when you're completely done (set `CONFIRM_DELETE = True`)

The `.sh` scripts in `node_scripts/` are called by the notebooks; you don't run them yourself.

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
- **Experiments**: started with `nohup setsid` on `control`. Each run gets its own folder
  `~/experiment-results/<method>-<timestamp>/` containing the runner's output, its log (`runner.log`) and
  `run-configuration.json` (notebook settings and runner command, branch and commit of the checkout, DYNAMOS and
  job images, node sites/hosts/sizes/IPs and pod placement). The folder survives DYNAMOS resets and reinstalls
  (only deleting the slice removes it). The download cell zips a run and copies it to `results/` in JupyterHub.
  Runs started before this layout keep their log in `~/experiment-logs/`; the download cell adds it to the zip.

## Notes

- `UPDATE_CHECKOUT = True` hard-resets `~/sd-<branch>` to `origin/<branch>`, discarding changes made on `control`.
- Renew the lease (`99_slice_management`) before it expires; an expired slice is deleted by FABRIC.
- Useful on `control`: `k9s`, `kubectl get pods -A -o wide`, `helm list -A`.

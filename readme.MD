# Scattered Directive

Welcome to Scattered Directive. This is an automated and reproducible framework to deploy the digital data marketplace
platform [DYNAMOS](https://github.com/Jorrit05/DYNAMOS) on the [FABRIC](https://portal.fabric-testbed.net) Research
Infrastructure.

Scattered Directive automates the entire setup, from requesting nodes to configuring and deploying DYNAMOS with the
relevant relations to the nodes all defined. This allows researchers and developers (you!) to utilise DYNAMOS without
any hassle.

To prove our implementation of Scattered Directive, we utilised it to implement a Vertical Federated Learning workload,
creating a new archetype to support shared execution of the global model training on a trusted third party. This
archetype is aptly named *Shared Execution via TTP*.

## Setup

### Local or FABRIC deployment from Bash

From the repository root, with Docker, Helm and kubectl configured:

```bash
make -C go all
make -C python vfl
bash configuration/dynamos-configuration-no-monitoring.sh local
```

Use `fabric` instead of `local` for FABRIC charts. To install monitoring too,
use `bash configuration/dynamos-configuration.sh local`. The no-monitoring
wrapper skips monitoring installation without removing an existing stack.

`dynamos.conf` detects this checkout automatically. If an exported
`DYNAMOS_ROOT` points to another checkout, unset it before installing.
The installer selects Docker Hub account `dynamos1` and image tag
`semi-overlap-fed-bcd` for application services and dynamically spawned workers,
including job sidecars. Override the deployment tag with `DYNAMOS_IMAGE_TAG`.
Per-service `*_TAG` agent environment overrides take precedence over the shared
`MICROSERVICE_TAG`; rebuild the agent image to deploy this tag-selection change.

Both Makefiles build and push the current Git branch tag by default.
`BUILD_BOTH=1` additionally publishes `latest`; `PUSH=0` disables pushing.
Build and push errors stop execution, preventing a failed build from pushing
an older local image. Ensure Docker/WSL integration and Docker Hub credentials
are working and that your account can push to `dynamos1`. Never put credentials
in source or logs. For the first deployment, publish all Go services and the
two VFL Python images; later rebuilds can target only changed services.
Ensure the cluster does not reuse cached older images with the same tag.

To uninstall:

```bash
bash configuration/uninstall-dynamos.sh
```

This is destructive: it deletes agent Jobs, uninstalls application, monitoring
and NGINX releases, then removes the namespace release and its namespaces/PVCs.
Missing releases are skipped; other Helm/kubectl errors stop cleanup.
Application releases are looked up in `${HELM_NAMESPACE:-default}`.
The agreements file is required to discover agent namespaces for Job deletion.
The local etcd chart omits the obsolete initialization Job; the existing PVC
helpers populate configuration instead.

These changes do not import eFLINT or alter the semi-overlap training pipeline.
Timing instrumentation, experiments and plotting are separate follow-up steps;
see [the replication checklist](REPLICATION-CHECKLIST.md).

Practically everything is automated. The only thing you need to do is open the FABRIC Jupyter Notebook, clone this
repository into there, open `fabric/dynamos.ipynb` configure the preferred configuration of nodes and execute.

#!/bin/bash
# DESTRUCTIVE: removes Kubernetes from all slice VMs (use only to rebuild the cluster from scratch).
set -euo pipefail

cd "$HOME/kubespray"
source ./venv/bin/activate

ansible-playbook -i inventory/dynamos/inventory.ini reset.yml -b -v -u ubuntu --private-key="$HOME/.ssh/slice_key" -e reset_confirmation=yes

#!/bin/bash
# Installs Kubernetes on all slice VMs with Kubespray and configures kubectl for the ubuntu user.
set -euo pipefail

cd "$HOME/kubespray"
source ./venv/bin/activate

ansible-playbook -i inventory/dynamos/inventory.ini cluster.yml -b -v -u ubuntu --private-key="$HOME/.ssh/slice_key"

mkdir -p "$HOME/.kube"
sudo cp -f /etc/kubernetes/admin.conf "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
kubectl get nodes -o wide

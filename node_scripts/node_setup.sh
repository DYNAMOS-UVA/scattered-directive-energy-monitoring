#!/bin/bash
# Base packages for every VM: Docker, kubeadm/kubelet/kubectl and swap off (required by Kubespray).
set -euo pipefail

sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get upgrade -y
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io apt-transport-https curl python3 python3-venv python3-pip ca-certificates gpg

sudo rm -f /etc/apt/sources.list.d/docker.list /etc/apt/sources.list.d/kubernetes.list

echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.30/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list

sudo mkdir -p -m 755 /etc/apt/keyrings
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.30/deb/Release.key | sudo gpg --dearmor --yes -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg

sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y kubelet kubeadm kubectl
sudo apt-mark hold kubelet kubeadm kubectl

sudo swapoff -a

echo '{ "exec-opts": ["native.cgroupdriver=systemd"] }' | sudo tee /etc/docker/daemon.json > /dev/null

sudo systemctl enable docker
sudo systemctl daemon-reload
sudo systemctl restart docker

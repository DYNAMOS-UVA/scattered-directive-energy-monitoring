#!/bin/bash
# Method-independent cluster add-ons on the control VM: Helm, Gateway API, Linkerd + Jaeger
# (pinned to the dynamos node), Docker (used by the installers to hash the RabbitMQ password),
# an etcd client, k9s and Python venv support for the experiment runners.
set -euo pipefail

if ! command -v helm > /dev/null; then
    curl -fsSL https://packages.buildkite.com/helm-linux/helm-debian/gpgkey | gpg --dearmor | sudo tee /usr/share/keyrings/helm.gpg > /dev/null
    echo "deb [signed-by=/usr/share/keyrings/helm.gpg] https://packages.buildkite.com/helm-linux/helm-debian/any/ any main" | \
        sudo tee /etc/apt/sources.list.d/helm-stable-debian.list
    sudo apt-get update
    if ! sudo apt-get install -y helm; then
        echo "APT install failed; installing Helm manually."
        version="v3.19.0"
        curl -fsSL "https://get.helm.sh/helm-${version}-linux-amd64.tar.gz" -o /tmp/helm.tar.gz
        tar -zxf /tmp/helm.tar.gz -C /tmp
        sudo mv /tmp/linux-amd64/helm /usr/local/bin/helm
        rm -rf /tmp/linux-amd64 /tmp/helm.tar.gz
    fi
fi
helm version

# Kubespray can leave Docker unusable on the control node; reinstall it for the installers.
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io python3-venv python3-pip
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y etcd-client || echo "WARNING: etcd-client not installed (optional)."
sudo systemctl restart docker
sudo usermod -aG docker "$USER"
sudo chmod 666 /var/run/docker.sock

if ! command -v k9s > /dev/null; then
    wget -q https://github.com/derailed/k9s/releases/download/v0.32.5/k9s_linux_amd64.deb -O /tmp/k9s.deb
    sudo apt-get install -y /tmp/k9s.deb
    rm -f /tmp/k9s.deb
fi

if ! grep -q 'api-gateway.api-gateway.svc.cluster.local' /etc/hosts; then
    echo "127.0.0.1 api-gateway.api-gateway.svc.cluster.local" | sudo tee -a /etc/hosts
fi

export PATH="$PATH:$HOME/.linkerd2/bin"
if ! grep -q '.linkerd2/bin' "$HOME/.bashrc"; then
    echo 'export PATH=$PATH:$HOME/.linkerd2/bin' >> "$HOME/.bashrc"
fi
if ! command -v linkerd > /dev/null; then
    curl --proto '=https' --tlsv1.2 -sSfL https://run.linkerd.io/install-edge | LINKERD2_VERSION=edge-25.8.1 sh
fi

kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml

if ! kubectl get namespace linkerd > /dev/null 2>&1; then
    linkerd check --pre
    linkerd install --set nodeSelector."kubernetes\\.io/hostname"=dynamos --crds | kubectl apply -f -
    linkerd install --set nodeSelector."kubernetes\\.io/hostname"=dynamos | kubectl apply -f -
fi
linkerd check

if ! kubectl get namespace linkerd-jaeger > /dev/null 2>&1; then
    linkerd jaeger install \
        --set collector.nodeSelector."kubernetes\\.io/hostname"=dynamos \
        --set nodeSelector."kubernetes\\.io/hostname"=dynamos \
        --set jaeger.nodeSelector."kubernetes\\.io/hostname"=dynamos \
        --set webhook.nodeSelector."kubernetes\\.io/hostname"=dynamos \
        | kubectl apply -f -
fi
linkerd jaeger check

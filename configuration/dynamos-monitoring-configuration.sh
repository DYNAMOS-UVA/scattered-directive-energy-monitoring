#!/bin/bash

# Importing dynamos config
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
source "${SCRIPT_DIR}/../dynamos.conf"

if [ "$1" == "local" ]; then
    CHARTS_PATH="${CHARTS_LOCAL_PATH}"
elif [ "$1" == "fabric" ]; then
    CHARTS_PATH="${CHARTS_FABRIC_PATH}"
else
    echo ">!< ERROR: Environment is not specified: 'local' or 'fabric'. >!<"
    exit 1
fi

# Paths
monitoring_chart="${CHARTS_PATH}/monitoring"

kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -

echo -e "Installing Prometheus stack...\n"
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm upgrade -i -f ${monitoring_chart}/prometheus-values.yaml prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --create-namespace

echo -e "\nInstalling Kepler...\n"
helm repo add kepler https://sustainable-computing-io.github.io/kepler-helm-chart
helm repo update
helm upgrade -i kepler kepler/kepler \
    --namespace monitoring \
    --version 0.5.12 \
    --set serviceMonitor.enabled=true \
    --set serviceMonitor.labels.release=prometheus \
    --values ${monitoring_chart}/kepler-values.yaml \

echo -e "\nSetting up additionally monitoring charts...\n"
helm upgrade -i monitoring ${monitoring_chart} --namespace monitoring -f ${monitoring_chart}/values.yaml
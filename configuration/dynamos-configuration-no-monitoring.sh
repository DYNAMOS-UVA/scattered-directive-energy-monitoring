#!/bin/bash

set -e

# Same as dynamos-configuration.sh, but skips installing the "monitoring" namespace
# (Prometheus stack, Grafana, Loki, Kepler, cadvisor) to save local cluster resources.

# Importing dynamos config
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
source "${SCRIPT_DIR}/../dynamos.conf"

if [ "$1" == "local" ]; then
    CHARTS_PATH="${CHARTS_LOCAL_PATH}"
elif [ "$1" == "fabric" ]; then
    CHARTS_PATH="${CHARTS_FABRIC_PATH}"
else
    echo ">!< ERROR: You must specify an environment argument: 'local' or 'fabric'. >!<"
    exit 1
fi

echo -e "\n=============== Started setting up DYNAMOS ($1, no monitoring) ===============\n"

# Change this to the path of the DYNAMOS repository on your disk
echo -e "Setting up paths...\n"

# Charts
core_chart="${CHARTS_PATH}/core"
namespace_chart="${CHARTS_PATH}/namespaces"
orchestrator_chart="${CHARTS_PATH}/orchestrator"
agents_chart="${CHARTS_PATH}/agents"
ttp_chart="${CHARTS_PATH}/thirdparty"
api_gw_chart="${CHARTS_PATH}/api-gateway"

# Config
k8s_service_files="${CONFIG_PATH}/k8s_service_files"
etcd_launch_files="${CONFIG_PATH}/etcd_launch_files"

# Add agents
agents=$(grep '"name":' ${etcd_launch_files}/agreements.json | awk -F'"' '{print $4}' | paste -sd "," -)
echo -e "Agents discovered: $agents\n"

echo -e "Generating agents and third parties charts...\n"
configure_dynamos="${CONFIG_PATH}/configure_dynamos.sh"
chmod +x ${configure_dynamos}
${configure_dynamos} $agents "" "$1"

rabbit_definitions_file="${k8s_service_files}/definitions.json"
example_definitions_file="${k8s_service_files}/definitions_example.json"

cp "$example_definitions_file" "$rabbit_definitions_file"
echo "definitions_example.json copied over definitions.json to ensure a clean file"

echo -e "Generating RabbitMQ password...\n"
# Create a password for a rabbit user
rabbit_pw=$(openssl rand -hex 16)

# Use the RabbitCtl to make a special hash of that password:
hashed_pw=$($SUDO docker run --rm rabbitmq:3-management rabbitmqctl hash_password $rabbit_pw)
actual_hash=$(echo "$hashed_pw" | cut -d $'\n' -f2)

echo -e "Replacing tokens...\n"
cp ${k8s_service_files}/definitions_example.json ${rabbit_definitions_file}


# The Rabbit Hashed password needs to be in definitions.json file, that is the configuration for RabbitMQ
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS sed
    sed -i '' "s|%PASSWORD%|${actual_hash}|g" ${rabbit_definitions_file}
else
    # GNU sed
    sed -i "s|%PASSWORD%|${actual_hash}|g" ${rabbit_definitions_file}
fi

echo -e "Installing namespaces...\n"
helm upgrade -i -f ${namespace_chart}/values.yaml namespaces ${namespace_chart} --set secret.password=${rabbit_pw}

echo -e "\nPreparing PVC...\n"

{
    cd ${DYNAMOS_ROOT}/configuration
    ./fill-rabbit-pvc.sh "$1"
}

# NOTE: monitoring namespace (Prometheus/Grafana/Loki/Kepler/cadvisor) intentionally skipped here.

echo -e "\nInstalling NGINX...\n"
helm install -f ${core_chart}/ingress-values.yaml nginx oci://ghcr.io/nginxinc/charts/nginx-ingress -n ingress --version 0.18.0

echo -e "Installing DYNAMOS core...\n"
helm upgrade -i -f ${core_chart}/values.yaml core ${core_chart} --set hostPath=${HOME}

sleep 3

echo -e "\nInstalling orchestrator layer...\n"
helm upgrade -i -f ${orchestrator_chart}/values.yaml orchestrator ${orchestrator_chart} --set dockerArtifactAccount=${DOCKERHUB_ACCOUNT}

if [ "$1" == "local" ]; then
    echo "Transferring local etcd_launch_files..."
    {
        cd ${DYNAMOS_ROOT}/configuration
        ./fill-etcd-pvc.sh
    }
fi



sleep 1

echo -e "\nInstalling agents layer...\n"
helm upgrade -i -f ${agents_chart}/values.yaml agents ${agents_chart} --set dockerArtifactAccount=${DOCKERHUB_ACCOUNT}

sleep 1

echo -e "\nInstalling thirdparty layer...\n"
helm upgrade -i -f ${ttp_chart}/values.yaml surf ${ttp_chart} --set dockerArtifactAccount=${DOCKERHUB_ACCOUNT}

sleep 1

echo -e "\nInstalling api gateway...\n"
helm upgrade -i -f ${api_gw_chart}/values.yaml api-gateway ${api_gw_chart} --set dockerArtifactAccount=${DOCKERHUB_ACCOUNT}

echo -e "\n=============== Finished setting up DYNAMOS (no monitoring) ===============\n"

exit 0

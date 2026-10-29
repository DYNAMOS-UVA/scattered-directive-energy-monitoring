#!/bin/bash

set -e

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

if [ "$#" -gt 2 ] || { [ -n "${2:-}" ] && [ "$2" != "--no-monitoring" ]; }; then
    echo ">!< ERROR: Usage: $0 <local|fabric> [--no-monitoring] >!<" >&2
    exit 1
fi

if [ ! -f "${CONFIG_PATH}/configure_dynamos.sh" ] || \
   [ ! -f "${CONFIG_PATH}/etcd_launch_files/agreements.json" ]; then
    echo ">!< ERROR: DYNAMOS_ROOT='${DYNAMOS_ROOT}' does not contain the required installation files." >&2
    echo "Expected configuration/configure_dynamos.sh and configuration/etcd_launch_files/agreements.json." >&2
    echo "Unset a stale override with 'unset DYNAMOS_ROOT' to use the checkout containing this installer," >&2
    echo "or set DYNAMOS_ROOT to a complete DYNAMOS checkout." >&2
    exit 1
fi

echo -e "\n=============== Started setting up DYNAMOS ($1) ===============\n"

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
agents=$(grep '"name":' "${etcd_launch_files}/agreements.json" | awk -F'"' '{print $4}' | paste -sd "," -)
echo -e "Agents discovered: $agents\n"

echo -e "Generating agents and third parties charts...\n"
configure_dynamos="${CONFIG_PATH}/configure_dynamos.sh"
bash "${configure_dynamos}" "$agents" "" "$1"

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
cp "${k8s_service_files}/definitions_example.json" "${rabbit_definitions_file}"


# The Rabbit Hashed password needs to be in definitions.json file, that is the configuration for RabbitMQ
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS sed
    sed -i '' "s|%PASSWORD%|${actual_hash}|g" "${rabbit_definitions_file}"
else
    # GNU sed
    sed -i "s|%PASSWORD%|${actual_hash}|g" "${rabbit_definitions_file}"
fi

echo -e "Installing namespaces...\n"
helm upgrade -i -f "${namespace_chart}/values.yaml" namespaces "${namespace_chart}" --set secret.password="${rabbit_pw}"

echo -e "\nPreparing PVC...\n"

{
    cd "${CONFIG_PATH}"
    bash ./fill-rabbit-pvc.sh "$1"
}

if [ "${2:-}" != "--no-monitoring" ]; then
    echo -e "\nPreparing energy monitoring installation...\n"
    (
        cd "${CONFIG_PATH}"
        bash ./dynamos-monitoring-configuration.sh "$1"
    )
fi

echo -e "\nInstalling NGINX...\n"
helm upgrade -i -f "${core_chart}/ingress-values.yaml" nginx oci://ghcr.io/nginxinc/charts/nginx-ingress -n ingress --version 0.18.0

echo -e "Installing DYNAMOS core...\n"
helm upgrade -i -f "${core_chart}/values.yaml" core "${core_chart}" --set hostPath="${HOME}"

sleep 3

echo -e "\nInstalling orchestrator layer...\n"
helm upgrade -i -f "${orchestrator_chart}/values.yaml" orchestrator "${orchestrator_chart}" --set dockerArtifactAccount="${DOCKERHUB_ACCOUNT}" --set-string branchNameTag="${DYNAMOS_IMAGE_TAG}"

if [ "$1" == "local" ]; then
    echo "Transferring local etcd_launch_files..."
    {
        cd "${CONFIG_PATH}"
        bash ./fill-etcd-pvc.sh
    }
fi



sleep 1

echo -e "\nInstalling agents layer...\n"
helm upgrade -i -f "${agents_chart}/values.yaml" agents "${agents_chart}" --set dockerArtifactAccount="${DOCKERHUB_ACCOUNT}" --set-string branchNameTag="${DYNAMOS_IMAGE_TAG}"

sleep 1

echo -e "\nInstalling thirdparty layer...\n"
helm upgrade -i -f "${ttp_chart}/values.yaml" surf "${ttp_chart}" --set dockerArtifactAccount="${DOCKERHUB_ACCOUNT}" --set-string branchNameTag="${DYNAMOS_IMAGE_TAG}"

sleep 1

echo -e "\nInstalling api gateway...\n"
helm upgrade -i -f "${api_gw_chart}/values.yaml" api-gateway "${api_gw_chart}" --set dockerArtifactAccount="${DOCKERHUB_ACCOUNT}" --set-string branchNameTag="${DYNAMOS_IMAGE_TAG}"

echo -e "\n=============== Finished setting up DYNAMOS ===============\n"

exit 0
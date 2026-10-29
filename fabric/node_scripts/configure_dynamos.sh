#!/bin/bash

set -e
# Importing dynamos config
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
source "${SCRIPT_DIR}/../../dynamos.conf"

{
# Parse the agents and thirdparties from the CLI arguments
IFS=',' read -r -a agents <<< "$1"
IFS=',' read -r -a thirdparties <<< "$2"

cd "${DYNAMOS_ROOT}"

echo "Adding agents..."
for agent in "${agents[@]}"
do
    echo "- agent '$agent'"
    bash ./scripts/add_agent.sh "$agent" "$3"
done

echo ""
echo "Adding third parties..."
for thirdparty in "${thirdparties[@]}"
do
    echo "- third party '$thirdparty'"
    bash ./scripts/add_thirdparty.sh "$thirdparty" "$3"
done
}
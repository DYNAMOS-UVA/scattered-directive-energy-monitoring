#!/bin/bash

set -e

if [ "$#" -ne 1 ]; then
    echo "Usage: $0 <local|fabric>" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
exec bash "${SCRIPT_DIR}/dynamos-configuration.sh" "$1" --no-monitoring

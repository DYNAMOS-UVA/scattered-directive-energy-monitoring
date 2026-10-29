#!/bin/bash

{
if [ -z "$1" ]; then
  echo "No agent name provided."
  exit 1
fi

if [ "$2" == "local" ]; then
    CHARTS_PATH="charts"
elif [ "$2" == "fabric" ]; then
    CHARTS_PATH="fabric/charts"
else
    echo ">!< ERROR: You must specify an environment argument : 'local' or 'fabric'. >!<"
    exit 1
fi

sed -e "s/^# //" -e "s/%WORKER%/$1/g" "${CHARTS_PATH}/agents/templates/workerX.yaml" > "${CHARTS_PATH}/agents/templates/${1}.yaml"

if ! grep -q "namespace: $1" ${CHARTS_PATH}/agents/templates/cluster_role.yaml; then
tee -a ${CHARTS_PATH}/agents/templates/cluster_role.yaml << END
---

apiVersion: v1
kind: ServiceAccount
metadata:
  name: job-creator-$1
  namespace: $1
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: job-creator-$1
  namespace: $1
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: job-creator
subjects:
- kind: ServiceAccount
  name: job-creator-$1
  namespace: $1
END
fi

bash ./scripts/add_namespace.sh "$1" "$2"
}

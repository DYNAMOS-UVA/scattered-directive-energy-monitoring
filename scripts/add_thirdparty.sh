#!/bin/bash

set -e

{
if [ -z "$1" ]; then
  echo "No thirdparty name provided."
  exit 1
fi

if [ "$2" == "local" ]; then
    CHARTS_PATH="charts"
elif [ "$2" == "fabric" ]; then
    CHARTS_PATH="fabric/charts"
else
    echo ">!< ERROR: You must specify an environment argument: 'local' or 'fabric'. >!<"
    exit 1
fi

if [ ! -f "${CHARTS_PATH}/thirdparty/templates/${1}.yaml" ]; then
    sed -e "s/^# //" -e "s/%THIRDPARTY%/$1/g" "${CHARTS_PATH}/thirdparty/templates/thirdpartyX.yaml" > "${CHARTS_PATH}/thirdparty/templates/${1}.yaml"
fi

if ! grep -q "namespace: $1" ${CHARTS_PATH}/thirdparty/templates/cluster_role.yaml; then

if grep -q "apiVersion:" ${CHARTS_PATH}/thirdparty/templates/cluster_role.yaml; then
tee -a ${CHARTS_PATH}/thirdparty/templates/cluster_role.yaml << END
---

END
fi

tee -a "${CHARTS_PATH}/thirdparty/templates/cluster_role.yaml" << END
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

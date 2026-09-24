#!/bin/bash

echo -e "\nStarting etcd file transfer...\n"

# Create the temporary pod that mounts the orchestrator etcd PVC.
kubectl apply -f temp-pod.yaml

# Wait for the pod to be in the 'Running' state
echo -e "\nWaiting for temp-pod-orch to be Running...\n"
kubectl wait --for=condition=Ready pod/temp-pod-orch --timeout=60s -n orchestrator

# Stage the etcd JSON configuration and eFLINT models at the PVC root.
echo -e "\nCompressing local etcd files and eFLINT models...\n"
staging_dir=$(mktemp -d)
trap 'rm -rf "$staging_dir" etcd_files.tar.gz' EXIT
cp -a ./etcd_launch_files/. "$staging_dir/"
cp -a ./eflint-models "$staging_dir/"
tar -czvf etcd_files.tar.gz -C "$staging_dir" .

# Copy the zip into the running pod's /etcd folder
echo -e "\nTransferring files into cluster...\n"
kubectl cp etcd_files.tar.gz temp-pod-orch:/mnt -n orchestrator

# Unzip the files clean up
kubectl exec -n orchestrator temp-pod-orch -- sh -c "tar -xzvf /mnt/etcd_files.tar.gz -C /mnt && rm /mnt/etcd_files.tar.gz"

# Delete the temporary pod
kubectl delete -f temp-pod.yaml --wait=false

echo -e "\nFiles have been successfully transfered!\n"
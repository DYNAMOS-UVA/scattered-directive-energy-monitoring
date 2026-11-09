#!/bin/bash
# Prepares Kubespray (release-2.27, Flannel over the FABRIC data-plane NIC) on the control VM.
set -euo pipefail

cd "$HOME"
if [ ! -d kubespray ]; then
    git clone --branch release-2.27 --depth 1 https://github.com/kubernetes-sigs/kubespray.git
    cd kubespray
    rm -rf .git
    cp -r inventory/sample inventory/dynamos

    sed -i -e "s/# flannel_interface_regexp:/flannel_interface_regexp: 'enp[5-9]s\\\\\\\\d' #/g" ./inventory/dynamos/group_vars/k8s_cluster/k8s-net-flannel.yml
    sed -i -e 's/kube_network_plugin: calico/kube_network_plugin: flannel/g' ./inventory/dynamos/group_vars/k8s_cluster/k8s-cluster.yml
    sed -i -e 's/# ingress_nginx_service_nodeport_http: 30080/ingress_nginx_service_nodeport_http: 30080/g' ./inventory/dynamos/group_vars/k8s_cluster/addons.yml
    sed -i -e 's/# ingress_nginx_service_nodeport_https: 30081/ingress_nginx_service_nodeport_https: 30443/g' ./inventory/dynamos/group_vars/k8s_cluster/addons.yml
else
    echo "Kubespray already prepared; skipping clone."
    cd kubespray
fi

if [ ! -d venv ]; then
    python3 -m venv venv
fi
source ./venv/bin/activate
pip3 install -r requirements.txt

ansible --version

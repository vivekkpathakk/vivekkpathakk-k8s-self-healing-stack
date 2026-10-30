#!/usr/bin/env bash
# Bootstrap k3s and Helm on an Ubuntu 24.04 Azure VM.
set -euo pipefail

curl -sfL https://get.k3s.io | sh -

mkdir -p "$HOME/.kube"
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
chmod 600 "$HOME/.kube/config"
grep -q 'KUBECONFIG=' "$HOME/.bashrc" || echo 'export KUBECONFIG=$HOME/.kube/config' >> "$HOME/.bashrc"
export KUBECONFIG="$HOME/.kube/config"

kubectl get nodes -o wide

if ! command -v helm >/dev/null; then
  curl -fsSL -o /tmp/get_helm.sh https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3
  chmod 700 /tmp/get_helm.sh && /tmp/get_helm.sh
fi
helm version

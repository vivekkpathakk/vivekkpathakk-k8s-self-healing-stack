#!/usr/bin/env bash
# Install Prometheus, Grafana, Alertmanager (release name: prometheus-stack).
set -euo pipefail
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"

helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm upgrade --install prometheus-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace

kubectl -n monitoring rollout status deploy/prometheus-stack-grafana --timeout=300s
kubectl get pods -n monitoring

echo "Grafana admin password:  kubectl -n monitoring get secret prometheus-stack-grafana -o jsonpath='{.data.admin-password}' | base64 -d; echo"
echo "Open Grafana via SSH tunnel: ssh -L 3000:localhost:3000 <user>@<vm-ip>, then kubectl -n monitoring port-forward svc/prometheus-stack-grafana 3000:80"

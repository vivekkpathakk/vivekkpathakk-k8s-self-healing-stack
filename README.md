# k8s-self-healing-stack

Kubernetes health checker and self-healing engine. A Go agent watches pods and nodes, deletes pods stuck in `CrashLoopBackOff`, `ErrImagePull`, `ImagePullBackOff` or `Evicted` so their controller recreates them, and exposes a dashboard and Prometheus metrics. Prometheus, Alertmanager and Grafana (kube-prometheus-stack) run on a k3s cluster on an Azure VM.

## Architecture

```text
Azure VM (Ubuntu 24.04)
 └── k3s cluster
      ├── kube-system: self-healing-agent (Go)  ── :8080  /  (dashboard)  /metrics  /healthz
      ├── monitoring:  Prometheus, Alertmanager, Grafana, node-exporter, kube-state-metrics
      └── default:     target workloads (watched namespaces)
Alerts: Prometheus rules -> Alertmanager -> Slack (webhook kept in a Kubernetes Secret)
```

## Structure

```text
.github/workflows/deploy.yml        CI: vet, test, build, govulncheck, Trivy scan
cmd/healer/main.go                  entry point, HTTP server, config from env
cmd/healer/templates/index.html     dashboard (embedded in the binary)
internal/controller/controller.go   informers, healing logic, guardrails, metrics
deployments/k8s/                    agent (RBAC, Deployment, Service, ServiceMonitor), chaos test
deployments/monitoring/             Prometheus rules, Alertmanager Helm values
scripts/                            install-k3s.sh, setup-monitoring.sh
```

## Setup

```bash
./scripts/install-k3s.sh
./scripts/setup-monitoring.sh                      # Helm release: prometheus-stack
go mod tidy                                        # first time only
go test ./...
docker build -t local-registry/k8s-self-healer:v1.0.0 .
docker save local-registry/k8s-self-healer:v1.0.0 -o self-healer.tar
sudo k3s ctr images import self-healer.tar
kubectl apply -f deployments/k8s/self-healing-agent.yaml
kubectl apply -f deployments/monitoring/prometheus-alerts.yaml
kubectl -n monitoring create secret generic slack-webhook --from-literal=url='<SLACK_WEBHOOK_URL>'
helm upgrade prometheus-stack prometheus-community/kube-prometheus-stack -n monitoring \
  --reuse-values -f deployments/monitoring/alertmanager-config.yaml
```

## Test the healing

```bash
kubectl apply -f deployments/k8s/test-failure-pod.yaml
kubectl -n kube-system logs -l app=self-healer -f
```

Expected: `[HEALER ALERT] Unhealthy pod found! ...` then `[HEALER ACTION] Successfully deleted unhealthy pod ...`. After `MAX_HEALS` deletions inside `HEAL_WINDOW` the agent stops and the `HealingLimitReached` alert fires.

## Configuration (env on the Deployment)

| Variable | Default | Meaning |
|---|---|---|
| WATCH_NAMESPACES | default | Only these namespaces are healed (match the RoleBindings) |
| DRY_RUN | false | Log and count, do not delete |
| MAX_HEALS | 5 | Max deletions per owner in the window |
| HEAL_WINDOW | 10m | Sliding window |

## Access (no public ports)

```bash
kubectl -n kube-system port-forward svc/self-healer 8080:8080
kubectl -n monitoring port-forward svc/prometheus-stack-grafana 3000:80
ssh -L 3000:localhost:3000 -L 8080:localhost:8080 <user>@<vm-ip>   # from your laptop
```

## Security notes

- Delete rights only in watched namespaces; cluster-wide access is read-only.
- Skips pods without a controller and anything outside the allowlist (kube-system, monitoring).
- Non-root, read-only filesystem, all capabilities dropped, resource limits.
- Slack webhook and Grafana password live in Secrets, never in Git. Do not commit screenshots that show IPs or credentials.
- Keep NSG ports 3000, 9090, 9093 and 6443 closed to the internet.

## Troubleshooting

| Symptom | Check |
|---|---|
| Rules not loaded | `release` label equals the Helm release name |
| No Slack message | Secret `slack-webhook` exists; `kubectl -n monitoring logs alertmanager-prometheus-stack-kube-prom-alertmanager-0` |
| Agent cannot delete | RoleBinding exists in the target namespace |
| Image not found | Re-import with `sudo k3s ctr images import` |

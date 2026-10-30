// Package controller watches pods and nodes and heals unhealthy pods.
package controller

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const maxHistory = 100

var (
	actions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "auto_healing_actions_total",
		Help: "Healing actions by action and status.",
	}, []string{"action", "status"})
	nodesNotReady = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "healer_nodes_not_ready",
		Help: "Number of nodes whose Ready condition is not True.",
	})
)

// Entry is one row of the remediation ledger shown on the dashboard.
type Entry struct {
	Timestamp string
	Pod       string
	Namespace string
	Reason    string
	Action    string
	Status    string // success, failed, skipped, dry-run, alert
}

// Config holds the safety guardrails of the healer.
type Config struct {
	Namespaces map[string]bool // allowlist; pods elsewhere are never touched
	DryRun     bool
	MaxHeals   int           // max deletions per owner inside Window
	Window     time.Duration // sliding window for MaxHeals
}

// Healer is the control loop.
type Healer struct {
	cs       kubernetes.Interface
	cfg      Config
	mu       sync.Mutex
	history  []Entry
	attempts map[string][]time.Time
	notReady map[string]bool
}

// New creates a Healer.
func New(cs kubernetes.Interface, cfg Config) *Healer {
	return &Healer{cs: cs, cfg: cfg, attempts: map[string][]time.Time{}, notReady: map[string]bool{}}
}

// History returns the ledger, newest first.
func (h *Healer) History() []Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Entry, 0, len(h.history))
	for i := len(h.history) - 1; i >= 0; i-- {
		out = append(out, h.history[i])
	}
	return out
}

// Run starts the informers and blocks until ctx is cancelled.
func (h *Healer) Run(ctx context.Context) error {
	factory := informers.NewSharedInformerFactory(h.cs, 0)
	podInf := factory.Core().V1().Pods().Informer()
	nodeInf := factory.Core().V1().Nodes().Informer()

	if _, err := podInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o interface{}) { h.onPod(ctx, o) },
		UpdateFunc: func(_, n interface{}) { h.onPod(ctx, n) },
	}); err != nil {
		return err
	}
	if _, err := nodeInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o interface{}) { h.onNode(o) },
		UpdateFunc: func(_, n interface{}) { h.onNode(n) },
	}); err != nil {
		return err
	}

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), podInf.HasSynced, nodeInf.HasSynced) {
		return fmt.Errorf("informer caches failed to sync")
	}
	log.Println("Self-Healing Agent started. Monitoring cluster pods...")
	<-ctx.Done()
	return nil
}

// unhealthyReason returns a non-empty reason when the pod needs healing.
func unhealthyReason(pod *corev1.Pod) string {
	if pod.Status.Phase == corev1.PodFailed && pod.Status.Reason == "Evicted" {
		return "Evicted"
	}
	statuses := append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...)
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	for _, s := range statuses {
		if w := s.State.Waiting; w != nil {
			switch w.Reason {
			case "CrashLoopBackOff", "ErrImagePull", "ImagePullBackOff":
				return w.Reason
			}
		}
	}
	return ""
}

func (h *Healer) onPod(ctx context.Context, obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod.DeletionTimestamp != nil || !h.cfg.Namespaces[pod.Namespace] {
		return
	}
	reason := unhealthyReason(pod)
	if reason == "" {
		return
	}
	log.Printf("[HEALER ALERT] Unhealthy pod found! Namespace: %s | Pod: %s | Reason: %s", pod.Namespace, pod.Name, reason)

	owner := metav1.GetControllerOf(pod)
	if owner == nil && reason != "Evicted" {
		h.record(pod, reason, "delete", "skipped")
		log.Printf("[HEALER SKIP] %s/%s has no controller and would not be recreated", pod.Namespace, pod.Name)
		return
	}
	key := string(pod.UID)
	if owner != nil {
		key = string(owner.UID)
	}
	if !h.allow(key) {
		h.record(pod, reason, "delete", "skipped")
		log.Printf("[HEALER ESCALATE] Heal limit reached for %s/%s; manual action needed", pod.Namespace, pod.Name)
		return
	}
	if h.cfg.DryRun {
		h.record(pod, reason, "delete", "dry-run")
		log.Printf("[HEALER DRY-RUN] Would delete pod %s/%s", pod.Namespace, pod.Name)
		return
	}
	err := h.cs.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	switch {
	case err == nil:
		h.record(pod, reason, "delete", "success")
		log.Printf("[HEALER ACTION] Successfully deleted unhealthy pod %s for auto-restart.", pod.Name)
	case apierrors.IsNotFound(err):
		// already gone, another event handled it
	default:
		h.record(pod, reason, "delete", "failed")
		log.Printf("[HEALER ERROR] Failed to delete pod %s/%s: %v", pod.Namespace, pod.Name, err)
	}
}

// allow enforces MaxHeals deletions per owner inside the sliding window.
func (h *Healer) allow(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	kept := []time.Time{}
	for _, t := range h.attempts[key] {
		if now.Sub(t) < h.cfg.Window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= h.cfg.MaxHeals {
		h.attempts[key] = kept
		return false
	}
	h.attempts[key] = append(kept, now)
	return true
}

func (h *Healer) record(pod *corev1.Pod, reason, action, status string) {
	actions.WithLabelValues(action, status).Inc()
	h.add(Entry{Pod: pod.Name, Namespace: pod.Namespace, Reason: reason, Action: action, Status: status})
}

func (h *Healer) add(e Entry) {
	e.Timestamp = time.Now().Format("2006-01-02 15:04:05")
	h.mu.Lock()
	defer h.mu.Unlock()
	h.history = append(h.history, e)
	if len(h.history) > maxHistory {
		h.history = h.history[len(h.history)-maxHistory:]
	}
}

func (h *Healer) onNode(obj interface{}) {
	n, ok := obj.(*corev1.Node)
	if !ok {
		return
	}
	notReady := true
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			notReady = c.Status != corev1.ConditionTrue
		}
	}
	h.mu.Lock()
	prev := h.notReady[n.Name]
	h.notReady[n.Name] = notReady
	count := 0
	for _, v := range h.notReady {
		if v {
			count++
		}
	}
	h.mu.Unlock()
	nodesNotReady.Set(float64(count))
	if notReady && !prev {
		log.Printf("[HEALER ALERT] Node not ready: %s", n.Name)
		actions.WithLabelValues("node-check", "alert").Inc()
		h.add(Entry{Pod: "-", Namespace: n.Name, Reason: "NodeNotReady", Action: "node-check", Status: "alert"})
	}
}

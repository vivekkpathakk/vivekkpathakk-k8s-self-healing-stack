package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func crashPod(name string, withOwner bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: "uid-" + "x"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		}}},
	}
	if withOwner {
		yes := true
		p.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "owner-1", Controller: &yes}}
	}
	return p
}

func newTestHealer(cs *fake.Clientset, max int) *Healer {
	return New(cs, Config{Namespaces: map[string]bool{"default": true}, MaxHeals: max, Window: time.Minute})
}

func exists(cs *fake.Clientset, ns, name string) bool {
	_, err := cs.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
	return !apierrors.IsNotFound(err)
}

func TestDeletesCrashLoopPodWithOwner(t *testing.T) {
	p := crashPod("p1", true)
	cs := fake.NewSimpleClientset(p)
	newTestHealer(cs, 5).onPod(context.Background(), p)
	if exists(cs, "default", "p1") {
		t.Fatal("expected pod to be deleted")
	}
}

func TestSkipsPodWithoutController(t *testing.T) {
	p := crashPod("p2", false)
	cs := fake.NewSimpleClientset(p)
	newTestHealer(cs, 5).onPod(context.Background(), p)
	if !exists(cs, "default", "p2") {
		t.Fatal("bare pod must not be deleted")
	}
}

func TestStopsAfterLimit(t *testing.T) {
	p := crashPod("p3", true)
	cs := fake.NewSimpleClientset(p)
	newTestHealer(cs, 0).onPod(context.Background(), p)
	if !exists(cs, "default", "p3") {
		t.Fatal("pod must be kept once the heal limit is reached")
	}
}

func TestIgnoresOtherNamespaces(t *testing.T) {
	p := crashPod("p4", true)
	p.Namespace = "kube-system"
	cs := fake.NewSimpleClientset(p)
	newTestHealer(cs, 5).onPod(context.Background(), p)
	if !exists(cs, "kube-system", "p4") {
		t.Fatal("kube-system pod must not be touched")
	}
}

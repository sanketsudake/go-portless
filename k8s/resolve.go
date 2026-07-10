package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// target is a resolved forwarding destination.
type target struct {
	namespace     string
	pod           string
	containerPort int
}

type resolver struct {
	client kubernetes.Interface
	opts   options
}

// resolve finds a ready pod and the container port to forward to.
func (r *resolver) resolve(ctx context.Context) (target, error) {
	switch {
	case r.opts.pod != "":
		return r.resolvePod(ctx)
	case r.opts.service != "":
		return r.resolveService(ctx)
	default:
		return r.resolveSelector(ctx, r.opts.selector, r.opts.targetPort)
	}
}

func (r *resolver) resolvePod(ctx context.Context) (target, error) {
	pod, err := r.client.CoreV1().Pods(r.opts.namespace).Get(ctx, r.opts.pod, metav1.GetOptions{})
	if err != nil {
		return target{}, err
	}
	if !podReady(pod) {
		return target{}, fmt.Errorf("pod %q not ready", pod.Name)
	}
	port, err := containerPort(pod, r.opts.targetPort)
	if err != nil {
		return target{}, err
	}
	return target{namespace: pod.Namespace, pod: pod.Name, containerPort: port}, nil
}

func (r *resolver) resolveService(ctx context.Context) (target, error) {
	svc, err := r.client.CoreV1().Services(r.opts.namespace).Get(ctx, r.opts.service, metav1.GetOptions{})
	if err != nil {
		return target{}, err
	}
	if len(svc.Spec.Selector) == 0 {
		return target{}, fmt.Errorf("service %q has no selector", svc.Name)
	}
	tp := r.opts.targetPort
	if !r.opts.hasTarget {
		if len(svc.Spec.Ports) != 1 {
			return target{}, fmt.Errorf("service %q has %d ports; specify TargetPort", svc.Name, len(svc.Spec.Ports))
		}
		tp = svc.Spec.Ports[0].TargetPort
	}
	return r.resolveSelector(ctx, metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: svc.Spec.Selector}), tp)
}

func (r *resolver) resolveSelector(ctx context.Context, selector string, tp intstr.IntOrString) (target, error) {
	if selector == "" {
		return target{}, fmt.Errorf("empty label selector")
	}
	pods, err := r.client.CoreV1().Pods(r.opts.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return target{}, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !podReady(pod) {
			continue
		}
		port, err := containerPort(pod, tp)
		if err != nil {
			return target{}, err
		}
		return target{namespace: pod.Namespace, pod: pod.Name, containerPort: port}, nil
	}
	return target{}, fmt.Errorf("no ready pod for selector %q in %q", selector, r.opts.namespace)
}

// podReady reports whether pod is Running with a true Ready condition.
func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// containerPort resolves tp against pod: an integer port is used directly, a
// named port is looked up among the pod's container ports.
func containerPort(pod *corev1.Pod, tp intstr.IntOrString) (int, error) {
	if tp.Type == intstr.Int {
		if tp.IntValue() == 0 {
			return 0, fmt.Errorf("target port is unset for pod %q", pod.Name)
		}
		return tp.IntValue(), nil
	}
	name := tp.StrVal
	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			if p.Name == name {
				return int(p.ContainerPort), nil
			}
		}
	}
	return 0, fmt.Errorf("named port %q not found in pod %q", name, pod.Name)
}

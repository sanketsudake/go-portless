package k8s

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// ErrTargetNotFound reports that the forwarding target does not exist at all:
// the Service or pod is absent, or no pod matches the selector. It is still
// delivered as a Retryable error — a target created later self-heals — but
// callers with skip-fast semantics ("this optional subsystem isn't
// installed") can detect it with errors.Is on the dial error instead of
// burning the full ready timeout:
//
//	if errors.Is(err, k8s.ErrTargetNotFound) { t.Skip(...) }
//
// A target that exists but is not ready yet (pod warming up) is NOT
// ErrTargetNotFound.
var ErrTargetNotFound = errors.New("k8s: target not found")

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
		return r.resolveSelector(ctx, r.opts.selector, r.opts.targetPort, !r.opts.hasTarget)
	}
}

func (r *resolver) resolvePod(ctx context.Context) (target, error) {
	pod, err := r.client.CoreV1().Pods(r.opts.namespace).Get(ctx, r.opts.pod, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return target{}, fmt.Errorf("%w: pod %s/%s: %w", ErrTargetNotFound, r.opts.namespace, r.opts.pod, err)
		}
		return target{}, err
	}
	if !podReady(pod) {
		return target{}, fmt.Errorf("pod %q not ready", pod.Name)
	}
	port, err := containerPort(pod, r.opts.targetPort, !r.opts.hasTarget)
	if err != nil {
		return target{}, err
	}
	return target{namespace: pod.Namespace, pod: pod.Name, containerPort: port}, nil
}

func (r *resolver) resolveService(ctx context.Context) (target, error) {
	svc, err := r.client.CoreV1().Services(r.opts.namespace).Get(ctx, r.opts.service, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return target{}, fmt.Errorf("%w: service %s/%s: %w", ErrTargetNotFound, r.opts.namespace, r.opts.service, err)
		}
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
		// Kubernetes semantics: an unset targetPort defaults to the port.
		// Both the zero-Int and empty-String forms mean unset (objects not
		// run through API-server defaulting can carry either).
		if (tp.Type == intstr.Int && tp.IntValue() == 0) ||
			(tp.Type == intstr.String && tp.StrVal == "") {
			tp = intstr.FromInt32(svc.Spec.Ports[0].Port)
		}
	}
	// infer=false: the Service path resolved tp above; pod-port inference
	// must never kick in for Services.
	return r.resolveSelector(ctx, metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: svc.Spec.Selector}), tp, false)
}

func (r *resolver) resolveSelector(ctx context.Context, selector string, tp intstr.IntOrString, infer bool) (target, error) {
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
		port, err := containerPort(pod, tp, infer)
		if err != nil {
			return target{}, err
		}
		return target{namespace: pod.Namespace, pod: pod.Name, containerPort: port}, nil
	}
	if len(pods.Items) == 0 {
		// No pod matches at all: the target was never created (vs. warming).
		return target{}, fmt.Errorf("%w: no pod matches selector %q in %q", ErrTargetNotFound, selector, r.opts.namespace)
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

// containerPort resolves tp against pod: an integer port is used directly
// and a named port is looked up among the pod's container ports. When tp was
// never set by the caller (infer), the pod's single declared container port
// is used, mirroring the Service single-port rule.
func containerPort(pod *corev1.Pod, tp intstr.IntOrString, infer bool) (int, error) {
	// Both the zero-Int and empty-String forms mean unset, mirroring the
	// Service-path defaulting — an empty named port must not fall through to
	// a confusing `named port "" not found` lookup.
	if (tp.Type == intstr.Int && tp.IntValue() == 0) ||
		(tp.Type == intstr.String && tp.StrVal == "") {
		if infer {
			return inferredContainerPort(pod)
		}
		return 0, fmt.Errorf("target port is unset for pod %q", pod.Name)
	}
	if tp.Type == intstr.Int {
		return tp.IntValue(), nil
	}
	name := tp.StrVal
	for _, c := range servingContainers(pod) {
		for _, p := range c.Ports {
			if p.Name == name {
				return int(p.ContainerPort), nil
			}
		}
	}
	return 0, fmt.Errorf("named port %q not found in pod %q", name, pod.Name)
}

// inferredContainerPort applies the single-port rule to a pod's declared
// container ports, for Pod/LabelSelector targets with no TargetPort option.
// Declared ports are informational in Kubernetes, so inference can only be
// as right as the pod spec: a pod whose only declared port belongs to a
// sidecar infers the sidecar's port — set TargetPort explicitly for such
// pods (see the TargetPort doc).
func inferredContainerPort(pod *corev1.Pod) (int, error) {
	var ports []int32
	for _, c := range servingContainers(pod) {
		for _, p := range c.Ports {
			ports = append(ports, p.ContainerPort)
		}
	}
	if len(ports) == 1 {
		return int(ports[0]), nil
	}
	return 0, fmt.Errorf("pod %q declares %d container ports; specify TargetPort", pod.Name, len(ports))
}

// servingContainers returns the containers that can serve traffic: regular
// containers plus native sidecars (restartable init containers, K8s 1.28+),
// which regular-container-only scans would miss.
func servingContainers(pod *corev1.Pod) []corev1.Container {
	cs := make([]corev1.Container, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, c := range pod.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			cs = append(cs, c)
		}
	}
	return append(cs, pod.Spec.Containers...)
}

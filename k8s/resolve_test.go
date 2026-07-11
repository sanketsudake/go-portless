package k8s

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

func readyPod(name, ns string, labels map[string]string, ports ...corev1.ContainerPort) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: ports}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func notReadyPod(name, ns string, labels map[string]string) *corev1.Pod {
	p := readyPod(name, ns, labels)
	p.Status.Conditions[0].Status = corev1.ConditionFalse
	return p
}

func TestResolveSelectorPicksReadyPod(t *testing.T) {
	labels := map[string]string{"app": "router"}
	client := fake.NewSimpleClientset(
		notReadyPod("router-0", "fission", labels),
		readyPod("router-1", "fission", labels, corev1.ContainerPort{Name: "http", ContainerPort: 8888}),
	)
	r := &resolver{client: client, opts: options{namespace: "fission", selector: "app=router", targetPort: intstr.FromInt32(8888), hasTarget: true}}

	tgt, err := r.resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tgt.pod != "router-1" {
		t.Fatalf("picked %q, want the ready pod router-1", tgt.pod)
	}
	if tgt.containerPort != 8888 {
		t.Fatalf("port = %d, want 8888", tgt.containerPort)
	}
}

func TestResolveNamedTargetPort(t *testing.T) {
	labels := map[string]string{"app": "router"}
	client := fake.NewSimpleClientset(
		readyPod("router-1", "fission", labels, corev1.ContainerPort{Name: "http", ContainerPort: 8888}),
	)
	r := &resolver{client: client, opts: options{namespace: "fission", selector: "app=router", targetPort: intstr.FromString("http"), hasTarget: true}}

	tgt, err := r.resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tgt.containerPort != 8888 {
		t.Fatalf("named port resolved to %d, want 8888", tgt.containerPort)
	}
}

func TestResolveServiceDefaultsTargetPort(t *testing.T) {
	labels := map[string]string{"app": "router"}
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "router", Namespace: "fission"},
			Spec: corev1.ServiceSpec{
				Selector: labels,
				Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8888)}},
			},
		},
		readyPod("router-1", "fission", labels, corev1.ContainerPort{ContainerPort: 8888}),
	)
	r := &resolver{client: client, opts: options{namespace: "fission", service: "router"}}

	tgt, err := r.resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tgt.pod != "router-1" || tgt.containerPort != 8888 {
		t.Fatalf("resolved %+v, want router-1:8888", tgt)
	}
}

func TestResolveNoReadyPod(t *testing.T) {
	labels := map[string]string{"app": "router"}
	client := fake.NewSimpleClientset(notReadyPod("router-0", "fission", labels))
	r := &resolver{client: client, opts: options{namespace: "fission", selector: "app=router", targetPort: intstr.FromInt32(8888), hasTarget: true}}

	if _, err := r.resolve(context.Background()); err == nil {
		t.Fatal("expected error when no pod is ready")
	}
}

func TestResolveServiceMultiPortRequiresTarget(t *testing.T) {
	labels := map[string]string{"app": "router"}
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "router", Namespace: "fission"},
			Spec: corev1.ServiceSpec{
				Selector: labels,
				Ports: []corev1.ServicePort{
					{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8888)},
					{Name: "metrics", Port: 9090, TargetPort: intstr.FromInt32(9090)},
				},
			},
		},
		readyPod("router-1", "fission", labels, corev1.ContainerPort{ContainerPort: 8888}),
	)
	r := &resolver{client: client, opts: options{namespace: "fission", service: "router"}}

	if _, err := r.resolve(context.Background()); err == nil {
		t.Fatal("multi-port service without TargetPort should error")
	}
}

func TestResolveInfersSingleContainerPort(t *testing.T) {
	labels := map[string]string{"app": "web"}
	client := fake.NewSimpleClientset(
		readyPod("web-0", "default", labels, corev1.ContainerPort{ContainerPort: 8080}),
	)
	// LabelSelector with NO TargetPort option: infer the pod's single port.
	r := &resolver{client: client, opts: options{namespace: "default", selector: "app=web"}}

	tgt, err := r.resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tgt.containerPort != 8080 {
		t.Fatalf("containerPort = %d, want 8080", tgt.containerPort)
	}
}

func TestResolveInferenceAmbiguousPorts(t *testing.T) {
	labels := map[string]string{"app": "web"}
	client := fake.NewSimpleClientset(
		readyPod("web-0", "default", labels,
			corev1.ContainerPort{ContainerPort: 8080},
			corev1.ContainerPort{ContainerPort: 9090},
		),
	)
	r := &resolver{client: client, opts: options{namespace: "default", selector: "app=web"}}

	if _, err := r.resolve(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "specify TargetPort") {
		t.Fatalf("err = %v, want 'specify TargetPort' guidance", err)
	}
}

func TestResolveInferenceNoDeclaredPorts(t *testing.T) {
	labels := map[string]string{"app": "web"}
	client := fake.NewSimpleClientset(readyPod("web-0", "default", labels)) // no ports
	r := &resolver{client: client, opts: options{namespace: "default", selector: "app=web"}}

	if _, err := r.resolve(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "specify TargetPort") {
		t.Fatalf("err = %v, want 'specify TargetPort' guidance", err)
	}
}

func TestResolveExplicitZeroTargetPortStillErrors(t *testing.T) {
	labels := map[string]string{"app": "web"}
	client := fake.NewSimpleClientset(
		readyPod("web-0", "default", labels, corev1.ContainerPort{ContainerPort: 8080}),
	)
	// hasTarget=true with a zero port: explicitly wrong, must NOT infer.
	r := &resolver{client: client, opts: options{
		namespace: "default", selector: "app=web",
		targetPort: intstr.FromInt32(0), hasTarget: true,
	}}
	if _, err := r.resolve(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "target port is unset") {
		t.Fatalf("err = %v, want 'target port is unset'", err)
	}
}

func TestResolveServiceUnsetTargetPortDefaultsToPort(t *testing.T) {
	labels := map[string]string{"app": "web"}
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec: corev1.ServiceSpec{
				Selector: labels,
				// No TargetPort: Kubernetes semantics default it to Port.
				Ports: []corev1.ServicePort{{Port: 8080}},
			},
		},
		readyPod("web-0", "default", labels, corev1.ContainerPort{ContainerPort: 8080}),
	)
	r := &resolver{client: client, opts: options{namespace: "default", service: "web"}}

	tgt, err := r.resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tgt.containerPort != 8080 {
		t.Fatalf("containerPort = %d, want 8080 (service Port)", tgt.containerPort)
	}
}

func TestResolveInfersNativeSidecarPort(t *testing.T) {
	labels := map[string]string{"app": "web"}
	pod := readyPod("web-0", "default", labels) // main container: no ports
	always := corev1.ContainerRestartPolicyAlways
	pod.Spec.InitContainers = []corev1.Container{{
		Name:          "sidecar",
		RestartPolicy: &always,
		Ports:         []corev1.ContainerPort{{ContainerPort: 7070}},
	}}
	client := fake.NewSimpleClientset(pod)
	r := &resolver{client: client, opts: options{namespace: "default", selector: "app=web"}}

	tgt, err := r.resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tgt.containerPort != 7070 {
		t.Fatalf("containerPort = %d, want 7070 (native sidecar)", tgt.containerPort)
	}
}

func TestResolveServiceEmptyStringTargetPortDefaultsToPort(t *testing.T) {
	labels := map[string]string{"app": "web"}
	client := fake.NewSimpleClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec: corev1.ServiceSpec{
				Selector: labels,
				// The string-empty unset form (objects not run through
				// API-server defaulting): must also default to Port.
				Ports: []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromString("")}},
			},
		},
		readyPod("web-0", "default", labels, corev1.ContainerPort{ContainerPort: 8080}),
	)
	r := &resolver{client: client, opts: options{namespace: "default", service: "web"}}

	tgt, err := r.resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tgt.containerPort != 8080 {
		t.Fatalf("containerPort = %d, want 8080 (service Port)", tgt.containerPort)
	}
}

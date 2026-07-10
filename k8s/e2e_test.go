//go:build kinde2e

// Package k8s e2e test. Runs only under the `kinde2e` build tag against a real
// cluster (a kind cluster in CI). It deploys nginx, forwards to it by name,
// then deletes the pod and asserts the next dial self-heals onto the
// replacement pod.
//
//	go test -tags kinde2e -run TestE2E ./k8s/...
package k8s

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	portless "github.com/sanketsudake/go-portless"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

const e2eNS = "portless-e2e"

func TestE2ESelfHealOnPodDelete(t *testing.T) {
	ctx := context.Background()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		t.Skipf("no cluster available: %v", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// namespace
	client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: e2eNS},
	}, metav1.CreateOptions{})
	t.Cleanup(func() {
		client.CoreV1().Namespaces().Delete(context.Background(), e2eNS, metav1.DeleteOptions{})
	})

	// nginx deployment + service
	labels := map[string]string{"app": "web"}
	_, err = client.AppsV1().Deployments(e2eNS).Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "nginx",
					Image: "nginx:1.27-alpine",
					Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 80}},
				}}},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CoreV1().Services(e2eNS).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(80)}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	b, err := PortForward(cfg, Service(e2eNS, "web"))
	if err != nil {
		t.Fatal(err)
	}
	reg := portless.New()
	defer reg.Close()
	if _, err := reg.Add(ctx, "web.e2e", b); err != nil {
		t.Fatal(err)
	}
	httpc := reg.HTTPClient()
	defer httpc.CloseIdleConnections()

	get := func() error {
		rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(rctx, http.MethodGet, portless.URL("web.e2e", 0, "/"), nil)
		resp, err := httpc.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != 200 {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}

	// initial reachability (blocks until the pod is ready)
	if err := get(); err != nil {
		t.Fatalf("initial GET: %v", err)
	}

	// delete the pod; the deployment replaces it
	pods, _ := client.CoreV1().Pods(e2eNS).List(ctx, metav1.ListOptions{LabelSelector: "app=web"})
	for _, p := range pods.Items {
		client.CoreV1().Pods(e2eNS).Delete(ctx, p.Name, metav1.DeleteOptions{})
	}

	// next dial must self-heal onto the replacement pod
	if err := get(); err != nil {
		t.Fatalf("GET after pod delete (self-heal) failed: %v", err)
	}
}

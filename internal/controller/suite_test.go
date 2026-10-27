package controller

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// The envtest suite runs a real kube-apiserver and etcd (no kubelet or
// built-in controllers: pods never start, so Registries stay Pending).
// make test downloads the binaries and sets KUBEBUILDER_ASSETS.

var (
	k8s    client.Client
	scheme = runtime.NewScheme()
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// Allow `go test` after `make test` has downloaded the assets once.
		if matches, _ := filepath.Glob("../../testbin/k8s/*"); len(matches) > 0 {
			os.Setenv("KUBEBUILDER_ASSETS", matches[len(matches)-1])
		} else {
			println("envtest assets missing: run `make test`")
			os.Exit(0)
		}
	}
	logf.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	_ = clientgoscheme.AddToScheme(scheme)
	_ = registryv1alpha1.AddToScheme(scheme)

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases"), filepath.Join("testdata", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		panic(err)
	}
	if err := (&RegistryReconciler{
		Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorderFor("test"),
		ProbeVersion: func(context.Context, string) (string, error) { return "v1.7.0-test", nil },
	}).SetupWithManager(mgr); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil {
			panic(err)
		}
	}()
	k8s, _ = client.New(cfg, client.Options{Scheme: scheme})

	code := m.Run()
	cancel()
	env.Stop()
	os.Exit(code)
}

// eventually polls fn until it returns true or the timeout passes.
func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func get(obj client.Object, ns, name string) bool {
	return k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, obj) == nil
}

func newNamespace(t *testing.T) string {
	t.Helper()
	ns := &corev1.Namespace{}
	ns.GenerateName = "t-"
	if err := k8s.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	return ns.Name
}

// deployment fetches the Registry's Deployment once it exists.
func deployment(t *testing.T, ns, name string) *appsv1.Deployment {
	t.Helper()
	dep := &appsv1.Deployment{}
	eventually(t, "deployment "+name, func() bool { return get(dep, ns, name) })
	return dep
}

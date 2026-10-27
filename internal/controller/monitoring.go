package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

var serviceMonitorGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor"}

// errNoCRD is returned when an optional API (Prometheus Operator, snapshot
// controller, Longhorn) is not installed in the cluster.
var errNoCRD = errors.New("CRD not installed")

// hasKind reports whether the cluster serves gvk.
func (r *RegistryReconciler) hasKind(gvk schema.GroupVersionKind) bool {
	_, err := r.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	return err == nil
}

// reconcileServiceMonitor manages the ServiceMonitor (unstructured: the
// operator does not depend on the Prometheus Operator's Go types).
func (r *RegistryReconciler) reconcileServiceMonitor(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) error {
	want := s.Metrics.Enabled && s.Metrics.ServiceMonitor.Enabled
	if !r.hasKind(serviceMonitorGVK) {
		if want {
			return fmt.Errorf("ServiceMonitor: %w (install the Prometheus Operator)", errNoCRD)
		}
		return nil
	}
	sm := &unstructured.Unstructured{}
	sm.SetGroupVersionKind(serviceMonitorGVK)
	sm.SetName(reg.Name)
	sm.SetNamespace(reg.Namespace)
	if !want {
		err := r.Get(ctx, client.ObjectKeyFromObject(sm), sm)
		if err == nil && metav1.IsControlledBy(sm, reg) {
			return client.IgnoreNotFound(r.Delete(ctx, sm))
		}
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	interval := string(s.Metrics.ServiceMonitor.Interval)
	if interval == "" {
		interval = "30s"
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sm, func() error {
		labels := labelsFor(reg)
		for k, v := range s.Metrics.ServiceMonitor.Labels {
			labels[k] = v
		}
		sm.SetLabels(labels)
		selector := map[string]interface{}{}
		for k, v := range labelsFor(reg) {
			selector[k] = v
		}
		sm.Object["spec"] = map[string]interface{}{
			"selector":  map[string]interface{}{"matchLabels": selector},
			"endpoints": []interface{}{map[string]interface{}{"port": "registry", "path": "/metrics", "interval": interval}},
		}
		return controllerutil.SetControllerReference(reg, sm, r.Scheme)
	})
	return err
}

// HealthProber reads rgstr's /healthz (replaced in tests).
type HealthProber func(ctx context.Context, url string) (Health, error)

// Health is what rgstr reports at /healthz.
type Health struct {
	Version    string `json:"version"`
	LastBackup string `json:"last_backup"` // RFC 3339, set when rgstr backs up itself
}

func httpHealth(ctx context.Context, url string) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Health{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Health{}, err
	}
	defer resp.Body.Close()
	var h Health
	err = json.NewDecoder(resp.Body).Decode(&h)
	return h, err
}

func (r *RegistryReconciler) observeHealth(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) {
	if !reg.Status.Ready {
		return
	}
	probe := r.ProbeHealth
	if probe == nil {
		probe = httpHealth
	}
	url := fmt.Sprintf("http://%s.%s.svc:%d/healthz", reg.Name, reg.Namespace, s.Port)
	h, err := probe(ctx, url)
	if err != nil {
		return
	}
	if h.Version != "" {
		reg.Status.Version = h.Version
	}
	if backupMethod(s) != methodS3 {
		return
	}
	b, err := backupSettings(s)
	if err != nil {
		return
	}
	// Overdue: the backup after the last one (or after the Registry was
	// created) should have finished an hour ago.
	since := reg.CreationTimestamp.Time
	if t, err := time.Parse(time.RFC3339, h.LastBackup); err == nil {
		lt := metav1.NewTime(t)
		reg.Status.LastBackupTime = &lt
		since = t
	}
	if due := b.schedule.Next(since).Add(time.Hour); now().After(due) {
		msg := "no backup yet"
		if reg.Status.LastBackupTime != nil {
			msg = "last backup " + reg.Status.LastBackupTime.UTC().Format(time.RFC3339)
		}
		setCondition(reg, CondBackup, metav1.ConditionFalse, "Overdue",
			msg+"; see the Backups page of the web admin or the rgstr log")
	}
}

func clearCondition(reg *registryv1alpha1.Registry, typ string) {
	apimeta.RemoveStatusCondition(&reg.Status.Conditions, typ)
}

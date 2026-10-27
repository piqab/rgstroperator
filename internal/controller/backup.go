package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

var (
	volumeSnapshotGVK = schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshot"}
	recurringJobGVK   = schema.GroupVersionKind{Group: "longhorn.io", Version: "v1beta2", Kind: "RecurringJob"}
)

const (
	methodSnapshot = "VolumeSnapshot"
	methodLonghorn = "Longhorn"
	methodS3       = "S3"

	// backupLabel marks VolumeSnapshots made by the operator; its value is
	// the Registry name.
	backupLabel = "registry.rgstr.io/backup-of"

	// finalizer removes the Longhorn RecurringJob (another namespace, so
	// it cannot be owned by the Registry) when the Registry is deleted.
	finalizer = "registry.rgstr.io/longhorn-job"

	longhornLabelPrefix = "recurring-job.longhorn.io/"
)

// now is the clock, replaceable in tests.
var now = time.Now

type backupDefaults struct {
	schedule cron.Schedule
	spec     string
	keep     int
	method   string
	task     string
	lhNS     string
}

func backupSettings(s registryv1alpha1.RegistrySpec) (backupDefaults, error) {
	b := backupDefaults{spec: s.Backup.Schedule, keep: int(s.Backup.Keep), method: s.Backup.Method,
		task: s.Backup.Longhorn.Task, lhNS: s.Backup.Longhorn.Namespace}
	if b.spec == "" {
		b.spec = "0 3 * * *"
	}
	if b.keep == 0 {
		b.keep = 7
	}
	if b.method == "" {
		b.method = methodSnapshot
	}
	if b.task == "" {
		b.task = "backup"
	}
	if b.lhNS == "" {
		b.lhNS = "longhorn-system"
	}
	sched, err := cron.ParseStandard(b.spec)
	if err != nil {
		return b, fmt.Errorf("backup.schedule %q: %w", b.spec, err)
	}
	b.schedule = sched
	return b, nil
}

// longhornJobName is the RecurringJob for a Registry: unique across
// namespaces and within the 63-character label-value limit.
func longhornJobName(reg *registryv1alpha1.Registry) string {
	name := "rgstr-" + reg.Namespace + "-" + reg.Name
	if len(name) <= 40 {
		return name
	}
	sum := sha256.Sum256([]byte(reg.Namespace + "/" + reg.Name))
	return name[:31] + "-" + hex.EncodeToString(sum[:4])
}

// reconcileBackup schedules backups. It returns how long until the next
// one is due (0: nothing scheduled by the operator).
func (r *RegistryReconciler) reconcileBackup(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec, claim string) (time.Duration, error) {
	reg.Status.NextBackupTime = nil
	switch backupMethod(s) {
	case methodSnapshot:
		return r.reconcileSnapshots(ctx, reg, s, claim)
	case methodLonghorn:
		return 0, r.reconcileLonghornJob(ctx, reg, s, claim)
	case methodS3:
		return r.reconcileS3Backup(reg, s)
	}
	reg.Status.LastBackup, reg.Status.LastBackupTime = "", nil
	return 0, nil
}

// ── S3 (made by rgstr) ───────────────────────────────────────────────────────

// reconcileS3Backup reports rgstr's own backups: rgstr runs the schedule;
// the last success comes from its /healthz (observeBackup). The returned
// delay re-reads it just after the next backup is due.
func (r *RegistryReconciler) reconcileS3Backup(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) (time.Duration, error) {
	b, err := backupSettings(s)
	if err != nil {
		return 0, err
	}
	if s.Backup.S3 == nil || s.Backup.S3.Bucket == "" || s.Backup.S3.SecretRef.Name == "" {
		return 0, fmt.Errorf("backup method S3 needs s3.bucket and s3.secretRef")
	}
	next := metav1.NewTime(b.schedule.Next(now()))
	reg.Status.NextBackupTime = &next
	target := "s3://" + s.Backup.S3.Bucket + "/" + strings.Trim(s.Backup.S3.Prefix, "/")
	reg.Status.LastBackup = strings.TrimSuffix(target, "/")
	return time.Until(next.Time) + 2*time.Minute, nil
}

// ── VolumeSnapshot ───────────────────────────────────────────────────────────

func (r *RegistryReconciler) reconcileSnapshots(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec, claim string) (time.Duration, error) {
	if !r.hasKind(volumeSnapshotGVK) {
		return 0, fmt.Errorf("VolumeSnapshot: %w (install the CSI snapshot controller)", errNoCRD)
	}
	b, err := backupSettings(s)
	if err != nil {
		return 0, err
	}
	snaps, err := r.listSnapshots(ctx, reg)
	if err != nil {
		return 0, err
	}

	// The last scheduled time not later than now; take a snapshot if the
	// newest one is older (a missed schedule is caught up once).
	t := now().UTC()
	from := reg.CreationTimestamp.Time
	if len(snaps) > 0 {
		from = snaps[0].GetCreationTimestamp().Time
	}
	due := b.schedule.Next(from)
	if !due.After(t) {
		name := fmt.Sprintf("%s-%s", reg.Name, t.Format("20060102-150405"))
		snap := &unstructured.Unstructured{}
		snap.SetGroupVersionKind(volumeSnapshotGVK)
		snap.SetName(name)
		snap.SetNamespace(reg.Namespace)
		labels := labelsFor(reg)
		labels[backupLabel] = reg.Name
		snap.SetLabels(labels)
		spec := map[string]interface{}{"source": map[string]interface{}{"persistentVolumeClaimName": claim}}
		if s.Backup.VolumeSnapshotClassName != "" {
			spec["volumeSnapshotClassName"] = s.Backup.VolumeSnapshotClassName
		}
		snap.Object["spec"] = spec
		if err := r.Create(ctx, snap); err != nil && !apierrors.IsAlreadyExists(err) {
			return 0, fmt.Errorf("create VolumeSnapshot: %w", err)
		}
		r.Recorder.Eventf(reg, corev1.EventTypeNormal, "BackupCreated", "VolumeSnapshot %s", name)
		if snaps, err = r.listSnapshots(ctx, reg); err != nil {
			return 0, err
		}
	}

	// Keep the newest b.keep.
	for _, old := range snaps[min(b.keep, len(snaps)):] {
		if err := r.Delete(ctx, &old); err != nil && !apierrors.IsNotFound(err) {
			return 0, fmt.Errorf("delete old VolumeSnapshot %s: %w", old.GetName(), err)
		}
	}
	if len(snaps) > 0 {
		newest := snaps[0]
		reg.Status.LastBackup = newest.GetName()
		ts := newest.GetCreationTimestamp()
		reg.Status.LastBackupTime = &ts
		if msg, _, _ := unstructured.NestedString(newest.Object, "status", "error", "message"); msg != "" {
			return 0, fmt.Errorf("VolumeSnapshot %s failed: %s", newest.GetName(), msg)
		}
	}
	next := b.schedule.Next(t)
	reg.Status.NextBackupTime = &metav1.Time{Time: next}
	return time.Until(next) + time.Second, nil
}

// listSnapshots returns the operator's snapshots of reg, newest first.
func (r *RegistryReconciler) listSnapshots(ctx context.Context, reg *registryv1alpha1.Registry) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(volumeSnapshotGVK.GroupVersion().WithKind("VolumeSnapshotList"))
	if err := r.List(ctx, list, client.InNamespace(reg.Namespace), client.MatchingLabels{backupLabel: reg.Name}); err != nil {
		return nil, err
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool {
		ti, tj := items[i].GetCreationTimestamp(), items[j].GetCreationTimestamp()
		if !ti.Equal(&tj) {
			return tj.Before(&ti)
		}
		return items[i].GetName() > items[j].GetName()
	})
	return items, nil
}

// ── Longhorn ─────────────────────────────────────────────────────────────────

func (r *RegistryReconciler) reconcileLonghornJob(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec, claim string) error {
	if !r.hasKind(recurringJobGVK) {
		return fmt.Errorf("Longhorn RecurringJob: %w (is Longhorn installed?)", errNoCRD)
	}
	b, err := backupSettings(s)
	if err != nil {
		return err
	}
	name := longhornJobName(reg)
	job := &unstructured.Unstructured{}
	job.SetGroupVersionKind(recurringJobGVK)
	job.SetName(name)
	job.SetNamespace(b.lhNS)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, job, func() error {
		job.SetLabels(map[string]string{
			"app.kubernetes.io/managed-by": "rgstroperator",
			"registry.rgstr.io/namespace":  reg.Namespace,
			"registry.rgstr.io/name":       reg.Name,
		})
		job.Object["spec"] = map[string]interface{}{
			"name":        name,
			"cron":        b.spec,
			"task":        b.task,
			"retain":      int64(b.keep),
			"concurrency": int64(1),
			"groups":      []interface{}{},
			"labels":      map[string]interface{}{"rgstr-registry": reg.Namespace + "." + reg.Name},
		}
		return nil
	}); err != nil {
		return fmt.Errorf("RecurringJob %s/%s: %w", b.lhNS, name, err)
	}
	// Longhorn applies jobs to a volume through labels on its PVC.
	if err := r.labelClaim(ctx, reg.Namespace, claim, map[string]string{
		longhornLabelPrefix + "source": "enabled",
		longhornLabelPrefix + name:     "enabled",
	}, nil); err != nil {
		return err
	}
	reg.Status.LastBackup = b.lhNS + "/" + name
	reg.Status.LastBackupTime = nil
	return nil
}

// backupMethod is the active backup method, "" when backups are off.
func backupMethod(s registryv1alpha1.RegistrySpec) string {
	if !s.Backup.Enabled || cluster(s) {
		return ""
	}
	if s.Backup.Method == "" {
		return methodSnapshot
	}
	return s.Backup.Method
}

// syncFinalizer runs first in a reconcile: it adds the finalizer while a
// Longhorn RecurringJob is used, and removes the job (and its PVC label)
// and the finalizer when it no longer is — backups turned off, the method
// changed, or the Registry being deleted. It reports whether it updated
// the Registry.
func (r *RegistryReconciler) syncFinalizer(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) (bool, error) {
	want := reg.DeletionTimestamp.IsZero() && backupMethod(s) == methodLonghorn
	has := controllerutil.ContainsFinalizer(reg, finalizer)
	switch {
	case want && !has:
		controllerutil.AddFinalizer(reg, finalizer)
		return true, r.Update(ctx, reg)
	case !want && has:
		if err := r.removeLonghornJob(ctx, reg, s); err != nil {
			return false, err
		}
		controllerutil.RemoveFinalizer(reg, finalizer)
		return true, r.Update(ctx, reg)
	}
	return false, nil
}

// removeLonghornJob deletes the RecurringJob and its label on the PVC.
func (r *RegistryReconciler) removeLonghornJob(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) error {
	claim := claimName(reg, s)
	b, _ := backupSettings(s)
	name := longhornJobName(reg)
	if r.hasKind(recurringJobGVK) {
		job := &unstructured.Unstructured{}
		job.SetGroupVersionKind(recurringJobGVK)
		job.SetName(name)
		job.SetNamespace(b.lhNS)
		if err := r.Delete(ctx, job); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if err := r.labelClaim(ctx, reg.Namespace, claim, nil, []string{longhornLabelPrefix + name}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// labelClaim adds and removes labels on the data PVC (also an existing
// claim the operator otherwise leaves alone).
func (r *RegistryReconciler) labelClaim(ctx context.Context, ns, claim string, add map[string]string, remove []string) error {
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: claim}, pvc); err != nil {
		return err
	}
	patch := client.MergeFrom(pvc.DeepCopy())
	if pvc.Labels == nil {
		pvc.Labels = map[string]string{}
	}
	changed := false
	for k, v := range add {
		if pvc.Labels[k] != v {
			pvc.Labels[k], changed = v, true
		}
	}
	for _, k := range remove {
		if _, ok := pvc.Labels[k]; ok {
			delete(pvc.Labels, k)
			changed = true
		}
	}
	// The source label is only needed while some job label is left.
	if !hasPrefixedLabel(pvc.Labels, longhornLabelPrefix, longhornLabelPrefix+"source") {
		if _, ok := pvc.Labels[longhornLabelPrefix+"source"]; ok {
			delete(pvc.Labels, longhornLabelPrefix+"source")
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return r.Patch(ctx, pvc, patch)
}

func hasPrefixedLabel(labels map[string]string, prefix, except string) bool {
	for k := range labels {
		if strings.HasPrefix(k, prefix) && k != except {
			return true
		}
	}
	return false
}

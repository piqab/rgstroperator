package controller

import (
	"context"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// poke changes an annotation so the Registry is reconciled now.
func poke(t *testing.T, ns, name string) {
	t.Helper()
	reg := &registryv1alpha1.Registry{}
	get(reg, ns, name)
	if reg.Annotations == nil {
		reg.Annotations = map[string]string{}
	}
	reg.Annotations["test/poke"] = strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := k8s.Update(context.Background(), reg); err != nil {
		t.Fatal(err)
	}
}

func snapshots(t *testing.T, ns string) []unstructured.Unstructured {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(volumeSnapshotGVK.GroupVersion().WithKind("VolumeSnapshotList"))
	if err := k8s.List(context.Background(), list, client.InNamespace(ns), client.MatchingLabels{backupLabel: "reg"}); err != nil {
		t.Fatal(err)
	}
	var alive []unstructured.Unstructured
	for _, s := range list.Items {
		if s.GetDeletionTimestamp() == nil {
			alive = append(alive, s)
		}
	}
	return alive
}

func TestVolumeSnapshots(t *testing.T) {
	ns := newNamespace(t)
	defer func() { now = time.Now }()
	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec: registryv1alpha1.RegistrySpec{Backup: registryv1alpha1.BackupSpec{
			Enabled: true, Schedule: "0 3 * * *", Keep: 2, VolumeSnapshotClassName: "csi-snap",
		}},
	}
	if err := k8s.Create(context.Background(), reg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "next backup in status", func() bool {
		got := &registryv1alpha1.Registry{}
		return get(got, ns, "reg") && got.Status.NextBackupTime != nil
	})
	if n := len(snapshots(t, ns)); n != 0 {
		t.Fatalf("snapshot before the schedule: %d", n)
	}

	// Each day a snapshot is due; only the newest two are kept.
	for day := 1; day <= 3; day++ {
		d := day
		now = func() time.Time { return time.Now().Add(time.Duration(d) * 25 * time.Hour) }
		poke(t, ns, "reg")
		eventually(t, "snapshot "+strconv.Itoa(day), func() bool { return len(snapshots(t, ns)) == min(day, 2) })
		time.Sleep(1100 * time.Millisecond) // distinct names (seconds) and creation times
	}
	snaps := snapshots(t, ns)
	src, _, _ := unstructured.NestedString(snaps[0].Object, "spec", "source", "persistentVolumeClaimName")
	class, _, _ := unstructured.NestedString(snaps[0].Object, "spec", "volumeSnapshotClassName")
	if src != "reg-data" || class != "csi-snap" || len(snaps[0].GetOwnerReferences()) != 0 {
		t.Errorf("snapshot: source %q class %q owners %v", src, class, snaps[0].GetOwnerReferences())
	}
	eventually(t, "lastBackup", func() bool {
		got := &registryv1alpha1.Registry{}
		return get(got, ns, "reg") && got.Status.LastBackupTime != nil && got.Status.LastBackup != ""
	})
}

func TestLonghornRecurringJob(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)
	lh := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "longhorn-system"}}
	_ = k8s.Create(ctx, lh)

	reg := &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: "reg", Namespace: ns},
		Spec: registryv1alpha1.RegistrySpec{Backup: registryv1alpha1.BackupSpec{
			Enabled: true, Method: "Longhorn", Schedule: "30 2 * * *", Keep: 5,
		}},
	}
	if err := k8s.Create(ctx, reg); err != nil {
		t.Fatal(err)
	}
	jobName := longhornJobName(reg)
	job := &unstructured.Unstructured{}
	job.SetGroupVersionKind(recurringJobGVK)
	eventually(t, "RecurringJob", func() bool { return get(job, "longhorn-system", jobName) })
	cron, _, _ := unstructured.NestedString(job.Object, "spec", "cron")
	task, _, _ := unstructured.NestedString(job.Object, "spec", "task")
	retain, _, _ := unstructured.NestedInt64(job.Object, "spec", "retain")
	if cron != "30 2 * * *" || task != "backup" || retain != 5 {
		t.Errorf("job spec: %v", job.Object["spec"])
	}
	pvc := &corev1.PersistentVolumeClaim{}
	eventually(t, "PVC labels", func() bool {
		return get(pvc, ns, "reg-data") && pvc.Labels[longhornLabelPrefix+jobName] == "enabled" &&
			pvc.Labels[longhornLabelPrefix+"source"] == "enabled"
	})
	get(reg, ns, "reg")
	if len(reg.Finalizers) != 1 {
		t.Errorf("finalizers: %v", reg.Finalizers)
	}

	// Turning backups off removes the job, the labels and the finalizer.
	reg.Spec.Backup.Enabled = false
	if err := k8s.Update(ctx, reg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "job removed", func() bool { return !get(job, "longhorn-system", jobName) })
	eventually(t, "labels removed", func() bool {
		return get(pvc, ns, "reg-data") && pvc.Labels[longhornLabelPrefix+jobName] == "" && pvc.Labels[longhornLabelPrefix+"source"] == ""
	})
	eventually(t, "finalizer removed", func() bool { return get(reg, ns, "reg") && len(reg.Finalizers) == 0 })

	// Deleting a Registry with a Longhorn job removes the job too.
	reg.Spec.Backup.Enabled = true
	if err := k8s.Update(ctx, reg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "job again", func() bool { return get(job, "longhorn-system", jobName) })
	eventually(t, "finalizer again", func() bool { return get(reg, ns, "reg") && len(reg.Finalizers) == 1 })
	if err := k8s.Delete(ctx, reg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "registry gone", func() bool { return !get(&registryv1alpha1.Registry{}, ns, "reg") })
	if get(job, "longhorn-system", jobName) {
		t.Error("RecurringJob left after deleting the Registry")
	}
}

func TestLonghornJobNameLength(t *testing.T) {
	reg := &registryv1alpha1.Registry{ObjectMeta: metav1.ObjectMeta{
		Name: "a-very-long-registry-name-for-testing", Namespace: "a-very-long-namespace-name"}}
	if n := longhornJobName(reg); len(n) > 40 {
		t.Errorf("job name too long: %q (%d)", n, len(n))
	}
}

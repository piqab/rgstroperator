package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// Keys the operator generates in "<name>-auth".
const (
	keyAuthSecret    = "RGSTR_AUTH_SECRET"
	keyAdminPassword = "RGSTR_ADMIN_PASSWORD"
)

// reconcileAuthSecret returns the Secret whose keys become rgstr environment
// variables, and a checksum of its data (a pod annotation, so changing the
// Secret restarts rgstr). Without spec.auth.secretRef the operator creates
// "<name>-auth" with a random JWT secret and a temporary admin password;
// existing values are never overwritten.
func (r *RegistryReconciler) reconcileAuthSecret(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) (name, checksum string, err error) {
	if !s.Auth.Enabled {
		return "", "", nil
	}
	if s.Auth.SecretRef != nil {
		name = s.Auth.SecretRef.Name
		sec := &corev1.Secret{}
		if err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: reg.Namespace}, sec); err != nil {
			if apierrors.IsNotFound(err) {
				return name, "", fmt.Errorf("secret %q not found", name)
			}
			return name, "", err
		}
		return name, dataChecksum(sec.Data), nil
	}

	name = reg.Name + authSecretSuffix
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: reg.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, sec, func() error {
		if sec.Labels == nil {
			sec.Labels = map[string]string{}
		}
		for k, v := range labelsFor(reg) {
			sec.Labels[k] = v
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		if len(sec.Data[keyAuthSecret]) == 0 {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			sec.Data[keyAuthSecret] = []byte(hex.EncodeToString(b))
		}
		if len(sec.Data[keyAdminPassword]) == 0 {
			pw, err := randomPassword(16)
			if err != nil {
				return err
			}
			sec.Data[keyAdminPassword] = []byte(pw)
		}
		// Kept with the data volume: a new secret would end every session
		// and reset the admin password of a recreated Registry.
		if *s.Storage.RetainOnDelete {
			sec.OwnerReferences = removeOwner(sec.OwnerReferences, reg.UID)
			return nil
		}
		return controllerutil.SetControllerReference(reg, sec, r.Scheme)
	})
	return name, dataChecksum(sec.Data), err
}

func dataChecksum(data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%x;", k, data[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func randomPassword(n int) (string, error) {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	out := make([]byte, n)
	for i := range out {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		out[i] = chars[k.Int64()]
	}
	return string(out), nil
}

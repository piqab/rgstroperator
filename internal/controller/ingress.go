package controller

import (
	"context"
	"fmt"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	registryv1alpha1 "github.com/piqab/rgstroperator/api/v1alpha1"
)

// nginxAnnotations let ingress-nginx pass registry traffic: layers are
// large (no body size limit, no request buffering) and slow to upload.
var nginxAnnotations = map[string]string{
	"nginx.ingress.kubernetes.io/proxy-body-size":         "0",
	"nginx.ingress.kubernetes.io/proxy-request-buffering": "off",
	"nginx.ingress.kubernetes.io/proxy-read-timeout":      "900",
	"nginx.ingress.kubernetes.io/proxy-send-timeout":      "900",
}

// publicPaths are the rgstr paths the Ingress publishes. /metrics (no
// authentication) stays inside the cluster.
var publicPaths = []string{"/v2", "/ui", "/stats", "/healthz", "/favicon.ico"}

func tlsEnabled(s registryv1alpha1.RegistrySpec) bool {
	return s.Ingress.TLS.Enabled == nil || *s.Ingress.TLS.Enabled
}

func tlsSecretName(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) string {
	if s.Ingress.TLS.SecretName != "" {
		return s.Ingress.TLS.SecretName
	}
	return reg.Name + "-tls"
}

// externalURL is where clients reach the registry.
func externalURL(reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) string {
	if s.Ingress.Enabled && s.Ingress.Host != "" {
		if tlsEnabled(s) {
			return "https://" + s.Ingress.Host
		}
		return "http://" + s.Ingress.Host
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", reg.Name, reg.Namespace, s.Port)
}

// reconcileIngress creates, updates or (when disabled) deletes the Ingress.
func (r *RegistryReconciler) reconcileIngress(ctx context.Context, reg *registryv1alpha1.Registry, s registryv1alpha1.RegistrySpec) (*networkingv1.Ingress, error) {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: reg.Name, Namespace: reg.Namespace}}
	if !s.Ingress.Enabled {
		err := r.Get(ctx, client.ObjectKeyFromObject(ing), ing)
		if err == nil && metav1.IsControlledBy(ing, reg) {
			return nil, client.IgnoreNotFound(r.Delete(ctx, ing))
		}
		if apierrors.IsNotFound(err) {
			err = nil
		}
		return nil, err
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ing, func() error {
		ann := map[string]string{}
		class := ""
		if s.Ingress.ClassName != nil {
			class = *s.Ingress.ClassName
		}
		if strings.Contains(class, "nginx") {
			for k, v := range nginxAnnotations {
				ann[k] = v
			}
		}
		if tlsEnabled(s) {
			switch {
			case s.Ingress.TLS.ClusterIssuer != "":
				ann["cert-manager.io/cluster-issuer"] = s.Ingress.TLS.ClusterIssuer
			case s.Ingress.TLS.Issuer != "":
				ann["cert-manager.io/issuer"] = s.Ingress.TLS.Issuer
			}
		}
		for k, v := range s.Ingress.Annotations {
			ann[k] = v
		}
		ing.Annotations = ann
		ing.Labels = labelsFor(reg)

		ing.Spec.IngressClassName = s.Ingress.ClassName
		var paths []networkingv1.HTTPIngressPath
		for _, p := range publicPaths {
			pathType := networkingv1.PathTypePrefix
			if p == "/favicon.ico" {
				pathType = networkingv1.PathTypeExact
			}
			paths = append(paths, networkingv1.HTTPIngressPath{
				Path: p, PathType: &pathType,
				Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
					Name: reg.Name, Port: networkingv1.ServiceBackendPort{Name: "registry"},
				}},
			})
		}
		ing.Spec.Rules = []networkingv1.IngressRule{{
			Host:             s.Ingress.Host,
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths}},
		}}
		ing.Spec.TLS = nil
		if tlsEnabled(s) {
			ing.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{s.Ingress.Host}, SecretName: tlsSecretName(reg, s)}}
		}
		return controllerutil.SetControllerReference(reg, ing, r.Scheme)
	})
	return ing, err
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeployParameterizedOIDCServer deploys an in-cluster mock OIDC server that
// issues RSA-signed JWTs with a caller-controlled subject claim (via
// POST /token?subject=<name>). The server is exposed via a NodePort so
// the test process (running outside the cluster) can reach it.
//
// Returns the in-cluster issuer URL (http://<name>.<namespace>.svc.cluster.local)
// and a cleanup function that removes all created resources.
func DeployParameterizedOIDCServer(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	timeout, pollingInterval time.Duration,
) (issuerURL string, allocatedNodePort int32, cleanup func()) {
	configMapName := name + "-code"

	// Patch the placeholder issuer into the script so the JWT iss claim and
	// the OIDC discovery document match the in-cluster service URL.
	issuerURL = fmt.Sprintf("http://%s.%s.svc.cluster.local", name, namespace)
	script := strings.ReplaceAll(parameterizedOIDCServerScript,
		"http://OIDC_SERVICE_NAME.OIDC_NAMESPACE.svc.cluster.local", issuerURL)

	ginkgo.By("Creating ConfigMap with parameterized OIDC server code")
	gomega.Expect(c.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: namespace},
		Data:       map[string]string{"server.py": script},
	})).To(gomega.Succeed())

	ginkgo.By("Creating parameterized OIDC server pod")
	gomega.Expect(c.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": name},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:    "oidc",
				Image:   "python:3.11-slim",
				Command: []string{"sh", "-c", "pip install --no-cache-dir cryptography && python3 /app/server.py"},
				Ports:   []corev1.ContainerPort{{ContainerPort: 8080}},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/.well-known/openid-configuration",
							Port: intstr.FromInt(8080),
						},
					},
					InitialDelaySeconds: 5,
					PeriodSeconds:       2,
					FailureThreshold:    30,
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "code", MountPath: "/app"}},
			}},
			Volumes: []corev1.Volume{{
				Name: "code",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
						DefaultMode:          ptr.To(int32(0755)),
					},
				},
			}},
		},
	})).To(gomega.Succeed())

	ginkgo.By("Creating parameterized OIDC server service with auto-assigned NodePort")
	oidcSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: map[string]string{"app": name},
			Ports: []corev1.ServicePort{{
				Port:       80,
				TargetPort: intstr.FromInt(8080),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	gomega.Expect(c.Create(ctx, oidcSvc)).To(gomega.Succeed())

	// Read back the auto-assigned NodePort
	gomega.Expect(c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, oidcSvc)).To(gomega.Succeed())
	allocatedNodePort = oidcSvc.Spec.Ports[0].NodePort
	gomega.Expect(allocatedNodePort).NotTo(gomega.BeZero(), "Kubernetes should auto-assign a NodePort")

	ginkgo.By("Waiting for parameterized OIDC server to be ready")
	gomega.Eventually(func() bool {
		pod := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, pod); err != nil {
			return false
		}
		if pod.Status.Phase != corev1.PodRunning {
			return false
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, timeout, pollingInterval).Should(gomega.BeTrue(), "parameterized OIDC server should be ready")

	cleanup = func() {
		_ = c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = c.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
		_ = c.Delete(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: namespace}})
		// Wait for the Pod and Service to be fully removed so their NodePort
		// and name can be reused immediately in a subsequent test run.
		gomega.Eventually(func() bool {
			pod := &corev1.Pod{}
			svc := &corev1.Service{}
			podGone := apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, pod))
			svcGone := apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, svc))
			return podGone && svcGone
		}, timeout, pollingInterval).Should(gomega.BeTrue(), "OIDC server pod and service should be fully deleted")
	}
	return issuerURL, allocatedNodePort, cleanup
}

// parameterizedOIDCServerScript is a minimal Python OIDC server that issues
// RSA-signed RS256 JWTs with a caller-controlled subject.
//
// Usage: POST /token?subject=alice  → returns {"access_token": "<jwt>", ...}
// The subject defaults to "test-user" when the query parameter is omitted.
const parameterizedOIDCServerScript = `
import base64, json, time, http.server, socketserver
from urllib.parse import urlparse, parse_qs
from cryptography.hazmat.primitives.asymmetric import rsa, padding as asym_padding
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.backends import default_backend

private_key = rsa.generate_private_key(public_exponent=65537, key_size=2048, backend=default_backend())
public_key = private_key.public_key()
pub_numbers = public_key.public_numbers()

def to_b64url(num):
    b = num.to_bytes((num.bit_length() + 7) // 8, byteorder="big")
    return base64.urlsafe_b64encode(b).decode().rstrip("=")

n_b64 = to_b64url(pub_numbers.n)
e_b64 = to_b64url(pub_numbers.e)
ISSUER = "http://OIDC_SERVICE_NAME.OIDC_NAMESPACE.svc.cluster.local"

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/.well-known/openid-configuration":
            self._json({"issuer": ISSUER, "authorization_endpoint": ISSUER+"/auth",
                "token_endpoint": ISSUER+"/token", "jwks_uri": ISSUER+"/jwks",
                "response_types_supported": ["code"], "subject_types_supported": ["public"],
                "id_token_signing_alg_values_supported": ["RS256"]})
        elif self.path == "/jwks":
            self._json({"keys": [{"kty": "RSA", "use": "sig", "kid": "k1", "alg": "RS256", "n": n_b64, "e": e_b64}]})
        else:
            self.send_response(404); self.end_headers()
    def do_POST(self):
        if self.path.startswith("/token"):
            params = parse_qs(urlparse(self.path).query)
            sub = params.get("subject", ["test-user"])[0]
            hdr = {"alg": "RS256", "typ": "JWT", "kid": "k1"}
            pay = {"sub": sub, "iss": ISSUER, "aud": "vmcp-audience", "exp": int(time.time())+3600, "iat": int(time.time())}
            def enc(d): return base64.urlsafe_b64encode(json.dumps(d, separators=(",",":")).encode()).decode().rstrip("=")
            h64, p64 = enc(hdr), enc(pay)
            sig = private_key.sign((h64+"."+p64).encode(), asym_padding.PKCS1v15(), hashes.SHA256())
            jwt = h64 + "." + p64 + "." + base64.urlsafe_b64encode(sig).decode().rstrip("=")
            print(f"Issued JWT for sub={sub}", flush=True)
            self._json({"access_token": jwt, "token_type": "Bearer", "expires_in": 3600})
        else:
            self.send_response(404); self.end_headers()
    def _json(self, obj):
        body = json.dumps(obj).encode()
        self.send_response(200); self.send_header("Content-Type","application/json"); self.end_headers(); self.wfile.write(body)
    def log_message(self, f, *a): pass

with socketserver.TCPServer(("", 8080), H) as s:
    print("OIDC server ready on 8080", flush=True)
    s.serve_forever()
`

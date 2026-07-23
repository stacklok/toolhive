// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package untrusted_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/redis/go-redis/v9"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted"
)

// Integration tests for the untrusted PodLifecycle against a real apiserver
// (envtest). These cover the paths the fake client cannot: ownerRef cascade
// delete, real AlreadyExists semantics, and status-patching for WaitReady.

var (
	intCfg    *rest.Config
	intClient client.Client
	intEnv    *envtest.Environment
	intMR     *miniredis.Miniredis
)

// newIntLifecycle builds a PodLifecycle against the shared envtest apiserver
// and a per-suite miniredis.
func newIntLifecycle() untrusted.PodLifecycle {
	return newIntLifecycleWithQuota(10)
}

func newIntLifecycleWithQuota(quota int) untrusted.PodLifecycle {
	rc := redis.NewClient(&redis.Options{Addr: intMR.Addr()})
	lc, err := untrusted.NewK8sPodLifecycleFromParts(
		intClient, rc, "thv:vmcp:session:", intNamespace, "vmcp-int", 30*time.Minute, 2*time.Minute, quota)
	Expect(err).NotTo(HaveOccurred())
	return lc
}

func TestUntrustedLifecycleIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Untrusted PodLifecycle Integration Suite")
}

var _ = BeforeSuite(func() {
	intEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "..", "deploy", "charts", "operator-crds", "files", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	var err error
	intCfg, err = intEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	scheme := runtime.NewScheme()
	Expect(corev1.AddToScheme(scheme)).To(Succeed())
	Expect(appsv1.AddToScheme(scheme)).To(Succeed())

	intClient, err = client.New(intCfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "untrusted-int"}}
	Expect(intClient.Create(context.Background(), ns)).To(Succeed())

	intMR = miniredis.RunT(GinkgoT())
})

var _ = AfterSuite(func() {
	Expect(intEnv.Stop()).To(Succeed())
})

const (
	intNamespace    = "untrusted-int"
	intMCPServerUID = "uid-int-1234567890"
)

func intRequest(sessionID string) untrusted.EnsurePodRequest {
	return untrusted.EnsurePodRequest{
		Session: untrusted.SessionRef{
			SessionID: sessionID,
			Issuer:    "https://issuer.example.com",
			Subject:   "user-int",
			Namespace: intNamespace,
		},
		MCPServerUID:  intMCPServerUID,
		MCPServerName: "int-mcp",
		Port:          8080,
		OwnerRefUID:   types.UID(intMCPServerUID),
	}
}

func intSTS() *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "int-backend-sts",
			Namespace: intNamespace,
			Labels: map[string]string{
				untrusted.LabelMCPServerUID: intMCPServerUID,
				"toolhive":                  "true",
				untrusted.LabelApp:          "int-backend",
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"x": "y"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"x": "y"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "mcp",
						Image: "ghcr.io/example/backend:int",
						Env:   []corev1.EnvVar{{Name: "SENTINEL", Value: "literal"}},
					}},
				},
			},
		},
	}
}

var _ = Describe("PodLifecycle (envtest)", func() {
	ctx := context.Background()

	It("creates a pod from the STS template, idempotently", func() {
		Expect(intClient.Create(ctx, intSTS())).To(Succeed())

		lifecycle := newIntLifecycle()
		req := intRequest("sess-int-1")

		pod, err := lifecycle.EnsurePod(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(pod.Name)).To(BeNumerically("<=", 63))
		Expect(pod.Labels[untrusted.LabelUntrusted]).To(Equal("true"))
		Expect(pod.Spec.Containers[0].Image).To(Equal("ghcr.io/example/backend:int"))

		userKey, err := binding.Format(req.Session.Issuer, req.Session.Subject)
		Expect(err).NotTo(HaveOccurred())
		Expect(pod.Name).To(Equal(untrusted.PodNameFor(intMCPServerUID, userKey, "sess-int-1")))

		// OwnerRef: non-controller, pointing at the MCPServer identity.
		Expect(pod.OwnerReferences).To(HaveLen(1))
		Expect(pod.OwnerReferences[0].Kind).To(Equal("MCPServer"))
		Expect(pod.OwnerReferences[0].Controller).To(BeNil())

		// Idempotency: second EnsurePod returns the same live pod (AlreadyExists
		// treated as success at the apiserver level).
		again, err := lifecycle.EnsurePod(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.UID).To(Equal(pod.UID))
	})

	It("delete is idempotent (NotFound swallowed)", func() {
		lifecycle := newIntLifecycle()
		req := intRequest("sess-int-2")
		pod, err := lifecycle.EnsurePod(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(lifecycle.DeletePod(ctx, pod.Name)).To(Succeed())
		Expect(lifecycle.DeletePod(ctx, pod.Name)).To(Succeed(), "second delete must be a no-op")

		gone := &corev1.Pod{}
		err = intClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: intNamespace}, gone)
		Expect(err).To(HaveOccurred())
	})

	It("WaitReady succeeds on a status-patched ready pod", func() {
		lifecycle := newIntLifecycle()
		req := intRequest("sess-int-3")
		pod, err := lifecycle.EnsurePod(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		// envtest has no kubelet; patch status to simulate scheduling+ready.
		latest := &corev1.Pod{}
		Expect(intClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: intNamespace}, latest)).To(Succeed())
		latest.Status.PodIP = "10.9.9.9"
		latest.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		Expect(intClient.Status().Update(ctx, latest)).To(Succeed())

		Expect(lifecycle.WaitReady(ctx, pod, 10*time.Second)).To(Succeed())
	})

	It("two racing EnsurePod calls create exactly one pod and take exactly one quota slot", func() {
		lifecycle := newIntLifecycleWithQuota(10)
		req := intRequest("sess-int-race")

		// Two independent lifecycle instances (two vMCP replicas) race the same
		// (user, session, server) tuple: only the deterministic pod name
		// coordinates them (real apiserver AlreadyExists semantics — the fake
		// client cannot reproduce this).
		other := newIntLifecycleWithQuota(10)

		const racers = 2
		results := make(chan error, racers)
		for _, lc := range []untrusted.PodLifecycle{lifecycle, other} {
			go func(lc untrusted.PodLifecycle) {
				_, err := lc.EnsurePod(ctx, req)
				results <- err
			}(lc)
		}
		for range racers {
			Expect(<-results).NotTo(HaveOccurred())
		}

		// Exactly one pod exists for the tuple.
		pods := &corev1.PodList{}
		Expect(intClient.List(ctx, pods,
			client.InNamespace(intNamespace),
			client.MatchingLabels{untrusted.LabelUntrusted: "true", untrusted.LabelMCPServerUID: intMCPServerUID},
		)).To(Succeed())
		Expect(pods.Items).To(HaveLen(1))
		Expect(pods.Items[0].Annotations[untrusted.AnnotationSessionID]).To(Equal("sess-int-race"))

		// Exactly one quota slot was taken: the loser adopted, it did not
		// double-count.
		userKey, err := binding.Format(req.Session.Issuer, req.Session.Subject)
		Expect(err).NotTo(HaveOccurred())
		rc := redis.NewClient(&redis.Options{Addr: intMR.Addr()})
		sum := sha256.Sum256([]byte(userKey))
		quotaKey := "thv:vmcp:session:untrusted:userquota:" + hex.EncodeToString(sum[:])[:40]
		Expect(rc.Get(ctx, quotaKey).Val()).To(Equal("1"))
	})

	It("fail-closed: hand-built secret-backed template is rejected, no pod created", func() {
		sts := intSTS()
		sts.Name = "int-backend-sts-secret"
		sts.Labels[untrusted.LabelMCPServerUID] = "uid-secret"
		sts.Spec.Template.Spec.Volumes = append(sts.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "creds",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "backend-creds"},
			},
		})
		Expect(intClient.Create(ctx, sts)).To(Succeed())

		lifecycle := newIntLifecycle()
		req := intRequest("sess-int-4")
		req.MCPServerUID = "uid-secret"
		req.OwnerRefUID = types.UID("uid-secret")

		_, err := lifecycle.EnsurePod(ctx, req)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("untrusted pod clone"))

		// Nothing may have been created.
		podName := untrusted.PodNameFor("uid-secret", req.Session.UserKey(), "sess-int-4")
		gone := &corev1.Pod{}
		getErr := intClient.Get(ctx, types.NamespacedName{Name: podName, Namespace: intNamespace}, gone)
		Expect(getErr).To(HaveOccurred())
	})
})

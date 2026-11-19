// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package entraobo runs the Entra on-behalf-of (OBO) backend auth path end to
// end against a REAL Microsoft Entra ID tenant on a local Kind cluster.
//
// The suite is opt-in. It skips unless every required TOOLHIVE_E2E_ENTRA_* env
// var is set (see README.md in this directory), and it carries the
// "entra-live" label, which the default operator e2e task excludes. Run it with
// `task thv-operator-e2e-test-entra`.
package entraobo

import (
	"context"
	"os"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"go.uber.org/zap/zapcore"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// entraLiveLabel selects this suite. The default operator e2e task filters it
// out so a misconfigured runner can never reach a real tenant.
const entraLiveLabel = "entra-live"

var (
	ctx        context.Context
	cancel     context.CancelFunc
	k8sClient  client.Client
	kubeconfig string

	// cfg is the tenant configuration, read once from the environment.
	cfg entraConfig
	// user is the end user's token for the middle-tier app and its claims.
	user userIdentity
)

func TestEntraOBO(t *testing.T) {
	t.Parallel()
	gomega.RegisterFailHandler(ginkgo.Fail)

	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	reporterConfig.Verbose = true

	ginkgo.RunSpecs(t, "Entra OBO live-tenant E2E Suite", suiteConfig, reporterConfig)
}

var _ = ginkgo.BeforeSuite(func() {
	var missing []string
	cfg, missing = loadEntraConfig(os.Getenv)
	if len(missing) > 0 {
		ginkgo.Skip("Entra OBO live-tenant suite skipped: set " + joinNames(missing) +
			" to run it (see test/e2e/thv-operator/entraobo/README.md)")
	}

	logf.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true), zap.Level(zapcore.InfoLevel)))
	ctx, cancel = context.WithCancel(context.Background())

	kubeconfig = os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		kubeconfig = homeDir + "/.kube/config"
	}
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "loading kubeconfig %s", kubeconfig)

	for _, add := range []func(*runtime.Scheme) error{
		mcpv1alpha1.AddToScheme, mcpv1beta1.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme,
	} {
		gomega.Expect(add(scheme.Scheme)).To(gomega.Succeed())
	}
	k8sClient, err = client.New(restCfg, client.Options{Scheme: scheme.Scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	createNamespace()

	ginkgo.By("acquiring the test user's token for the middle-tier app")
	user = acquireUserIdentity(ctx, cfg)
	ginkgo.GinkgoWriter.Printf("user token acquired from %s (fingerprint %s)\n",
		user.source, fingerprint(user.token))
})

var _ = ginkgo.AfterSuite(func() {
	if k8sClient != nil {
		ginkgo.By("deleting the suite namespace")
		_ = k8sClient.Delete(ctx, &corev1.Namespace{ // best effort; Kind is disposable
			ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
		})
	}
	if cancel != nil {
		cancel()
	}
})

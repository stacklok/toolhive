// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package testutil provides shared envtest bootstrap helpers for the operator
// integration suites under cmd/thv-operator/test-integration. Each suite
// delegates its BeforeSuite/AfterSuite plumbing here and keeps only its
// per-suite controller registrations.
package testutil

import (
	"context"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive,staticcheck // dot-import is the Ginkgo convention
	. "github.com/onsi/gomega"    //nolint:revive,staticcheck // dot-import is the Gomega convention
	"go.uber.org/zap/zapcore"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

const (
	// managerGracefulShutdownTimeout bounds how long the manager waits for its
	// runnables (controllers and the informer cache) to return once the suite
	// context is cancelled. It is deliberately well under envtest's 20s
	// ControlPlaneStopTimeout so a wedged runnable surfaces as a manager
	// shutdown timeout rather than as a kube-apiserver teardown failure.
	managerGracefulShutdownTimeout = 10 * time.Second

	// managerStopTimeout bounds how long Stop waits for Manager.Start to return.
	// It allows a little slack over managerGracefulShutdownTimeout so the
	// manager's own bounded shutdown always wins the race.
	managerStopTimeout = 15 * time.Second
)

// SuiteOptions configures the envtest bootstrap performed by StartSuite. Add
// fields only as concrete suites require them.
type SuiteOptions struct {
	// RegisterGroupRefIndexers installs the spec.groupRef field indexers for
	// MCPServer, MCPRemoteProxy, and MCPServerEntry. Suites whose controllers
	// list by GroupRef (anything that wires up the MCPGroup or VirtualMCPServer
	// controllers) need these; the rest leave it false.
	RegisterGroupRefIndexers bool
}

// SuiteEnv holds the shared envtest infrastructure for an operator integration
// suite. It is produced by StartSuite (full bootstrap with a manager) or
// StartMinimalSuite (envtest + client only) and torn down by Stop.
type SuiteEnv struct {
	// Cfg is the rest config for the running envtest control plane.
	Cfg *rest.Config
	// Client is a controller-runtime client wired to the global scheme.
	Client client.Client
	// Manager is the controller manager. It is nil for StartMinimalSuite and is
	// created but not started by StartSuite — call StartManager to launch it.
	Manager ctrl.Manager
	// Ctx is the suite-scoped context cancelled by Stop. Specs and indexers that
	// need a context should use it.
	Ctx context.Context

	testEnv *envtest.Environment
	cancel  context.CancelFunc
	// managerDone is closed when the goroutine started by StartManager returns,
	// i.e. once Manager.Start has fully shut down every runnable. It stays nil
	// until StartManager is called.
	managerDone chan struct{}
}

// StartSuite bootstraps a full operator integration suite: it starts an envtest
// control plane loaded with the operator CRDs, registers the operator API
// versions plus the built-in Kubernetes types on the global client-go scheme,
// creates a client and a controller manager (metrics and health probes
// disabled), and optionally installs the GroupRef field indexers.
//
// The returned manager is not started — callers register their controllers on
// it and then call StartManager. Pair every StartSuite with a deferred or
// AfterSuite call to Stop. It must be called from within a Ginkgo node
// (BeforeSuite or a spec) because it uses Ginkgo/Gomega for logging and
// assertions.
func StartSuite(opts SuiteOptions) *SuiteEnv {
	setupLogger()

	ctx, cancel := context.WithCancel(context.Background())

	By("bootstrapping test environment")
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "..", "deploy", "charts", "operator-crds", "files", "crds")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	registerOperatorScheme()

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0", // Disable metrics server for tests to avoid port conflicts
		},
		HealthProbeBindAddress: "0", // Disable health probe for tests
		// Keep the manager's own shutdown bounded so Stop never waits on it
		// longer than envtest is willing to wait for the control plane.
		GracefulShutdownTimeout: ptr.To(managerGracefulShutdownTimeout),
	})
	Expect(err).ToNot(HaveOccurred())

	if opts.RegisterGroupRefIndexers {
		registerGroupRefIndexers(ctx, k8sManager)
	}

	return &SuiteEnv{
		Cfg:     cfg,
		Client:  k8sClient,
		Manager: k8sManager,
		Ctx:     ctx,
		testEnv: testEnv,
		cancel:  cancel,
	}
}

// StartMinimalSuite bootstraps an envtest control plane with no operator CRDs
// and no controller manager: just the API server, the apiextensions scheme (so
// tests can install CRDs on demand), and a client. It exists for the
// StorageVersionMigrator suite, which installs its own CRDs per-test and drives
// reconcilers directly. It must be called from within a Ginkgo node.
func StartMinimalSuite() *SuiteEnv {
	setupLogger()

	ctx, cancel := context.WithCancel(context.Background())

	By("bootstrapping envtest")
	testEnv := &envtest.Environment{
		ErrorIfCRDPathMissing: false, // tests install CRDs on demand
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	Expect(apiextensionsv1.AddToScheme(scheme.Scheme)).To(Succeed())

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	return &SuiteEnv{
		Cfg:     cfg,
		Client:  k8sClient,
		Ctx:     ctx,
		testEnv: testEnv,
		cancel:  cancel,
	}
}

// StartManager launches the controller manager in a background goroutine. Call
// it after registering all controllers on Manager. It is a no-op when there is
// no manager (i.e. for StartMinimalSuite).
func (e *SuiteEnv) StartManager() {
	if e.Manager == nil {
		return
	}
	e.managerDone = make(chan struct{})
	go func() {
		defer GinkgoRecover()
		// Closing before GinkgoRecover runs (deferred functions are LIFO) means
		// Stop is released even if the manager fails.
		defer close(e.managerDone)
		Expect(e.Manager.Start(e.Ctx)).To(Succeed(), "failed to run manager")
	}()
}

// Stop cancels the suite context and tears down the envtest control plane.
//
// When a manager is running it waits for Manager.Start to return before
// stopping the control plane. That wait is load-bearing, not politeness: the
// manager's informers hold long-running watch requests against kube-apiserver,
// and kube-apiserver's graceful shutdown blocks on draining them. Signalling
// the API server to stop while watches are still open makes it miss envtest's
// 20s ControlPlaneStopTimeout, which surfaces as a flaky
// "timeout waiting for process kube-apiserver to stop" failure in AfterSuite.
// The suites with fewer specs than Ginkgo procs (MCPTelemetryConfig, MCPGroup)
// hit it most often: their idle procs cancel the suite context moments after
// the informers established their watches.
func (e *SuiteEnv) Stop() {
	By("tearing down the test environment")
	e.cancel()
	if e.managerDone != nil {
		select {
		case <-e.managerDone:
		case <-time.After(managerStopTimeout):
			// Fall through to the control plane teardown anyway: reporting the
			// apiserver timeout on top of this would only obscure the real
			// problem, which is a runnable that refused to stop.
			AddReportEntry("manager did not shut down within " + managerStopTimeout.String())
		}
	}
	Expect(e.testEnv.Stop()).NotTo(HaveOccurred())
}

// setupLogger configures the controller-runtime logger to emit only errors
// unless a test fails, matching the behavior every suite relied on.
func setupLogger() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true), zap.Level(zapcore.ErrorLevel)))
}

// registerOperatorScheme adds the operator API versions and the built-in
// Kubernetes types used by the controllers to the global client-go scheme.
// Registering types a given suite does not use is harmless.
func registerOperatorScheme() {
	for _, add := range []func(s *runtime.Scheme) error{
		mcpv1alpha1.AddToScheme,
		mcpv1beta1.AddToScheme,
		appsv1.AddToScheme,
		corev1.AddToScheme,
		rbacv1.AddToScheme,
	} {
		Expect(add(scheme.Scheme)).To(Succeed())
	}
}

// registerGroupRefIndexers installs the spec.groupRef field indexers for the
// MCPServer, MCPRemoteProxy, and MCPServerEntry types so controllers can list
// objects by their owning group.
func registerGroupRefIndexers(ctx context.Context, mgr ctrl.Manager) {
	indexer := mgr.GetFieldIndexer()

	Expect(indexer.IndexField(ctx, &mcpv1beta1.MCPServer{}, "spec.groupRef", func(obj client.Object) []string {
		return groupRefValue(obj.(*mcpv1beta1.MCPServer).Spec.GroupRef.GetName())
	})).To(Succeed())

	Expect(indexer.IndexField(ctx, &mcpv1beta1.MCPRemoteProxy{}, "spec.groupRef", func(obj client.Object) []string {
		return groupRefValue(obj.(*mcpv1beta1.MCPRemoteProxy).Spec.GroupRef.GetName())
	})).To(Succeed())

	Expect(indexer.IndexField(ctx, &mcpv1beta1.MCPServerEntry{}, "spec.groupRef", func(obj client.Object) []string {
		return groupRefValue(obj.(*mcpv1beta1.MCPServerEntry).Spec.GroupRef.GetName())
	})).To(Succeed())
}

// groupRefValue returns the index value for a groupRef name, or nil when unset
// so the object is left out of the index.
func groupRefValue(name string) []string {
	if name == "" {
		return nil
	}
	return []string{name}
}

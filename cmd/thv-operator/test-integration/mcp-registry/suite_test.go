package operator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	testMgr   ctrl.Manager
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestOperatorE2E(t *testing.T) { //nolint:paralleltest // E2E tests should not run in parallel
	RegisterFailHandler(Fail)
	RunSpecs(t, "Operator E2E Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")

	// Check if we should use an existing cluster (for CI/CD)
	useExistingCluster := os.Getenv("USE_EXISTING_CLUSTER") == "true"

	// // Get kubebuilder assets path
	kubebuilderAssets := os.Getenv("KUBEBUILDER_ASSETS")

	if !useExistingCluster {
		By(fmt.Sprintf("using kubebuilder assets from: %s", kubebuilderAssets))
		if kubebuilderAssets == "" {
			By("WARNING: no kubebuilder assets found, test may fail")
		}
	}

	testEnv = &envtest.Environment{
		UseExistingCluster: &useExistingCluster,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "..", "deploy", "charts", "operator-crds", "crds"),
		},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: kubebuilderAssets,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	// Add MCPRegistry scheme
	err = mcpv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// Create controller-runtime client
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Verify MCPRegistry CRD is available
	By("verifying MCPRegistry CRD is available")
	Eventually(func() error {
		mcpRegistry := &mcpv1alpha1.MCPRegistry{}
		return k8sClient.Get(ctx, client.ObjectKey{
			Namespace: "default",
			Name:      "test-availability-check",
		}, mcpRegistry)
	}, time.Minute, time.Second).Should(MatchError(ContainSubstring("not found")))

	// Set up the manager for controllers (only for envtest, not existing cluster)
	if !useExistingCluster {
		By("setting up controller manager for envtest")
		testMgr, err = ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme.Scheme,
			Metrics: metricsserver.Options{
				BindAddress: "0", // Disable metrics server for tests
			},
			HealthProbeBindAddress: "0", // Disable health probe for tests
		})
		Expect(err).NotTo(HaveOccurred())

		// Set up MCPRegistry controller
		By("setting up MCPRegistry controller")
		err = controllers.NewMCPRegistryReconciler(testMgr.GetClient(), testMgr.GetScheme()).SetupWithManager(testMgr)
		Expect(err).NotTo(HaveOccurred())

		// Start the manager in the background
		By("starting controller manager")
		go func() {
			defer GinkgoRecover()
			err = testMgr.Start(ctx)
			Expect(err).NotTo(HaveOccurred(), "failed to run manager")
		}()

		// Wait for the manager to be ready
		By("waiting for controller manager to be ready")
		Eventually(func() bool {
			return testMgr.GetCache().WaitForCacheSync(ctx)
		}, time.Minute, time.Second).Should(BeTrue())
	}
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// TestNamespace represents a test namespace with automatic cleanup
type TestNamespace struct {
	Name      string
	Namespace *corev1.Namespace
	Client    client.Client
	ctx       context.Context
}

// NewTestNamespace creates a new test namespace with a unique name
func NewTestNamespace(namePrefix string) *TestNamespace {
	timestamp := time.Now().Unix()
	name := fmt.Sprintf("%s-%d", namePrefix, timestamp)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"test.toolhive.io/suite":  "operator-e2e",
				"test.toolhive.io/prefix": namePrefix,
			},
		},
	}

	return &TestNamespace{
		Name:      name,
		Namespace: ns,
		Client:    k8sClient,
		ctx:       ctx,
	}
}

// Create creates the namespace in the cluster
func (tn *TestNamespace) Create() error {
	return tn.Client.Create(tn.ctx, tn.Namespace)
}

// Delete deletes the namespace and all its resources
func (tn *TestNamespace) Delete() error {
	return tn.Client.Delete(tn.ctx, tn.Namespace)
}

// WaitForDeletion waits for the namespace to be fully deleted
func (tn *TestNamespace) WaitForDeletion(timeout time.Duration) {
	Eventually(func() bool {
		ns := &corev1.Namespace{}
		err := tn.Client.Get(tn.ctx, client.ObjectKey{Name: tn.Name}, ns)
		return err != nil
	}, timeout, time.Second).Should(BeTrue(), "namespace should be deleted")
}

// GetClient returns a client scoped to this namespace
func (tn *TestNamespace) GetClient() client.Client {
	return tn.Client
}

// GetContext returns the test context
func (tn *TestNamespace) GetContext() context.Context {
	return tn.ctx
}

package operator_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi"
)

var _ = Describe("MCPRegistry RBAC Resources", Label("k8s", "registry", "rbac"), func() {
	var (
		ctx             context.Context
		registryHelper  *MCPRegistryTestHelper
		configMapHelper *ConfigMapTestHelper
		statusHelper    *StatusTestHelper
		testNamespace   string
	)

	BeforeEach(func() {
		ctx = context.Background()
		testNamespace = createTestNamespace(ctx)

		registryHelper = NewMCPRegistryTestHelper(ctx, k8sClient, testNamespace)
		configMapHelper = NewConfigMapTestHelper(ctx, k8sClient, testNamespace)
		statusHelper = NewStatusTestHelper(ctx, k8sClient, testNamespace)
	})

	AfterEach(func() {
		Expect(registryHelper.CleanupRegistries()).To(Succeed())
		Expect(configMapHelper.CleanupConfigMaps()).To(Succeed())
		deleteTestNamespace(ctx, testNamespace)
	})

	Context("RBAC Resource Creation", func() {
		It("should create ServiceAccount, Role, and RoleBinding for registry", func() {
			configMap := configMapHelper.CreateSampleToolHiveRegistry("rbac-test-config")

			registry := registryHelper.NewRegistryBuilder("rbac-test").
				WithConfigMapSource(configMap.Name, "registry.json").
				Create(registryHelper)

			// Wait for registry to be reconciled
			statusHelper.WaitForPhaseAny(registry.Name, []mcpv1alpha1.MCPRegistryPhase{
				mcpv1alpha1.MCPRegistryPhaseReady,
				mcpv1alpha1.MCPRegistryPhasePending,
			}, MediumTimeout)

			resourceName := registryapi.GetServiceAccountName(registry)

			By("verifying ServiceAccount is created")
			sa := &corev1.ServiceAccount{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: testNamespace,
				}, sa)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())
			Expect(sa.OwnerReferences).To(HaveLen(1))
			Expect(sa.OwnerReferences[0].Kind).To(Equal("MCPRegistry"))
			Expect(sa.OwnerReferences[0].Name).To(Equal(registry.Name))
			Expect(sa.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

			role := &rbacv1.Role{}
			By("verifying Role is created with correct rules")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: testNamespace,
				}, role)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())
			Expect(role.OwnerReferences).To(HaveLen(1))
			Expect(role.OwnerReferences[0].Kind).To(Equal("MCPRegistry"))
			Expect(role.OwnerReferences[0].Name).To(Equal(registry.Name))
			Expect(role.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

			rb := &rbacv1.RoleBinding{}
			By("verifying RoleBinding is created")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: testNamespace,
				}, rb)
			}, MediumTimeout, DefaultPollingInterval).Should(Succeed())
			Expect(rb.OwnerReferences).To(HaveLen(1))
			Expect(rb.OwnerReferences[0].Kind).To(Equal("MCPRegistry"))
			Expect(rb.OwnerReferences[0].Name).To(Equal(registry.Name))
			Expect(rb.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

			By("verifying Deployment uses the correct ServiceAccount")
			Eventually(func() string {
				deploymentName := registry.Name + "-api"
				deployment, err := NewK8sResourceTestHelper(ctx, k8sClient, testNamespace).GetDeployment(deploymentName)
				if err != nil {
					return ""
				}
				return deployment.Spec.Template.Spec.ServiceAccountName
			}, MediumTimeout, DefaultPollingInterval).Should(Equal(resourceName))
		})

	})
})

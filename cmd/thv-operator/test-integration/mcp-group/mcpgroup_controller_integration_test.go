package operator_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("MCPGroup Controller Integration Tests", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When creating an MCPGroup with existing MCPServers", Ordered, func() {
		var (
			namespace     string
			mcpGroupName  string
			mcpGroup      *mcpv1alpha1.MCPGroup
			server1       *mcpv1alpha1.MCPServer
			server2       *mcpv1alpha1.MCPServer
			serverNoGroup *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = fmt.Sprintf("test-mcpgroup-%d", time.Now().Unix())
			mcpGroupName = "test-group"

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			// Create MCPServers first
			server1 = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server1",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "example/mcp-server:latest",
					GroupRef: mcpGroupName,
				},
			}
			Expect(k8sClient.Create(ctx, server1)).Should(Succeed())

			server2 = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server2",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "example/mcp-server:latest",
					GroupRef: mcpGroupName,
				},
			}
			Expect(k8sClient.Create(ctx, server2)).Should(Succeed())

			serverNoGroup = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server-no-group",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "example/mcp-server:latest",
					// No GroupRef
				},
			}
			Expect(k8sClient.Create(ctx, serverNoGroup)).Should(Succeed())

			// Update server statuses to Running
			Eventually(func() error {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server1.Name, Namespace: namespace}, freshServer); err != nil {
					return err
				}
				freshServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
				return k8sClient.Status().Update(ctx, freshServer)
			}, timeout, interval).Should(Succeed())

			Eventually(func() error {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server2.Name, Namespace: namespace}, freshServer); err != nil {
					return err
				}
				freshServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
				return k8sClient.Status().Update(ctx, freshServer)
			}, timeout, interval).Should(Succeed())

			// Verify the statuses were updated
			Eventually(func() bool {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server1.Name, Namespace: namespace}, freshServer); err != nil {
					return false
				}
				return freshServer.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning
			}, timeout, interval).Should(BeTrue())

			Eventually(func() bool {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server2.Name, Namespace: namespace}, freshServer); err != nil {
					return false
				}
				return freshServer.Status.Phase == mcpv1alpha1.MCPServerPhaseRunning
			}, timeout, interval).Should(BeTrue())

			// Now create the MCPGroup
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for integration tests",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up
			Expect(k8sClient.Delete(ctx, server1)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, server2)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, serverNoGroup)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())

			// Delete namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("Should find existing MCPServers and update status", func() {
			// Check that the group found both servers
			Eventually(func() int {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup); err != nil {
					return -1
				}
				return updatedGroup.Status.ServerCount
			}, timeout, interval).Should(Equal(2))

			// The group should be Ready after successful reconciliation
			Eventually(func() mcpv1alpha1.MCPGroupPhase {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup); err != nil {
					return ""
				}
				return updatedGroup.Status.Phase
			}, timeout, interval).Should(Equal(mcpv1alpha1.MCPGroupPhaseReady))

			// Verify the servers are in the group
			updatedGroup := &mcpv1alpha1.MCPGroup{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpGroupName,
				Namespace: namespace,
			}, updatedGroup)).Should(Succeed())

			Expect(updatedGroup.Status.Servers).To(ContainElements("server1", "server2"))
			Expect(updatedGroup.Status.Servers).NotTo(ContainElement("server-no-group"))
		})
	})

	Context("When creating a new MCPServer with groupRef", Ordered, func() {
		var (
			namespace    string
			mcpGroupName string
			mcpGroup     *mcpv1alpha1.MCPGroup
			newServer    *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = fmt.Sprintf("test-new-server-%d", time.Now().Unix())
			mcpGroupName = "test-group-new-server"

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			// Create MCPGroup first
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for new server",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for initial reconciliation
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			// Clean up
			if newServer != nil {
				Expect(k8sClient.Delete(ctx, newServer)).Should(Succeed())
			}
			Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())

			// Delete namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("Should trigger MCPGroup reconciliation when server is created", func() {
			// Create new server with groupRef
			newServer = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "new-server",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "example/mcp-server:latest",
					GroupRef: mcpGroupName,
				},
			}
			Expect(k8sClient.Create(ctx, newServer)).Should(Succeed())

			// Update server status to Running
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: newServer.Name, Namespace: namespace}, newServer); err != nil {
					return err
				}
				newServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
				return k8sClient.Status().Update(ctx, newServer)
			}, timeout, interval).Should(Succeed())

			// Wait for MCPGroup to be updated
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				if err != nil {
					return false
				}

				return updatedGroup.Status.ServerCount == 1 &&
					updatedGroup.Status.Phase == mcpv1alpha1.MCPGroupPhaseReady
			}, timeout, interval).Should(BeTrue())

			// Verify the server is in the group
			updatedGroup := &mcpv1alpha1.MCPGroup{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpGroupName,
				Namespace: namespace,
			}, updatedGroup)).Should(Succeed())

			Expect(updatedGroup.Status.Servers).To(ContainElement("new-server"))
		})
	})

	Context("When deleting an MCPServer from a group", Ordered, func() {
		var (
			namespace    string
			mcpGroupName string
			mcpGroup     *mcpv1alpha1.MCPGroup
			server1      *mcpv1alpha1.MCPServer
			server2      *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = fmt.Sprintf("test-delete-server-%d", time.Now().Unix())
			mcpGroupName = "test-group-delete"

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			// Create MCPServers
			server1 = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server1",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "example/mcp-server:latest",
					GroupRef: mcpGroupName,
				},
			}
			Expect(k8sClient.Create(ctx, server1)).Should(Succeed())

			server2 = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server2",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "example/mcp-server:latest",
					GroupRef: mcpGroupName,
				},
			}
			Expect(k8sClient.Create(ctx, server2)).Should(Succeed())

			// Update server statuses to Running
			Eventually(func() error {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server1.Name, Namespace: namespace}, freshServer); err != nil {
					return err
				}
				freshServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
				return k8sClient.Status().Update(ctx, freshServer)
			}, timeout, interval).Should(Succeed())

			Eventually(func() error {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server2.Name, Namespace: namespace}, freshServer); err != nil {
					return err
				}
				freshServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
				return k8sClient.Status().Update(ctx, freshServer)
			}, timeout, interval).Should(Succeed())

			// Create MCPGroup
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for server deletion",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for initial reconciliation with both servers
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.ServerCount == 2
			}, timeout, interval).Should(BeTrue())
		})

		AfterAll(func() {
			// Clean up remaining resources
			// server1 is deleted in the test, so only check if it still exists
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: server1.Name, Namespace: namespace}, server1); err == nil {
				Expect(k8sClient.Delete(ctx, server1)).Should(Succeed())
			}
			Expect(k8sClient.Delete(ctx, server2)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())

			// Delete namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("Should remain Ready after checking servers in namespace", func() {
			// The MCPGroup should remain Ready because it can successfully list servers
			// in the namespace. The MCPGroup phase is based on the ability to query
			// servers, not on the state or count of servers.
			updatedGroup := &mcpv1alpha1.MCPGroup{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpGroupName,
				Namespace: namespace,
			}, updatedGroup)).Should(Succeed())

			// The MCPGroup should be Ready with 2 servers
			Expect(updatedGroup.Status.Phase).To(Equal(mcpv1alpha1.MCPGroupPhaseReady))
			Expect(updatedGroup.Status.ServerCount).To(Equal(2))

			// Trigger a reconciliation by updating the MCPGroup spec
			Eventually(func() error {
				freshGroup := &mcpv1alpha1.MCPGroup{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: mcpGroupName, Namespace: namespace}, freshGroup); err != nil {
					return err
				}
				freshGroup.Spec.Description = "Test group for server deletion - updated"
				return k8sClient.Update(ctx, freshGroup)
			}, timeout, interval).Should(Succeed())

			// After reconciliation, the MCPGroup should still be Ready
			Eventually(func() mcpv1alpha1.MCPGroupPhase {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup); err != nil {
					return ""
				}
				return updatedGroup.Status.Phase
			}, timeout, interval).Should(Equal(mcpv1alpha1.MCPGroupPhaseReady))
		})
	})

	Context("When an MCPServer changes state", Ordered, func() {
		var (
			namespace    string
			mcpGroupName string
			mcpGroup     *mcpv1alpha1.MCPGroup
			server1      *mcpv1alpha1.MCPServer
			server2      *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespace = fmt.Sprintf("test-server-state-%d", time.Now().Unix())
			mcpGroupName = "test-group-state"

			// Create namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).Should(Succeed())

			// Create MCPServers
			server1 = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server1",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "example/mcp-server:latest",
					GroupRef: mcpGroupName,
				},
			}
			Expect(k8sClient.Create(ctx, server1)).Should(Succeed())

			server2 = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server2",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "example/mcp-server:latest",
					GroupRef: mcpGroupName,
				},
			}
			Expect(k8sClient.Create(ctx, server2)).Should(Succeed())

			// Update server statuses to Running
			Eventually(func() error {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server1.Name, Namespace: namespace}, freshServer); err != nil {
					return err
				}
				freshServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
				return k8sClient.Status().Update(ctx, freshServer)
			}, timeout, interval).Should(Succeed())

			Eventually(func() error {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server2.Name, Namespace: namespace}, freshServer); err != nil {
					return err
				}
				freshServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
				return k8sClient.Status().Update(ctx, freshServer)
			}, timeout, interval).Should(Succeed())

			// Create MCPGroup
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for state changes",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for initial reconciliation - the group should find the servers
			Eventually(func() int {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup); err != nil {
					return -1
				}
				return updatedGroup.Status.ServerCount
			}, timeout, interval).Should(Equal(2))
		})

		AfterAll(func() {
			// Clean up
			Expect(k8sClient.Delete(ctx, server1)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, server2)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, mcpGroup)).Should(Succeed())

			// Delete namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Delete(ctx, ns)).Should(Succeed())
		})

		It("Should remain Ready when reconciled after server status changes", func() {
			// Update server1 status to Failed
			Eventually(func() error {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: server1.Name, Namespace: namespace}, freshServer); err != nil {
					return err
				}
				freshServer.Status.Phase = mcpv1alpha1.MCPServerPhaseFailed
				return k8sClient.Status().Update(ctx, freshServer)
			}, timeout, interval).Should(Succeed())

			// Status changes don't trigger MCPGroup reconciliation, so we need to trigger it
			// by updating the MCPGroup spec (e.g., adding/updating description)
			Eventually(func() error {
				freshGroup := &mcpv1alpha1.MCPGroup{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: mcpGroupName, Namespace: namespace}, freshGroup); err != nil {
					return err
				}
				freshGroup.Spec.Description = "Test group for state changes - updated"
				return k8sClient.Update(ctx, freshGroup)
			}, timeout, interval).Should(Succeed())

			// The MCPGroup should still be Ready because it doesn't check individual server phases
			// (it only checks if servers exist). This reflects the simplified controller logic.
			Eventually(func() mcpv1alpha1.MCPGroupPhase {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				if err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, updatedGroup); err != nil {
					return ""
				}
				return updatedGroup.Status.Phase
			}, timeout, interval).Should(Equal(mcpv1alpha1.MCPGroupPhaseReady))

			// Verify both servers are still counted
			updatedGroup := &mcpv1alpha1.MCPGroup{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpGroupName,
				Namespace: namespace,
			}, updatedGroup)).Should(Succeed())
			Expect(updatedGroup.Status.ServerCount).To(Equal(2))
		})
	})

	Context("When testing namespace isolation", Ordered, func() {
		var (
			namespaceA   string
			namespaceB   string
			mcpGroupName string
			mcpGroupA    *mcpv1alpha1.MCPGroup
			serverA      *mcpv1alpha1.MCPServer
			serverB      *mcpv1alpha1.MCPServer
		)

		BeforeAll(func() {
			namespaceA = fmt.Sprintf("test-ns-a-%d", time.Now().Unix())
			namespaceB = fmt.Sprintf("test-ns-b-%d", time.Now().Unix())
			mcpGroupName = "test-group"

			// Create namespaces
			nsA := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespaceA,
				},
			}
			Expect(k8sClient.Create(ctx, nsA)).Should(Succeed())

			nsB := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespaceB,
				},
			}
			Expect(k8sClient.Create(ctx, nsB)).Should(Succeed())

			// Create server in namespace A
			serverA = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server-a",
					Namespace: namespaceA,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "example/mcp-server:latest",
					GroupRef: mcpGroupName,
				},
			}
			Expect(k8sClient.Create(ctx, serverA)).Should(Succeed())

			// Create server in namespace B with same group name
			serverB = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "server-b",
					Namespace: namespaceB,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:    "example/mcp-server:latest",
					GroupRef: mcpGroupName, // Same group name, different namespace
				},
			}
			Expect(k8sClient.Create(ctx, serverB)).Should(Succeed())

			// Update server statuses
			Eventually(func() error {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: serverA.Name, Namespace: namespaceA}, freshServer); err != nil {
					return err
				}
				freshServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
				return k8sClient.Status().Update(ctx, freshServer)
			}, timeout, interval).Should(Succeed())

			Eventually(func() error {
				freshServer := &mcpv1alpha1.MCPServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: serverB.Name, Namespace: namespaceB}, freshServer); err != nil {
					return err
				}
				freshServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
				return k8sClient.Status().Update(ctx, freshServer)
			}, timeout, interval).Should(Succeed())

			// Create MCPGroup in namespace A
			mcpGroupA = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespaceA,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group in namespace A",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroupA)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up
			Expect(k8sClient.Delete(ctx, serverA)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, serverB)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, mcpGroupA)).Should(Succeed())

			// Delete namespaces
			nsA := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespaceA,
				},
			}
			Expect(k8sClient.Delete(ctx, nsA)).Should(Succeed())

			nsB := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespaceB,
				},
			}
			Expect(k8sClient.Delete(ctx, nsB)).Should(Succeed())
		})

		It("Should only include servers from the same namespace", func() {
			// Wait for reconciliation
			Eventually(func() bool {
				updatedGroup := &mcpv1alpha1.MCPGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespaceA,
				}, updatedGroup)
				return err == nil && updatedGroup.Status.ServerCount > 0
			}, timeout, interval).Should(BeTrue())

			// Verify only server-a is in the group
			updatedGroup := &mcpv1alpha1.MCPGroup{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      mcpGroupName,
				Namespace: namespaceA,
			}, updatedGroup)).Should(Succeed())

			Expect(updatedGroup.Status.ServerCount).To(Equal(1))
			Expect(updatedGroup.Status.Servers).To(ContainElement("server-a"))
			Expect(updatedGroup.Status.Servers).NotTo(ContainElement("server-b"))
		})
	})
})

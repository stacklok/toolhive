// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var _ = Describe("VirtualMCPServer Backend Discovery Integration Tests", func() {
	const (
		timeout          = time.Second * 30
		interval         = time.Millisecond * 250
		defaultNamespace = "default"
	)

	Context("When creating a VirtualMCPServer with multiple backends", Ordered, func() {
		var (
			namespace            string
			mcpGroupName         string
			vmcpName             string
			mcpServer1Name       string
			mcpServer2Name       string
			mcpServer3Name       string
			authConfigName       string
			mcpGroup             *mcpv1alpha1.MCPGroup
			vmcp                 *mcpv1alpha1.VirtualMCPServer
			mcpServer1           *mcpv1alpha1.MCPServer
			mcpServer2           *mcpv1alpha1.MCPServer
			mcpServer3           *mcpv1alpha1.MCPServer
			externalAuthConfig   *mcpv1alpha1.MCPExternalAuthConfig
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpGroupName = "test-discovery-group"
			vmcpName = "test-discovery-vmcp"
			mcpServer1Name = "backend-server-1"
			mcpServer2Name = "backend-server-2"
			mcpServer3Name = "backend-server-3"
			authConfigName = "test-auth-config"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Create MCPExternalAuthConfig for testing auth discovery
			externalAuthConfig = &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      authConfigName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "tokenExchange",
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						Audience: "https://backend-service.example.com",
						SubjectTokenType: "access_token",
					},
				},
			}
			Expect(k8sClient.Create(ctx, externalAuthConfig)).Should(Succeed())

			// Create MCPServer 1 - no auth config (discovered mode)
			mcpServer1 = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServer1Name,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "example/backend1:latest",
					Transport: "streamable-http",
					ProxyMode: "mcp",
					Port:      8080,
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer1)).Should(Succeed())

			// Create MCPServer 2 - with external auth config
			mcpServer2 = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServer2Name,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "example/backend2:latest",
					Transport: "http",
					ProxyMode: "mcp",
					Port:      8080,
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: authConfigName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer2)).Should(Succeed())

			// Create MCPServer 3 - stdio transport
			mcpServer3 = &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServer3Name,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "example/backend3:latest",
					Transport: "stdio",
					ProxyMode: "sse",
					Port:      8080,
				},
			}
			Expect(k8sClient.Create(ctx, mcpServer3)).Should(Succeed())

			// Wait for MCPServers to have URLs in status (set by controller)
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServer1Name,
					Namespace: namespace,
				}, mcpServer1)
				return err == nil && mcpServer1.Status.URL != ""
			}, timeout, interval).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServer2Name,
					Namespace: namespace,
				}, mcpServer2)
				return err == nil && mcpServer2.Status.URL != ""
			}, timeout, interval).Should(BeTrue())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpServer3Name,
					Namespace: namespace,
				}, mcpServer3)
				return err == nil && mcpServer3.Status.URL != ""
			}, timeout, interval).Should(BeTrue())

			// Create MCPGroup containing all three servers
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group for backend discovery",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Wait for MCPGroup to discover all servers
			Eventually(func() int {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, mcpGroup)
				if err != nil {
					return 0
				}
				return mcpGroup.Status.ServerCount
			}, timeout, interval).Should(Equal(3))

			// Create VirtualMCPServer referencing the group
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
					// Add outgoing auth override for server3
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "mixed",
						Backends: map[string]mcpv1alpha1.BackendAuthConfig{
							mcpServer3Name: {
								Type: "pass_through",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())
		})

		AfterAll(func() {
			// Clean up resources in reverse order
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
			_ = k8sClient.Delete(ctx, mcpServer3)
			_ = k8sClient.Delete(ctx, mcpServer2)
			_ = k8sClient.Delete(ctx, mcpServer1)
			_ = k8sClient.Delete(ctx, externalAuthConfig)
		})

		It("Should discover all backends from the MCPGroup", func() {
			Eventually(func() int {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, vmcp)
				if err != nil {
					return 0
				}
				return vmcp.Status.BackendCount
			}, timeout, interval).Should(Equal(3))

			// Verify DiscoveredBackends field is populated
			Expect(vmcp.Status.DiscoveredBackends).To(HaveLen(3))
		})

		It("Should correctly identify backend with no auth config (discovered mode)", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, vmcp)
				if err != nil {
					return false
				}

				// Find backend-server-1 in discovered backends
				for _, backend := range vmcp.Status.DiscoveredBackends {
					if backend.Name == mcpServer1Name {
						return backend.AuthType == "discovered" &&
							backend.ExternalAuthConfigRef == "" &&
							backend.TransportType == "streamable-http" &&
							backend.URL != ""
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("Should correctly extract external auth config from MCPServer", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, vmcp)
				if err != nil {
					return false
				}

				// Find backend-server-2 in discovered backends
				for _, backend := range vmcp.Status.DiscoveredBackends {
					if backend.Name == mcpServer2Name {
						return backend.AuthType == "external_auth_config" &&
							backend.ExternalAuthConfigRef == authConfigName &&
							backend.TransportType == "http" &&
							backend.URL != ""
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("Should apply outgoing auth overrides from VirtualMCPServer spec", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, vmcp)
				if err != nil {
					return false
				}

				// Find backend-server-3 in discovered backends
				// Should use pass_through auth from VirtualMCPServer spec override
				for _, backend := range vmcp.Status.DiscoveredBackends {
					if backend.Name == mcpServer3Name {
						return backend.AuthType == "pass_through" &&
							backend.TransportType == "stdio" &&
							backend.URL != ""
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("Should update discovered backends when MCPGroup changes", func() {
			// Remove server3 from the group by deleting it
			Expect(k8sClient.Delete(ctx, mcpServer3)).Should(Succeed())

			// Wait for MCPGroup to update its server list
			Eventually(func() int {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      mcpGroupName,
					Namespace: namespace,
				}, mcpGroup)
				if err != nil {
					return -1
				}
				return mcpGroup.Status.ServerCount
			}, timeout, interval).Should(Equal(2))

			// VirtualMCPServer should reflect the change
			Eventually(func() int {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, vmcp)
				if err != nil {
					return -1
				}
				return vmcp.Status.BackendCount
			}, timeout, interval).Should(Equal(2))

			// Verify server3 is no longer in discovered backends
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, vmcp)
				if err != nil {
					return false
				}

				for _, backend := range vmcp.Status.DiscoveredBackends {
					if backend.Name == mcpServer3Name {
						return false // Server3 should not be present
					}
				}
				return true
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When MCPGroup is not ready", Ordered, func() {
		var (
			namespace    string
			mcpGroupName string
			vmcpName     string
			mcpGroup     *mcpv1alpha1.MCPGroup
			vmcp         *mcpv1alpha1.VirtualMCPServer
		)

		BeforeAll(func() {
			namespace = defaultNamespace
			mcpGroupName = "test-notready-group"
			vmcpName = "test-notready-vmcp"

			// Create namespace if it doesn't exist
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_ = k8sClient.Create(ctx, ns)

			// Create MCPGroup but don't add any servers (will be Pending)
			mcpGroup = &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpGroupName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPGroupSpec{
					Description: "Test group that stays not ready",
				},
			}
			Expect(k8sClient.Create(ctx, mcpGroup)).Should(Succeed())

			// Create VirtualMCPServer referencing the not-ready group
			vmcp = &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmcpName,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: mcpGroupName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, vmcp)).Should(Succeed())
		})

		AfterAll(func() {
			_ = k8sClient.Delete(ctx, vmcp)
			_ = k8sClient.Delete(ctx, mcpGroup)
		})

		It("Should set GroupRefValidated condition to False when MCPGroup is not ready", func() {
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      vmcpName,
					Namespace: namespace,
				}, vmcp)
				if err != nil {
					return false
				}

				// Check for GroupRefValidated condition
				for _, condition := range vmcp.Status.Conditions {
					if condition.Type == "GroupRefValidated" {
						return condition.Status == metav1.ConditionFalse &&
							condition.Reason == "GroupRefNotReady"
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("Should not discover any backends when MCPGroup is not ready", func() {
			// Wait a bit to ensure controller has reconciled
			time.Sleep(2 * time.Second)

			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      vmcpName,
				Namespace: namespace,
			}, vmcp)
			Expect(err).NotTo(HaveOccurred())

			// BackendCount should be 0 because group is not ready
			Expect(vmcp.Status.BackendCount).To(Equal(0))
			Expect(vmcp.Status.DiscoveredBackends).To(BeEmpty())
		})
	})
})

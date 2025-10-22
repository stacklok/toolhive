package sources_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/httpclient"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
)

var _ = Describe("ToolHiveAPIHandler", func() {
	var (
		handler    *sources.ToolHiveAPIHandler
		ctx        context.Context
		mockServer *httptest.Server
	)

	BeforeEach(func() {
		httpClient := httpclient.NewDefaultClient(0)
		handler = sources.NewToolHiveAPIHandler(httpClient)
		ctx = context.Background()
	})

	AfterEach(func() {
		if mockServer != nil {
			mockServer.Close()
		}
	})

	Describe("Validate", func() {
		Context("Valid ToolHive API", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == toolhiveInfoPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{"version":"1.0.0","last_updated":"2025-01-14T00:00:00Z","source":"file:/data/registry.json","total_servers":10}`)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				}))
			})

			It("should validate successfully", func() {
				err := handler.Validate(ctx, mockServer.URL)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Missing /v0/info endpoint", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				}))
			})

			It("should fail validation", func() {
				err := handler.Validate(ctx, mockServer.URL)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to fetch /v0/info"))
			})
		})

		Context("Invalid JSON response", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == toolhiveInfoPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{invalid json}`)
					}
				}))
			})

			It("should fail validation", func() {
				err := handler.Validate(ctx, mockServer.URL)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to parse"))
			})
		})

		Context("Missing version field", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == toolhiveInfoPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{"last_updated":"2025-01-14T00:00:00Z","total_servers":10}`)
					}
				}))
			})

			It("should fail validation", func() {
				err := handler.Validate(ctx, mockServer.URL)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("missing 'version' field"))
			})
		})

		Context("Invalid total_servers value", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == toolhiveInfoPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{"version":"1.0.0","total_servers":-5}`)
					}
				}))
			})

			It("should fail validation", func() {
				err := handler.Validate(ctx, mockServer.URL)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid 'total_servers' value"))
			})
		})

		Context("Zero servers is valid", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == toolhiveInfoPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{"version":"1.0.0","total_servers":0}`)
					}
				}))
			})

			It("should validate successfully", func() {
				err := handler.Validate(ctx, mockServer.URL)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("FetchRegistry", func() {
		var mcpRegistry *mcpv1alpha1.MCPRegistry

		BeforeEach(func() {
			mcpRegistry = &mcpv1alpha1.MCPRegistry{}
			mcpRegistry.Spec.Source.API = &mcpv1alpha1.APISource{}
		})

		Context("Successful fetch with server details", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case toolhiveServersPath:
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{
							"servers": [
								{"name": "server1", "description": "Test Server 1", "tier": "official", "status": "active", "transport": "stdio"},
								{"name": "server2", "description": "Test Server 2", "tier": "community", "status": "active", "transport": "sse"}
							],
							"total": 2
						}`)
					case "/v0/servers/server1":
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{
							"name": "server1",
							"description": "Test Server 1",
							"tier": "official",
							"status": "active",
							"transport": "stdio",
							"image": "ghcr.io/test/server1:latest",
							"env": {"KEY": "value"}
						}`)
					case "/v0/servers/server2":
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{
							"name": "server2",
							"description": "Test Server 2",
							"tier": "community",
							"status": "active",
							"transport": "sse",
							"image": "ghcr.io/test/server2:v1.0"
						}`)
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				mcpRegistry.Spec.Source.API.Endpoint = mockServer.URL
			})

			It("should fetch and convert servers successfully", func() {
				result, err := handler.FetchRegistry(ctx, mcpRegistry)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).NotTo(BeNil())
				Expect(result.Registry).NotTo(BeNil())
				Expect(result.Registry.Servers).To(HaveLen(2))
				Expect(result.Registry.Servers).To(HaveKey("server1"))
				Expect(result.Registry.Servers).To(HaveKey("server2"))
				Expect(result.Hash).NotTo(BeEmpty())
				Expect(result.Format).To(Equal(mcpv1alpha1.RegistryFormatToolHive))
			})

			It("should fetch server details correctly", func() {
				result, err := handler.FetchRegistry(ctx, mcpRegistry)
				Expect(err).NotTo(HaveOccurred())

				server1 := result.Registry.Servers["server1"]
				Expect(server1).NotTo(BeNil())
				Expect(server1.Image).To(Equal("ghcr.io/test/server1:latest"))

				server2 := result.Registry.Servers["server2"]
				Expect(server2).NotTo(BeNil())
				Expect(server2.Image).To(Equal("ghcr.io/test/server2:v1.0"))
			})
		})

		Context("Fallback to summary when detail fetch fails", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == toolhiveServersPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{
							"servers": [
								{"name": "server1", "description": "Test Server", "tier": "official", "status": "active", "transport": "stdio"}
							],
							"total": 1
						}`)
					} else {
						// Server detail endpoint fails
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				mcpRegistry.Spec.Source.API.Endpoint = mockServer.URL
			})

			It("should use summary data when detail fetch fails", func() {
				result, err := handler.FetchRegistry(ctx, mcpRegistry)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Registry.Servers).To(HaveLen(1))
				Expect(result.Registry.Servers).To(HaveKey("server1"))
				Expect(result.Registry.Servers).To(Not(HaveKey("image")))
			})
		})

		Context("Failed to fetch server list", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				mcpRegistry.Spec.Source.API.Endpoint = mockServer.URL
			})

			It("should return error", func() {
				_, err := handler.FetchRegistry(ctx, mcpRegistry)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to fetch from API"))
			})
		})

		Context("Invalid JSON in server list response", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == toolhiveServersPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{invalid json}`)
					}
				}))
				mcpRegistry.Spec.Source.API.Endpoint = mockServer.URL
			})

			It("should return parse error", func() {
				_, err := handler.FetchRegistry(ctx, mcpRegistry)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to parse API response"))
			})
		})

		Context("Empty server list", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == toolhiveServersPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{"servers": [], "total": 0}`)
					}
				}))
				mcpRegistry.Spec.Source.API.Endpoint = mockServer.URL
			})

			It("should return empty registry", func() {
				result, err := handler.FetchRegistry(ctx, mcpRegistry)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Registry.Servers).To(BeEmpty())
			})
		})
	})

	Describe("CurrentHash", func() {
		var mcpRegistry *mcpv1alpha1.MCPRegistry

		BeforeEach(func() {
			mcpRegistry = &mcpv1alpha1.MCPRegistry{}
			mcpRegistry.Spec.Source.API = &mcpv1alpha1.APISource{}
		})

		Context("Successful hash calculation", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == toolhiveServersPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{"servers": [], "total": 0}`)
					}
				}))
				mcpRegistry.Spec.Source.API.Endpoint = mockServer.URL
			})

			It("should return hash of response", func() {
				hash, err := handler.CurrentHash(ctx, mcpRegistry)
				Expect(err).NotTo(HaveOccurred())
				Expect(hash).NotTo(BeEmpty())
				Expect(hash).To(HaveLen(64)) // SHA256 hex string length
			})
		})

		Context("Failed to fetch", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				mcpRegistry.Spec.Source.API.Endpoint = mockServer.URL
			})

			It("should return error", func() {
				_, err := handler.CurrentHash(ctx, mcpRegistry)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to fetch from API"))
			})
		})
	})
})

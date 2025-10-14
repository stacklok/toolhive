package sources_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
)

const (
	toolhiveInfoPath    = "/v0/info"
	toolhiveServersPath = "/v0/servers"
	openapiPath         = "/openapi.yaml"
)

func TestAPISources(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)
	RunSpecs(t, "API Source Handler Suite")
}

var _ = Describe("APISourceHandler", func() {
	var (
		handler    *sources.APISourceHandler
		ctx        context.Context
		mockServer *httptest.Server
	)

	BeforeEach(func() {
		handler = sources.NewAPISourceHandler()
		ctx = context.Background()
	})

	AfterEach(func() {
		if mockServer != nil {
			mockServer.Close()
		}
	})

	Describe("Validate", func() {
		It("should reject non-API source types", func() {
			source := &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeGit,
			}

			err := handler.Validate(source)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid source type"))
		})

		It("should reject missing API configuration", func() {
			source := &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeAPI,
				API:  nil,
			}

			err := handler.Validate(source)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("api configuration is required"))
		})

		It("should reject empty endpoint", func() {
			source := &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeAPI,
				API: &mcpv1alpha1.APISource{
					Endpoint: "",
				},
			}

			err := handler.Validate(source)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("endpoint cannot be empty"))
		})

		It("should accept valid API configuration", func() {
			source := &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeAPI,
				API: &mcpv1alpha1.APISource{
					Endpoint: "http://example.com",
				},
			}

			err := handler.Validate(source)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("Format Detection", func() {
		Context("ToolHive Format", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case toolhiveInfoPath:
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{"version":"1.0.0","last_updated":"2025-01-14T00:00:00Z","source":"file:/data/registry.json","total_servers":5}`)
					case toolhiveServersPath:
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `{"servers":[],"total":0}`)
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
			})

			It("should detect and validate ToolHive format", func() {
				mcpRegistry := &mcpv1alpha1.MCPRegistry{
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Source: mcpv1alpha1.MCPRegistrySource{
							Type: mcpv1alpha1.RegistrySourceTypeAPI,
							API: &mcpv1alpha1.APISource{
								Endpoint: mockServer.URL,
							},
						},
					},
				}

				result, err := handler.FetchRegistry(ctx, mcpRegistry)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).NotTo(BeNil())
				Expect(result.Format).To(Equal(mcpv1alpha1.RegistryFormatToolHive))
			})
		})

		Context("Upstream Format", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case toolhiveInfoPath:
						// Return 404 for /v0/info (upstream doesn't have this)
						w.WriteHeader(http.StatusNotFound)
						fmt.Fprint(w, `{"detail":"Endpoint not found"}`)
					case openapiPath:
						w.Header().Set("Content-Type", "application/x-yaml")
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, `
info:
  title: Official MCP Registry
  description: |
    A community driven registry service for Model Context Protocol (MCP) servers.

    [GitHub repository](https://github.com/modelcontextprotocol/registry)
  version: 1.0.0
openapi: 3.1.0
`)
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
			})

			It("should detect upstream format", func() {
				mcpRegistry := &mcpv1alpha1.MCPRegistry{
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Source: mcpv1alpha1.MCPRegistrySource{
							Type: mcpv1alpha1.RegistrySourceTypeAPI,
							API: &mcpv1alpha1.APISource{
								Endpoint: mockServer.URL,
							},
						},
					},
				}

				// Should detect as upstream but fail fetch (Phase 2 not implemented)
				_, err := handler.FetchRegistry(ctx, mcpRegistry)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("upstream MCP Registry API support not yet implemented"))
			})
		})

		Context("Invalid Format", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					// Return 404 for all endpoints
					w.WriteHeader(http.StatusNotFound)
				}))
			})

			It("should fail when neither format validates", func() {
				mcpRegistry := &mcpv1alpha1.MCPRegistry{
					Spec: mcpv1alpha1.MCPRegistrySpec{
						Source: mcpv1alpha1.MCPRegistrySource{
							Type: mcpv1alpha1.RegistrySourceTypeAPI,
							API: &mcpv1alpha1.APISource{
								Endpoint: mockServer.URL,
							},
						},
					},
				}

				_, err := handler.FetchRegistry(ctx, mcpRegistry)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("format detection failed"))
			})
		})
	})
})

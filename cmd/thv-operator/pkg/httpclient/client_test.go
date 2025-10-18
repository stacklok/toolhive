package httpclient_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/httpclient"
)

func TestHTTPClient(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)
	RunSpecs(t, "HTTPClient Suite")
}

var _ = Describe("DefaultClient", func() {
	var (
		client     httpclient.Client
		mockServer *httptest.Server
		ctx        context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
	})

	AfterEach(func() {
		if mockServer != nil {
			mockServer.Close()
		}
	})

	Describe("NewDefaultClient", func() {
		It("should create client with custom timeout", func() {
			client = httpclient.NewDefaultClient(5 * time.Second)
			Expect(client).NotTo(BeNil())
		})

		It("should use default timeout when zero is provided", func() {
			client = httpclient.NewDefaultClient(0)
			Expect(client).NotTo(BeNil())
		})
	})

	Describe("Get", func() {
		Context("Successful requests", func() {
			BeforeEach(func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Verify headers
					Expect(r.Header.Get("User-Agent")).To(Equal("toolhive-operator/1.0"))
					Expect(r.Header.Get("Accept")).To(Equal("application/json"))

					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{"message": "success"}`)
				}))
				client = httpclient.NewDefaultClient(30 * time.Second)
			})

			It("should successfully fetch data", func() {
				data, err := client.Get(ctx, mockServer.URL)
				Expect(err).NotTo(HaveOccurred())
				Expect(data).To(Equal([]byte(`{"message": "success"}`)))
			})

			It("should set correct headers", func() {
				_, err := client.Get(ctx, mockServer.URL)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("HTTP error responses", func() {
			BeforeEach(func() {
				client = httpclient.NewDefaultClient(30 * time.Second)
			})

			It("should handle 404 Not Found", func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, "Not Found")
				}))

				_, err := client.Get(ctx, mockServer.URL)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("HTTP 404"))
			})

			It("should handle 500 Internal Server Error", func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprint(w, "Internal Server Error")
				}))

				_, err := client.Get(ctx, mockServer.URL)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("HTTP 500"))
			})

			It("should handle 401 Unauthorized", func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, "Unauthorized")
				}))

				_, err := client.Get(ctx, mockServer.URL)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("HTTP 401"))
			})
		})

		Context("Network errors", func() {
			BeforeEach(func() {
				client = httpclient.NewDefaultClient(30 * time.Second)
			})

			It("should handle invalid URL", func() {
				_, err := client.Get(ctx, "://invalid-url")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to create request"))
			})

			It("should handle unreachable host", func() {
				_, err := client.Get(ctx, "http://invalid-host-does-not-exist.local:9999")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to execute request"))
			})
		})

		Context("Context cancellation", func() {
			BeforeEach(func() {
				// Create server that delays response
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					time.Sleep(2 * time.Second)
					w.WriteHeader(http.StatusOK)
				}))
				client = httpclient.NewDefaultClient(30 * time.Second)
			})

			It("should respect context cancellation", func() {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel() // Cancel immediately

				_, err := client.Get(cancelCtx, mockServer.URL)
				Expect(err).To(HaveOccurred())
			})

			It("should respect context timeout", func() {
				timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()

				_, err := client.Get(timeoutCtx, mockServer.URL)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("Response body handling", func() {
			BeforeEach(func() {
				client = httpclient.NewDefaultClient(30 * time.Second)
			})

			It("should handle empty response body", func() {
				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))

				data, err := client.Get(ctx, mockServer.URL)
				Expect(err).NotTo(HaveOccurred())
				Expect(data).To(BeEmpty())
			})

			It("should handle large response body", func() {
				largeData := make([]byte, 1024*1024) // 1MB
				for i := range largeData {
					largeData[i] = 'a'
				}

				mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(largeData)
				}))

				data, err := client.Get(ctx, mockServer.URL)
				Expect(err).NotTo(HaveOccurred())
				Expect(data).To(HaveLen(1024 * 1024))
			})
		})
	})
})

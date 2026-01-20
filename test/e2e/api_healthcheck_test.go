package e2e_test

import (
	"io"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Healthcheck API", Label("api", "healthcheck", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("GET /health", func() {
		Context("when the container runtime is available", func() {
			It("should return 204 No Content", func() {
				By("Making a GET request to /health endpoint")
				resp, err := apiServer.Get("/health")
				Expect(err).ToNot(HaveOccurred(), "Should be able to make GET request")
				defer resp.Body.Close()

				By("Verifying the response status code")
				Expect(resp.StatusCode).To(Equal(http.StatusNoContent),
					"Health endpoint should return 204 when runtime is available")

				By("Verifying the response body is empty")
				body, err := io.ReadAll(resp.Body)
				Expect(err).ToNot(HaveOccurred(), "Should be able to read response body")
				Expect(body).To(BeEmpty(), "Response body should be empty for 204 status")
			})

			It("should handle multiple concurrent requests", func() {
				const concurrentRequests = 10
				done := make(chan bool, concurrentRequests)

				By("Making multiple concurrent requests to /health")
				for i := 0; i < concurrentRequests; i++ {
					go func() {
						defer GinkgoRecover()
						resp, err := apiServer.Get("/health")
						Expect(err).ToNot(HaveOccurred())
						resp.Body.Close()
						Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
						done <- true
					}()
				}

				By("Waiting for all requests to complete")
				for i := 0; i < concurrentRequests; i++ {
					Eventually(done).Should(Receive())
				}
			})
		})

		Context("when checking response headers", func() {
			It("should not return Content-Type header for 204 response", func() {
				By("Making a GET request to /health endpoint")
				resp, err := apiServer.Get("/health")
				Expect(err).ToNot(HaveOccurred())
				defer resp.Body.Close()

				By("Checking that Content-Type header is not set for empty response")
				// For 204 responses, Content-Type should typically not be set
				// The server middleware sets Content-Type for /api/ paths only
				contentType := resp.Header.Get("Content-Type")
				Expect(contentType).ToNot(Equal("application/json"),
					"Content-Type should not be set to application/json for /health endpoint")
			})
		})
	})
})

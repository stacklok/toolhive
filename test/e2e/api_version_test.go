// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"io"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Version API", Label("api", "version", "e2e"), func() {
	var apiServer *e2e.Server

	BeforeEach(func() {
		config := e2e.NewServerConfig()
		apiServer = e2e.StartServer(config)
	})

	Describe("GET /api/v1beta/version", func() {
		It("should return version information", func() {
			resp := getVersion(apiServer)
			defer resp.Body.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})

		It("should return JSON content type", func() {
			resp := getVersion(apiServer)
			defer resp.Body.Close()

			Expect(resp.Header.Get("Content-Type")).To(Equal("application/json"))
		})

		It("should return a non-empty version string", func() {
			resp := getVersion(apiServer)
			defer resp.Body.Close()

			var versionResp versionAPIResponse
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())

			err = json.Unmarshal(body, &versionResp)
			Expect(err).NotTo(HaveOccurred())

			Expect(versionResp.Version).NotTo(BeEmpty())
		})

		It("should return a version matching expected format", func() {
			resp := getVersion(apiServer)
			defer resp.Body.Close()

			var versionResp versionAPIResponse
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())

			err = json.Unmarshal(body, &versionResp)
			Expect(err).NotTo(HaveOccurred())

			// Version should be either a semantic version (vX.Y.Z), "dev", or "build-<commit>"
			Expect(versionResp.Version).To(SatisfyAny(
				MatchRegexp(`^v\d+\.\d+\.\d+`),     // Semantic version
				MatchRegexp(`^build-[a-f0-9]{8}$`), // Build with commit hash
				Equal("dev"),                       // Development version
			))
		})
	})
})

// -----------------------------------------------------------------------------
// Response types
// -----------------------------------------------------------------------------

type versionAPIResponse struct {
	Version string `json:"version"`
}

// -----------------------------------------------------------------------------
// Helper functions
// -----------------------------------------------------------------------------

func getVersion(server *e2e.Server) *http.Response {
	resp, err := http.Get(server.BaseURL() + "/api/v1beta/version")
	Expect(err).NotTo(HaveOccurred())
	return resp
}

package httpclient_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/httpclient"
)

var _ = Describe("HTTPError", func() {
	Describe("NewHTTPError", func() {
		It("should create HTTPError with all fields", func() {
			err := httpclient.NewHTTPError(404, "http://example.com", "Not Found")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("HTTP 404"))
			Expect(err.Error()).To(ContainSubstring("http://example.com"))
			Expect(err.Error()).To(ContainSubstring("Not Found"))
		})

		It("should format error message correctly", func() {
			err := httpclient.NewHTTPError(500, "http://api.example.com/v1/data", "Internal Server Error")
			expected := "HTTP 500 for URL http://api.example.com/v1/data: Internal Server Error"
			Expect(err.Error()).To(Equal(expected))
		})

		It("should handle different status codes", func() {
			testCases := []struct {
				statusCode int
				message    string
			}{
				{200, "OK"},
				{201, "Created"},
				{400, "Bad Request"},
				{401, "Unauthorized"},
				{403, "Forbidden"},
				{404, "Not Found"},
				{500, "Internal Server Error"},
				{502, "Bad Gateway"},
				{503, "Service Unavailable"},
			}

			for _, tc := range testCases {
				err := httpclient.NewHTTPError(tc.statusCode, "http://test.com", tc.message)
				Expect(err.Error()).To(ContainSubstring(tc.message))
			}
		})

		It("should handle empty message", func() {
			err := httpclient.NewHTTPError(404, "http://example.com", "")
			Expect(err.Error()).To(Equal("HTTP 404 for URL http://example.com: "))
		})

		It("should handle long URLs", func() {
			longURL := "http://example.com/very/long/path/with/many/segments/that/goes/on/and/on"
			err := httpclient.NewHTTPError(404, longURL, "Not Found")
			Expect(err.Error()).To(ContainSubstring(longURL))
		})
	})
})

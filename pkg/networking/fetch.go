// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package networking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// DefaultMaxResponseSize is the default maximum response body size (1MB).
	DefaultMaxResponseSize = 1024 * 1024

	// DefaultErrorPreviewSize is the maximum size of error body preview in HTTPError.
	DefaultErrorPreviewSize = 1024

	// ContentTypeJSON is the JSON content type.
	ContentTypeJSON = "application/json"

	// ContentTypeFormURLEncoded is the form-urlencoded content type.
	ContentTypeFormURLEncoded = "application/x-www-form-urlencoded"
)

// FetchResult contains the result of a successful JSON fetch operation.
type FetchResult[T any] struct {
	// Data is the parsed JSON response body.
	Data T

	// StatusCode is the HTTP status code of the response.
	StatusCode int

	// Headers are the response headers.
	Headers http.Header

	// ContentType is the Content-Type header value.
	ContentType string
}

// HTTPError represents an HTTP error response with status code and body preview.
type HTTPError struct {
	// StatusCode is the HTTP status code.
	StatusCode int

	// Body is a preview of the response body (limited to DefaultErrorPreviewSize).
	Body string

	// URL is the requested URL.
	URL string
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP request to %s failed with status %d", e.URL, e.StatusCode)
}

// IsHTTPError checks if an error is an HTTPError with the specified status code.
// If statusCode is 0, it matches any HTTPError.
func IsHTTPError(err error, statusCode int) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if statusCode == 0 {
		return true
	}
	return httpErr.StatusCode == statusCode
}

// FetchOption configures a fetch request.
type FetchOption func(*fetchOptions)

// fetchOptions holds the configuration for a fetch request.
type fetchOptions struct {
	method                    string
	headers                   http.Header
	body                      io.Reader
	maxResponseSize           int64
	skipContentTypeValidation bool
	errorHandler              func(*http.Response, []byte) error
}

// newFetchOptions creates default fetch options.
func newFetchOptions() *fetchOptions {
	return &fetchOptions{
		method:          http.MethodGet,
		headers:         make(http.Header),
		maxResponseSize: DefaultMaxResponseSize,
	}
}

// WithMethod sets the HTTP method for the request.
func WithMethod(method string) FetchOption {
	return func(opts *fetchOptions) {
		opts.method = method
	}
}

// WithHeader adds a single header to the request.
func WithHeader(key, value string) FetchOption {
	return func(opts *fetchOptions) {
		opts.headers.Set(key, value)
	}
}

// WithHeaders adds multiple headers to the request.
// These headers are merged with any existing headers.
func WithHeaders(headers http.Header) FetchOption {
	return func(opts *fetchOptions) {
		for key, values := range headers {
			for _, value := range values {
				opts.headers.Add(key, value)
			}
		}
	}
}

// WithBody sets the request body.
func WithBody(body io.Reader) FetchOption {
	return func(opts *fetchOptions) {
		opts.body = body
	}
}

// WithMaxResponseSize sets the maximum response body size.
// If not set, DefaultMaxResponseSize (1MB) is used.
func WithMaxResponseSize(size int64) FetchOption {
	return func(opts *fetchOptions) {
		opts.maxResponseSize = size
	}
}

// WithoutContentTypeValidation disables Content-Type validation.
// By default, FetchJSON validates that the response Content-Type is application/json.
func WithoutContentTypeValidation() FetchOption {
	return func(opts *fetchOptions) {
		opts.skipContentTypeValidation = true
	}
}

// WithErrorHandler sets a custom error handler for non-200 responses.
// The handler receives the response and body, and should return an error.
// If the handler returns nil, the default HTTPError will be returned.
// This is useful for parsing structured error responses (e.g., OAuth error responses).
func WithErrorHandler(handler func(*http.Response, []byte) error) FetchOption {
	return func(opts *fetchOptions) {
		opts.errorHandler = handler
	}
}

// FetchJSON performs an HTTP request and parses the JSON response body.
// It sets the Accept header to application/json by default.
// For non-200 responses, it returns an HTTPError or the result of a custom error handler.
func FetchJSON[T any](
	ctx context.Context,
	client HTTPClient,
	requestURL string,
	opts ...FetchOption,
) (*FetchResult[T], error) {
	options := newFetchOptions()
	for _, opt := range opts {
		opt(options)
	}

	// Set default Accept header if not already set
	if options.headers.Get("Accept") == "" {
		options.headers.Set("Accept", ContentTypeJSON)
	}

	req, err := http.NewRequestWithContext(ctx, options.method, requestURL, options.body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Apply headers
	for key, values := range options.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read body with size limit
	body, err := io.ReadAll(io.LimitReader(resp.Body, options.maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Handle non-200 responses
	if resp.StatusCode != http.StatusOK {
		// Try custom error handler first
		if options.errorHandler != nil {
			if customErr := options.errorHandler(resp, body); customErr != nil {
				return nil, customErr
			}
		}

		// Fall back to default HTTPError
		bodyPreview := string(body)
		if len(bodyPreview) > DefaultErrorPreviewSize {
			bodyPreview = bodyPreview[:DefaultErrorPreviewSize]
		}
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Body:       bodyPreview,
			URL:        requestURL,
		}
	}

	// Validate Content-Type for successful responses
	if !options.skipContentTypeValidation {
		contentType := resp.Header.Get("Content-Type")
		if !strings.Contains(strings.ToLower(contentType), ContentTypeJSON) {
			return nil, fmt.Errorf("unexpected content type: %s", contentType)
		}
	}

	// Parse JSON response
	var data T
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return &FetchResult[T]{
		Data:        data,
		StatusCode:  resp.StatusCode,
		Headers:     resp.Header,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// FetchJSONWithForm performs a POST request with form-urlencoded body and parses JSON response.
// This is a convenience wrapper around FetchJSON for token endpoints and similar APIs.
// It sets Content-Type to application/x-www-form-urlencoded and Accept to application/json.
func FetchJSONWithForm[T any](
	ctx context.Context,
	client HTTPClient,
	requestURL string,
	formData url.Values,
	opts ...FetchOption,
) (*FetchResult[T], error) {
	// Prepend form-specific options
	formOpts := []FetchOption{
		WithMethod(http.MethodPost),
		WithHeader("Content-Type", ContentTypeFormURLEncoded),
		WithBody(strings.NewReader(formData.Encode())),
	}

	// Append user options (they can override form options if needed)
	allOpts := append(formOpts, opts...)

	return FetchJSON[T](ctx, client, requestURL, allOpts...)
}

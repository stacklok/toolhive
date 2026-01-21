// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// maxResponseSize is the maximum response body size (1MB).
	maxResponseSize = 1024 * 1024

	// contentTypeJSON is the JSON content type.
	contentTypeJSON = "application/json"

	// contentTypeFormURLEncoded is the form-urlencoded content type.
	contentTypeFormURLEncoded = "application/x-www-form-urlencoded"
)

// FetchResult contains the result of a successful JSON fetch operation.
type FetchResult[T any] struct {
	// Data is the parsed JSON response body.
	Data T

	// Headers are the response headers.
	Headers http.Header
}

// FetchOption configures a fetch request.
type FetchOption func(*fetchOptions)

// fetchOptions holds the configuration for a fetch request.
type fetchOptions struct {
	method       string
	headers      http.Header
	body         io.Reader
	errorHandler func(*http.Response, []byte) error
}

// newFetchOptions creates default fetch options.
func newFetchOptions() *fetchOptions {
	return &fetchOptions{
		method:  http.MethodGet,
		headers: make(http.Header),
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

// WithBody sets the request body.
func WithBody(body io.Reader) FetchOption {
	return func(opts *fetchOptions) {
		opts.body = body
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
		options.headers.Set("Accept", contentTypeJSON)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
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

		// Fall back to default HTTPError using status text to avoid leaking sensitive body content
		return nil, NewHTTPError(resp.StatusCode, requestURL, resp.Status)
	}

	// Validate Content-Type for successful responses
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(contentType), contentTypeJSON) {
		return nil, fmt.Errorf("unexpected content type: %s", contentType)
	}

	// Parse JSON response
	var data T
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return &FetchResult[T]{
		Data:    data,
		Headers: resp.Header,
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
		WithHeader("Content-Type", contentTypeFormURLEncoded),
		WithBody(strings.NewReader(formData.Encode())),
	}

	// Append user options (they can override form options if needed)
	allOpts := append(formOpts, opts...)

	return FetchJSON[T](ctx, client, requestURL, allOpts...)
}

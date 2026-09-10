// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	// DefaultTimeout prevents calls made through NewClient from hanging indefinitely.
	DefaultTimeout = 30 * time.Second
	// DefaultMaxResponseBodyBytes is the maximum response body accepted by NewClient.
	DefaultMaxResponseBodyBytes int64 = 10 << 20
)

var (
	// ErrInvalidServerURL reports that a client was given a non-absolute HTTP(S) URL.
	ErrInvalidServerURL = errors.New("ToolHive SDK server URL must be an absolute HTTP(S) URL")
	// ErrResponseBodyTooLarge reports that a bounded client received a response body larger than its limit.
	ErrResponseBodyTooLarge = errors.New("ToolHive SDK response body exceeds configured limit")
)

type defaultClientConfig struct {
	httpClient *http.Client
}

// DefaultClientOption configures NewDefaultClient. New code should use NewClient with WithClient.
type DefaultClientOption func(*defaultClientConfig)

// WithHTTPClient preserves the caller's HTTP and authentication transport while NewDefaultClient applies its timeout
// and response-size policies. The supplied client is copied and is not modified.
func WithHTTPClient(httpClient *http.Client) DefaultClientOption {
	return func(config *defaultClientConfig) {
		if httpClient != nil {
			config.httpClient = httpClient
		}
	}
}

// NewClient creates a generated API client with a validated HTTP(S) URL, a 30-second request deadline, and a 10 MiB
// response-body limit. Caller-provided clients passed through WithClient retain their transport, authentication, and
// other settings; requests without an earlier caller deadline receive DefaultTimeout. Every response is size-bounded
// and returns ErrResponseBodyTooLarge rather than silently truncating data.
func NewClient(serverURL string, options ...ClientOption) (*Client, error) {
	if err := validateServerURL(serverURL); err != nil {
		return nil, err
	}

	client, err := NewUnsafeClient(serverURL, options...)
	if err != nil {
		return nil, err
	}
	client.cfg.Client = boundedClient{client: client.cfg.Client}
	return client, nil
}

// NewDefaultClient creates a client with NewClient's default policy. It is retained for compatibility; use NewClient
// with WithClient for new code.
func NewDefaultClient(serverURL string, options ...DefaultClientOption) (*Client, error) {
	config := defaultClientConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	if config.httpClient == nil {
		return NewClient(serverURL)
	}
	return NewClient(serverURL, WithClient(config.httpClient))
}

func validateServerURL(serverURL string) error {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidServerURL, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%w: %q", ErrInvalidServerURL, serverURL)
	}
	return nil
}

type boundedClient struct {
	client interface {
		Do(*http.Request) (*http.Response, error)
	}
}

func (c boundedClient) Do(request *http.Request) (*http.Response, error) {
	contextWithDeadline, cancel := requestContext(request.Context())
	boundedRequest := request.Clone(contextWithDeadline)
	response, err := c.client.Do(boundedRequest)
	if err != nil {
		cancel()
		return response, err
	}
	if response.Body == nil {
		cancel()
		return response, nil
	}
	response.Body = &limitedReadCloser{
		ReadCloser: response.Body,
		remaining:  DefaultMaxResponseBodyBytes,
		cancel:     cancel,
	}
	return response, nil
}

func requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, DefaultTimeout)
}

type limitedReadCloser struct {
	io.ReadCloser
	remaining int64
	cancel    context.CancelFunc
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, ErrResponseBodyTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (r *limitedReadCloser) Close() error {
	r.cancel()
	return r.ReadCloser.Close()
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

func TestClassifyPullError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "nil error returns 200",
			err:  nil,
			want: http.StatusOK,
		},
		{
			name: "context deadline exceeded maps to 504",
			err:  context.DeadlineExceeded,
			want: http.StatusGatewayTimeout,
		},
		{
			name: "wrapped context deadline exceeded maps to 504",
			err:  fmt.Errorf("pulling OCI artifact: %w", context.DeadlineExceeded),
			want: http.StatusGatewayTimeout,
		},
		{
			name: "context canceled maps to 504",
			err:  context.Canceled,
			want: http.StatusGatewayTimeout,
		},
		{
			name: "registry 401 maps to 401",
			err:  newErrResp(http.StatusUnauthorized),
			want: http.StatusUnauthorized,
		},
		{
			name: "registry 403 maps to 401",
			err:  newErrResp(http.StatusForbidden),
			want: http.StatusUnauthorized,
		},
		{
			name: "registry 404 maps to 404",
			err:  newErrResp(http.StatusNotFound),
			want: http.StatusNotFound,
		},
		{
			name: "registry 429 maps to 429",
			err:  newErrResp(http.StatusTooManyRequests),
			want: http.StatusTooManyRequests,
		},
		{
			name: "registry 400 maps to 502",
			err:  newErrResp(http.StatusBadRequest),
			want: http.StatusBadGateway,
		},
		{
			name: "registry 500 maps to 502",
			err:  newErrResp(http.StatusInternalServerError),
			want: http.StatusBadGateway,
		},
		{
			name: "registry 503 maps to 502",
			err:  newErrResp(http.StatusServiceUnavailable),
			want: http.StatusBadGateway,
		},
		{
			name: "wrapped registry 401 maps to 401",
			err:  fmt.Errorf("copy graph: %w", newErrResp(http.StatusUnauthorized)),
			want: http.StatusUnauthorized,
		},
		{
			name: "errdef.ErrNotFound maps to 404",
			err:  errdef.ErrNotFound,
			want: http.StatusNotFound,
		},
		{
			name: "wrapped errdef.ErrNotFound maps to 404",
			err:  fmt.Errorf("fetching manifest: %w", errdef.ErrNotFound),
			want: http.StatusNotFound,
		},
		{
			name: "generic error maps to 502",
			err:  errors.New("connection refused"),
			want: http.StatusBadGateway,
		},
		{
			name: "registry error takes precedence over generic wrapper",
			err: fmt.Errorf("pulling from registry: %w",
				fmt.Errorf("wrapped: %w", newErrResp(http.StatusNotFound))),
			want: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyPullError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// newErrResp constructs a *errcode.ErrorResponse with the given HTTP status
// for use as a synthetic oras-go error.
func newErrResp(status int) *errcode.ErrorResponse {
	u, _ := url.Parse("https://registry.example.com/v2/foo/manifests/latest")
	return &errcode.ErrorResponse{
		Method:     http.MethodGet,
		URL:        u,
		StatusCode: status,
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookErrors(t *testing.T) {
	t.Parallel()

	underlyingErr := fmt.Errorf("connection refused")

	tests := []struct {
		name           string
		err            error
		expectedMsg    string
		isTimeout      bool
		isNetwork      bool
		isInvalidResp  bool
		unwrapsToInner bool
	}{
		{
			name:           "TimeoutError",
			err:            NewTimeoutError("my-webhook", underlyingErr),
			expectedMsg:    `webhook "my-webhook": timeout: connection refused`,
			isTimeout:      true,
			unwrapsToInner: true,
		},
		{
			name:           "NetworkError",
			err:            NewNetworkError("my-webhook", underlyingErr),
			expectedMsg:    `webhook "my-webhook": network error: connection refused`,
			isNetwork:      true,
			unwrapsToInner: true,
		},
		{
			name:           "InvalidResponseError",
			err:            NewInvalidResponseError("my-webhook", underlyingErr, 0),
			expectedMsg:    `webhook "my-webhook": invalid response: connection refused`,
			isInvalidResp:  true,
			unwrapsToInner: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expectedMsg, tt.err.Error())

			// Test errors.As for each type.
			var timeoutErr *TimeoutError
			assert.Equal(t, tt.isTimeout, errors.As(tt.err, &timeoutErr))

			var networkErr *NetworkError
			assert.Equal(t, tt.isNetwork, errors.As(tt.err, &networkErr))

			var invalidRespErr *InvalidResponseError
			assert.Equal(t, tt.isInvalidResp, errors.As(tt.err, &invalidRespErr))

			// Test Unwrap chain reaches the underlying error.
			if tt.unwrapsToInner {
				require.True(t, errors.Is(tt.err, underlyingErr),
					"expected error to unwrap to underlying error")
			}
		})
	}
}

func TestWebhookErrorBaseType(t *testing.T) {
	t.Parallel()

	inner := fmt.Errorf("some error")
	err := &WebhookError{WebhookName: "base-test", Err: inner}

	assert.Equal(t, `webhook "base-test": some error`, err.Error())
	assert.Equal(t, inner, err.Unwrap())
}

func TestIsAlwaysDenyError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "unprocessable entity invalid response",
			err:  NewInvalidResponseError("test", fmt.Errorf("unprocessable"), 422),
			want: true,
		},
		{
			name: "other invalid response status",
			err:  NewInvalidResponseError("test", fmt.Errorf("bad request"), 400),
			want: false,
		},
		{
			name: "invalid response without status",
			err:  NewInvalidResponseError("test", fmt.Errorf("decode error"), 0),
			want: false,
		},
		{
			name: "non invalid response error",
			err:  NewNetworkError("test", fmt.Errorf("network")),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsAlwaysDenyError(tt.err))
		})
	}
}

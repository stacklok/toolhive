// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oautherr_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/oautherr"
)

func retrieveErr(status int, code string) *oauth2.RetrieveError {
	return &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: status},
		ErrorCode: code,
	}
}

func TestIsTransientRetrieveError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *oauth2.RetrieveError
		want bool
	}{
		{"nil error", nil, false},
		{"nil response carries no signal", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}, false},
		{"500 is a server-side blip", retrieveErr(http.StatusInternalServerError, ""), true},
		{"503 is a server-side blip", retrieveErr(http.StatusServiceUnavailable, "temporarily_unavailable"), true},
		{"429 is transient regardless of body", retrieveErr(http.StatusTooManyRequests, "invalid_grant"), true},
		{"4xx without a code is infrastructure", retrieveErr(http.StatusForbidden, ""), true},
		{"invalid_grant is a verdict", retrieveErr(http.StatusBadRequest, "invalid_grant"), false},
		{"invalid_client is a verdict", retrieveErr(http.StatusUnauthorized, "invalid_client"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, oautherr.IsTransientRetrieveError(tt.err))
		})
	}
}

func TestIsPermanentCredentialError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated error", errors.New("keyring is locked"), false},
		{"nil response is not a verdict", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}, false},
		{"5xx is not a verdict", retrieveErr(http.StatusBadGateway, ""), false},
		{"429 is not a verdict", retrieveErr(http.StatusTooManyRequests, "invalid_grant"), false},
		{"4xx without a code is not a verdict", retrieveErr(http.StatusForbidden, ""), false},
		{"invalid_grant is a verdict", retrieveErr(http.StatusBadRequest, "invalid_grant"), true},
		{"wrapped verdict is still found", fmt.Errorf("refresh failed: %w", retrieveErr(http.StatusBadRequest, "invalid_grant")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, oautherr.IsPermanentCredentialError(tt.err))
		})
	}
}

func TestIsRejectedRefreshGrant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated error", errors.New("keyring is locked"), false},
		{"invalid_grant is the credential verdict", retrieveErr(http.StatusBadRequest, "invalid_grant"), true},
		{"wrapped invalid_grant is still found",
			fmt.Errorf("refresh failed: %w", retrieveErr(http.StatusBadRequest, "invalid_grant")), true},

		// Permanent, but a verdict on the client rather than the credential:
		// a fresh login reproduces these unchanged.
		{"invalid_client indicts the registration", retrieveErr(http.StatusUnauthorized, "invalid_client"), false},
		{"unauthorized_client indicts the registration", retrieveErr(http.StatusBadRequest, "unauthorized_client"), false},
		{"invalid_scope indicts the request", retrieveErr(http.StatusBadRequest, "invalid_scope"), false},

		// Shapes the transient classifier already excuses stay excused, even
		// when the body claims invalid_grant.
		{"429 is transient regardless of body", retrieveErr(http.StatusTooManyRequests, "invalid_grant"), false},
		{"5xx is transient regardless of body", retrieveErr(http.StatusBadGateway, "invalid_grant"), false},
		{"nil response carries no signal", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}, false},

		// The 'error' field is arbitrary server-chosen text; only the exact
		// literal counts.
		{"a code merely containing invalid_grant does not count",
			retrieveErr(http.StatusBadRequest, "invalid_grant\r\nX-Injected: 1"), false},
		{"empty code is not a verdict", retrieveErr(http.StatusForbidden, ""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, oautherr.IsRejectedRefreshGrant(tt.err))
		})
	}
}

func TestIsParseError(t *testing.T) {
	t.Parallel()

	assert.False(t, oautherr.IsParseError(nil))
	assert.False(t, oautherr.IsParseError(errors.New("some other failure")))
	assert.True(t, oautherr.IsParseError(errors.New("oauth2: cannot parse json")))
	assert.True(t, oautherr.IsParseError(fmt.Errorf("wrapped: %w", errors.New("oauth2: cannot parse response"))))
}

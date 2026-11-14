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

	legacy "github.com/stacklok/toolhive/pkg/auth/oautherr"
	"github.com/stacklok/toolhive/pkg/oauthproto/oautherr"
)

func TestCompatibilityForwarding(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"", "invalid_grant", "invalid_client"} {
		for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("%d/%s", status, code), func(t *testing.T) {
				t.Parallel()
				cause := &oauth2.RetrieveError{
					Response:  &http.Response{StatusCode: status},
					ErrorCode: code,
				}
				err := fmt.Errorf("wrapped: %w", cause)
				assert.Equal(t, oautherr.IsTransientRetrieveError(cause), legacy.IsTransientRetrieveError(cause))
				assert.Equal(t, oautherr.IsPermanentCredentialError(err), legacy.IsPermanentCredentialError(err))
				assert.Equal(t, oautherr.IsRejectedRefreshGrant(err), legacy.IsRejectedRefreshGrant(err))
			})
		}
	}

	assert.Equal(t, oautherr.IsTransientRetrieveError(nil), legacy.IsTransientRetrieveError(nil))
	for _, err := range []error{nil, errors.New("unrelated"), errors.New("oauth2: cannot parse json")} {
		assert.Equal(t, oautherr.IsParseError(err), legacy.IsParseError(err))
		assert.Equal(t, oautherr.IsPermanentCredentialError(err), legacy.IsPermanentCredentialError(err))
		assert.Equal(t, oautherr.IsRejectedRefreshGrant(err), legacy.IsRejectedRefreshGrant(err))
	}
}

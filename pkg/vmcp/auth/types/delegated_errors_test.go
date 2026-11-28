// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestDelegatedAuthRequiredError(t *testing.T) {
	t.Parallel()

	cause := errors.New("private cause")
	err := authtypes.NewDelegatedAuthRequiredError("github", cause)
	assert.Equal(t, "github", err.Provider)
	assert.EqualError(t, err, `provider "github" requires upstream authorization`)
	assert.ErrorIs(t, err, cause)
	assert.NoError(t, errors.Unwrap(authtypes.NewDelegatedAuthRequiredError("github", nil)))
	assert.EqualError(t, authtypes.NewDelegatedAuthRequiredError("", nil), "upstream authorization required")
}

func TestDelegatedCredentialUnavailable(t *testing.T) {
	t.Parallel()

	assert.EqualError(t, authtypes.ErrDelegatedCredentialUnavailable, "upstream credential unavailable")
}

func TestDelegatedCredentialError(t *testing.T) {
	t.Parallel()

	authReq := authtypes.NewDelegatedAuthRequiredError("github", nil)
	unavailable := authtypes.ErrDelegatedCredentialUnavailable
	var nilAuthReq *authtypes.DelegatedAuthRequiredError
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{name: "bare authorization required", err: authReq, want: authReq},
		{name: "wrapped authorization required", err: fmt.Errorf("authenticate: %w", authReq), want: authReq},
		{name: "bare unavailable", err: unavailable, want: unavailable},
		{name: "wrapped unavailable", err: fmt.Errorf("authenticate: %w", unavailable), want: unavailable},
		{name: "authorization required wins over unavailable", err: errors.Join(unavailable, authReq), want: authReq},
		{name: "typed-nil authorization required", err: nilAuthReq},
		{name: "typed-nil authorization required does not mask unavailable", err: errors.Join(nilAuthReq, unavailable), want: unavailable},
		{name: "non-delegated", err: authtypes.ErrUpstreamTokenNotFound},
		{name: "nil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := authtypes.DelegatedCredentialError(tc.err)
			if tc.want == nil {
				assert.NoError(t, got)
				return
			}
			assert.Same(t, tc.want, got)
		})
	}
}

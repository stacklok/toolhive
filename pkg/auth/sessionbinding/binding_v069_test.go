// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionbinding

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

// These functions are frozen from release v0.69.0, with calls redirected to
// the frozen helpers so changes to the current implementation cannot affect them.
func formatV069(iss, sub string) (string, error) {
	if iss == "" || sub == "" || strings.ContainsRune(iss, '\x00') || strings.ContainsRune(sub, '\x00') {
		return "", ErrInvalidBinding
	}
	return iss + "\x00" + sub, nil
}

func parseV069(value string) (iss, sub string, ok bool) {
	iss, sub, ok = strings.Cut(value, "\x00")
	if !ok || iss == "" || sub == "" || strings.ContainsRune(sub, '\x00') {
		return "", "", false
	}
	return iss, sub, true
}

func fromIdentityV069(identity *auth.Identity) (string, error) {
	if identity == nil {
		return UnauthenticatedSentinel, nil
	}
	iss, _ := identity.Claims["iss"].(string)
	sub, _ := identity.Claims["sub"].(string)
	return formatV069(iss, sub)
}

func TestBindingV069Compatibility(t *testing.T) {
	t.Parallel()
	interactive := delegatedIdentity(false)
	interactive.AdmissionRoute = auth.AdmissionRouteInteractive
	delegated := delegatedIdentity(true)
	for _, tc := range []struct {
		name     string
		identity *auth.Identity
	}{
		{"without actor", delegatedIdentity(false)},
		{"with actor", delegated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stored, err := FromIdentity(tc.identity)
			require.NoError(t, err)
			_, _, ok := parseV069(stored)
			require.False(t, ok, "v0.69.0 must reject the delegated form")
		})
	}
	t.Run("old binding", func(t *testing.T) {
		t.Parallel()
		stored, err := fromIdentityV069(interactive)
		require.NoError(t, err)
		require.NoError(t, Validate(stored, interactive))
		require.ErrorIs(t, Validate(stored, delegated), ErrNotFound)
	})
}

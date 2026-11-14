// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func alwaysConfirm() (bool, error) { return true, nil }
func neverConfirm() (bool, error)  { return false, nil }

func TestAcquireExplicitFlagAlwaysWins(t *testing.T) {
	t.Parallel()

	// Regression case: an explicit --identity-token must be resolved and
	// forwarded even when --key is also set, so the server (not the
	// client) reports the conflict. It must never be silently dropped.
	token, err := Acquire(t.Context(), Options{
		FlagValue: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig",
		Key:       "/tmp/cosign.key",
		Confirm:   neverConfirm,
	})
	require.NoError(t, err)
	assert.Equal(t, "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig", token)
}

func TestAcquireExplicitKeyOrNoSignSkipsAcquisition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts Options
	}{
		{name: "key set", opts: Options{Key: "/tmp/cosign.key"}},
		{name: "no_sign set", opts: Options{NoSign: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.opts.Confirm = func() (bool, error) {
				t.Fatal("Confirm must not be called when an explicit signing choice was made")
				return false, nil
			}
			token, err := Acquire(t.Context(), tc.opts)
			require.NoError(t, err)
			assert.Empty(t, token)
		})
	}
}

// TestAcquireConsultsConfirmWhenNoAmbientToken proves Confirm is only
// reached once ambient acquisition is unavailable — declining it surfaces
// ErrNoCredential without ever attempting a real interactive sign-in.
// Acquire hardcodes oauthflow.DefaultIDTokenGetter for the confirmed=true
// case, so that branch isn't exercised here — see TestInteractive, which
// verifies the actual OIDConnect wiring via an injected StaticTokenGetter.
func TestAcquireConsultsConfirmWhenNoAmbientToken(t *testing.T) {
	// Not parallel: Ambient reads process env vars via t.Setenv.
	t.Setenv(envRequestURL, "")
	t.Setenv(envRequestToken, "")

	called := false
	confirm := func() (bool, error) {
		called = true
		return false, nil
	}

	_, err := Acquire(t.Context(), Options{Confirm: confirm})
	require.ErrorIs(t, err, ErrNoCredential)
	assert.True(t, called, "Confirm must be consulted once ambient acquisition is unavailable")
}

func TestAcquireConfirmErrorPropagates(t *testing.T) {
	t.Setenv(envRequestURL, "")
	t.Setenv(envRequestToken, "")

	wantErr := errors.New("reading stdin failed")
	_, err := Acquire(t.Context(), Options{
		Confirm: func() (bool, error) { return false, wantErr },
	})
	require.ErrorIs(t, err, wantErr)
}

func TestAcquireRequiresConfirmCallback(t *testing.T) {
	t.Parallel()

	_, err := Acquire(t.Context(), Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Confirm is required")
}

func TestAcquireInvalidFlagValuePropagatesResolveError(t *testing.T) {
	t.Parallel()

	_, err := Acquire(t.Context(), Options{
		FlagValue: "/does/not/exist/and-not-a-jwt",
		Confirm:   alwaysConfirm,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither a readable file nor a JWT")
}

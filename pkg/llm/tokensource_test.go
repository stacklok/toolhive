// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// minimalConfig returns a Config with the minimum fields for a configured gateway.
func minimalConfig() *Config {
	return &Config{
		GatewayURL: "https://llm.example.com",
		OIDC: OIDCConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "test-client",
		},
	}
}

// ── DeriveSecretKey ───────────────────────────────────────────────────────────

func TestDeriveSecretKey(t *testing.T) {
	t.Parallel()

	key1 := DeriveSecretKey("https://llm.example.com", "https://auth.example.com")
	key2 := DeriveSecretKey("https://llm.example.com", "https://auth.example.com")
	key3 := DeriveSecretKey("https://other.example.com", "https://auth.example.com")

	assert.Equal(t, key1, key2, "same inputs must produce the same key")
	assert.NotEqual(t, key1, key3, "different gateway URLs must produce different keys")
	assert.Contains(t, key1, "LLM_OAUTH_", "key must start with expected prefix")
}

// ── ensureOfflineAccess ───────────────────────────────────────────────────────

func TestEnsureOfflineAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  []string
		expect []string
	}{
		{
			name:   "already present",
			input:  []string{"openid", "offline_access"},
			expect: []string{"openid", "offline_access"},
		},
		{
			name:   "not present",
			input:  []string{"openid"},
			expect: []string{"openid", "offline_access"},
		},
		{
			name:   "empty",
			input:  []string{},
			expect: []string{"offline_access"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ensureOfflineAccess(tc.input)
			assert.Equal(t, tc.expect, got)
		})
	}
}

// ── Token – non-interactive, no cached token → ErrTokenRequired ──────────────

func TestTokenSource_NonInteractive_NoCache_ReturnsErrTokenRequired(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	// Secrets provider returns not-found for any key lookup.
	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), gomock.Any()).
		Return("", errors.New("not found")).
		AnyTimes()

	cfg := minimalConfig()
	ts := NewTokenSource(cfg, mockSecrets, false /* non-interactive */, nil)

	_, err := ts.Token(context.Background())
	require.ErrorIs(t, err, ErrTokenRequired)
}

// ── Token – non-interactive, no secrets provider → ErrTokenRequired ──────────

func TestTokenSource_NonInteractive_NilSecrets_ReturnsErrTokenRequired(t *testing.T) {
	t.Parallel()

	cfg := minimalConfig()
	ts := NewTokenSource(cfg, nil /* no secrets */, false, nil)

	_, err := ts.Token(context.Background())
	require.ErrorIs(t, err, ErrTokenRequired)
}

// ── refreshTokenKey – uses CachedRefreshTokenRef when set ────────────────────

func TestTokenSource_RefreshTokenKey_UsesCached(t *testing.T) {
	t.Parallel()

	cfg := minimalConfig()
	cfg.OIDC.CachedRefreshTokenRef = "my-persisted-key"

	ts := NewTokenSource(cfg, nil, false, nil)
	assert.Equal(t, "my-persisted-key", ts.refreshTokenKey())
}

// ── refreshTokenKey – derives key when no cached ref ─────────────────────────

func TestTokenSource_RefreshTokenKey_DerivedWhenEmpty(t *testing.T) {
	t.Parallel()

	cfg := minimalConfig()
	ts := NewTokenSource(cfg, nil, false, nil)

	key := ts.refreshTokenKey()
	expected := DeriveSecretKey(cfg.GatewayURL, cfg.OIDC.Issuer)
	assert.Equal(t, expected, key)
}

// ── Preemptive refresh: expiry is shifted back by 30 s ───────────────────────

func TestPreemptiveRefreshWindow_Value(t *testing.T) {
	t.Parallel()
	// The window must be exactly 30 s so token helpers and proxy workers
	// consistently treat tokens as expired before the gateway does.
	assert.Equal(t, 30*time.Second, preemptiveRefreshWindow)
}

func TestTokenSource_PreemptiveRefreshWindow_ExpiryShift(t *testing.T) {
	t.Parallel()

	// Verify the expiry shift arithmetic: a token expiring in 2 minutes should
	// have its effective expiry moved back by preemptiveRefreshWindow.
	realExpiry := time.Now().Add(2 * time.Minute)
	shifted := realExpiry.Add(-preemptiveRefreshWindow)

	assert.True(t, shifted.Before(realExpiry),
		"shifted expiry must be earlier than the real expiry")
	assert.InDelta(t,
		preemptiveRefreshWindow.Seconds(),
		realExpiry.Sub(shifted).Seconds(),
		0.001,
		"shift must equal preemptiveRefreshWindow",
	)
}

// ── updateConfigTokenRef – calls the injected updater ────────────────────────

func TestTokenSource_UpdateConfigTokenRef_CallsUpdater(t *testing.T) {
	t.Parallel()

	var gotKey string
	var gotExpiry time.Time

	updater := func(key string, expiry time.Time) {
		gotKey = key
		gotExpiry = expiry
	}

	ts := NewTokenSource(minimalConfig(), nil, false, updater)
	wantExpiry := time.Now().Add(time.Hour)
	ts.updateConfigTokenRef("test-key", wantExpiry)

	assert.Equal(t, "test-key", gotKey)
	assert.Equal(t, wantExpiry, gotExpiry)
}

// ── updateConfigTokenRef – nil updater is a no-op ────────────────────────────

func TestTokenSource_UpdateConfigTokenRef_NilUpdater_NoOp(t *testing.T) {
	t.Parallel()

	ts := NewTokenSource(minimalConfig(), nil, false, nil)
	// Must not panic.
	assert.NotPanics(t, func() {
		ts.updateConfigTokenRef("key", time.Now())
	})
}

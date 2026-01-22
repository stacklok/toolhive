// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// mockTokenSource is a test implementation of oauth2.TokenSource
type mockTokenSource struct {
	tokens    []*oauth2.Token
	callCount int
	err       error
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.callCount >= len(m.tokens) {
		return m.tokens[len(m.tokens)-1], nil
	}
	token := m.tokens[m.callCount]
	m.callCount++
	return token, nil
}

func TestPersistingTokenSource_Token(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		tokens         []*oauth2.Token
		sourceErr      error
		wantPersisted  int
		wantErr        bool
		wantErrContain string
	}{
		{
			name: "persists token on first call",
			tokens: []*oauth2.Token{
				{AccessToken: "token1", RefreshToken: "refresh1", Expiry: time.Now().Add(time.Hour)},
			},
			wantPersisted: 1,
		},
		{
			name: "persists token when refreshed",
			tokens: []*oauth2.Token{
				{AccessToken: "token1", RefreshToken: "refresh1", Expiry: time.Now().Add(time.Hour)},
				{AccessToken: "token2", RefreshToken: "refresh2", Expiry: time.Now().Add(2 * time.Hour)},
			},
			wantPersisted: 2,
		},
		{
			name: "does not persist when token unchanged",
			tokens: []*oauth2.Token{
				{AccessToken: "token1", RefreshToken: "refresh1", Expiry: time.Now().Add(time.Hour)},
				{AccessToken: "token1", RefreshToken: "refresh1", Expiry: time.Now().Add(time.Hour)},
			},
			wantPersisted: 1, // Only first call persists
		},
		{
			name:           "returns error from underlying source",
			sourceErr:      errors.New("token source error"),
			wantErr:        true,
			wantErrContain: "token source error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := &mockTokenSource{
				tokens: tt.tokens,
				err:    tt.sourceErr,
			}

			var persistCount atomic.Int32
			persister := func(_ string, _ time.Time) error {
				persistCount.Add(1)
				return nil
			}

			pts := NewPersistingTokenSource(source, persister)

			// Call Token() for each token in the list
			callCount := len(tt.tokens)
			if callCount == 0 {
				callCount = 1
			}

			for i := 0; i < callCount; i++ {
				token, err := pts.Token()
				if tt.wantErr {
					require.Error(t, err)
					if tt.wantErrContain != "" {
						assert.Contains(t, err.Error(), tt.wantErrContain)
					}
					return
				}
				require.NoError(t, err)
				assert.NotNil(t, token)
			}

			assert.Equal(t, int32(tt.wantPersisted), persistCount.Load())
		})
	}
}

func TestPersistingTokenSource_PersisterError(t *testing.T) {
	t.Parallel()

	source := &mockTokenSource{
		tokens: []*oauth2.Token{
			{AccessToken: "token1", RefreshToken: "refresh1", Expiry: time.Now().Add(time.Hour)},
		},
	}

	// Persister that always fails
	persister := func(_ string, _ time.Time) error {
		return errors.New("persistence failed")
	}

	pts := NewPersistingTokenSource(source, persister)

	// Token should still be returned even if persistence fails
	token, err := pts.Token()
	require.NoError(t, err)
	assert.Equal(t, "token1", token.AccessToken)
}

func TestPersistingTokenSource_NilPersister(t *testing.T) {
	t.Parallel()

	source := &mockTokenSource{
		tokens: []*oauth2.Token{
			{AccessToken: "token1", RefreshToken: "refresh1", Expiry: time.Now().Add(time.Hour)},
		},
	}

	// Create with nil persister
	pts := NewPersistingTokenSource(source, nil)

	// Should work without error
	token, err := pts.Token()
	require.NoError(t, err)
	assert.Equal(t, "token1", token.AccessToken)
}

func TestConfig_HasValidCachedTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
		want   bool
	}{
		{
			name: "returns true when refresh token ref exists",
			config: Config{
				CachedRefreshTokenRef: "OAUTH_REFRESH_TOKEN_test",
			},
			want: true,
		},
		{
			name: "returns true when refresh token ref and expiry exist",
			config: Config{
				CachedRefreshTokenRef: "OAUTH_REFRESH_TOKEN_test",
				CachedTokenExpiry:     time.Now().Add(time.Hour),
			},
			want: true,
		},
		{
			name:   "returns false when no token ref exists",
			config: Config{},
			want:   false,
		},
		{
			name: "returns false when only expiry exists",
			config: Config{
				CachedTokenExpiry: time.Now().Add(time.Hour),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.config.HasValidCachedTokens())
		})
	}
}

func TestConfig_ClearCachedTokens(t *testing.T) {
	t.Parallel()

	config := Config{
		CachedRefreshTokenRef: "OAUTH_REFRESH_TOKEN_test",
		CachedTokenExpiry:     time.Now().Add(time.Hour),
	}

	config.ClearCachedTokens()

	assert.Empty(t, config.CachedRefreshTokenRef)
	assert.True(t, config.CachedTokenExpiry.IsZero())
}

func TestCreateTokenSourceFromCached(t *testing.T) {
	t.Parallel()

	// This test verifies that CreateTokenSourceFromCached creates a valid token source
	// Note: We can't fully test the refresh behavior without a real OAuth server
	oauth2Config := &oauth2.Config{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://example.com/auth",
			TokenURL: "https://example.com/token",
		},
	}

	tokenSource := CreateTokenSourceFromCached(
		oauth2Config,
		"refresh_token",
		time.Now().Add(time.Hour),
	)

	assert.NotNil(t, tokenSource)
}

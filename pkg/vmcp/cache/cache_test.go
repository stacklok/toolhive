package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCachedToken_IsExpired(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "expired one hour ago",
			expiresAt: now.Add(-1 * time.Hour),
			want:      true,
		},
		{
			name:      "expired one millisecond ago",
			expiresAt: now.Add(-1 * time.Millisecond),
			want:      true,
		},
		{
			name:      "expires in one hour",
			expiresAt: now.Add(1 * time.Hour),
			want:      false,
		},
		{
			name:      "expires in 24 hours",
			expiresAt: now.Add(24 * time.Hour),
			want:      false,
		},
		{
			name:      "zero time",
			expiresAt: time.Time{},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			token := &CachedToken{
				Token:     "test-token",
				TokenType: "Bearer",
				ExpiresAt: tt.expiresAt,
			}

			// Add small sleep for tests that need time to pass
			if tt.expiresAt.Before(now) && !tt.expiresAt.IsZero() {
				time.Sleep(2 * time.Millisecond)
			}

			assert.Equal(t, tt.want, token.IsExpired())
		})
	}
}

func TestCachedToken_ShouldRefresh(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name      string
		expiresAt time.Time
		offset    time.Duration
		want      bool
	}{
		// Standard offset tests
		{
			name:      "within refresh window (3min left, 5min offset)",
			expiresAt: now.Add(3 * time.Minute),
			offset:    5 * time.Minute,
			want:      true,
		},
		{
			name:      "outside refresh window (10min left, 5min offset)",
			expiresAt: now.Add(10 * time.Minute),
			offset:    5 * time.Minute,
			want:      false,
		},
		// Various offset durations
		{
			name:      "zero offset with valid token",
			expiresAt: now.Add(1 * time.Hour),
			offset:    0,
			want:      false,
		},
		{
			name:      "negative offset",
			expiresAt: now.Add(1 * time.Hour),
			offset:    -5 * time.Minute,
			want:      false,
		},
		{
			name:      "very large offset",
			expiresAt: now.Add(24 * time.Hour),
			offset:    48 * time.Hour,
			want:      true,
		},
		// Expired tokens
		{
			name:      "already expired token",
			expiresAt: now.Add(-1 * time.Hour),
			offset:    5 * time.Minute,
			want:      true,
		},
		{
			name:      "expired with zero offset",
			expiresAt: now.Add(-1 * time.Hour),
			offset:    0,
			want:      true,
		},
		{
			name:      "about to expire (30 seconds)",
			expiresAt: now.Add(30 * time.Second),
			offset:    5 * time.Minute,
			want:      true,
		},
		// Production scenarios
		{
			name:      "fresh 1-hour token, 5min offset",
			expiresAt: now.Add(1 * time.Hour),
			offset:    5 * time.Minute,
			want:      false,
		},
		{
			name:      "near expiry (4min left), 5min offset",
			expiresAt: now.Add(4 * time.Minute),
			offset:    5 * time.Minute,
			want:      true,
		},
		{
			name:      "short-lived (3min left), 1min offset",
			expiresAt: now.Add(3 * time.Minute),
			offset:    1 * time.Minute,
			want:      false,
		},
		{
			name:      "short-lived (30s left), 1min offset",
			expiresAt: now.Add(30 * time.Second),
			offset:    1 * time.Minute,
			want:      true,
		},
		{
			name:      "long-lived (8min left), 10min offset",
			expiresAt: now.Add(8 * time.Minute),
			offset:    10 * time.Minute,
			want:      true,
		},
		{
			name:      "long-lived (15min left), 10min offset",
			expiresAt: now.Add(15 * time.Minute),
			offset:    10 * time.Minute,
			want:      false,
		},
		// Edge cases
		{
			name:      "zero time",
			expiresAt: time.Time{},
			offset:    5 * time.Minute,
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			token := &CachedToken{
				Token:     "test-token",
				TokenType: "Bearer",
				ExpiresAt: tt.expiresAt,
			}

			assert.Equal(t, tt.want, token.ShouldRefresh(tt.offset))
		})
	}
}

func TestCachedToken_ShouldRefresh_ConsistentWithIsExpired(t *testing.T) {
	t.Parallel()

	// If a token is expired, ShouldRefresh should always return true
	expiredToken := &CachedToken{
		Token:     "expired-token",
		TokenType: "Bearer",
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}

	assert.True(t, expiredToken.IsExpired())
	assert.True(t, expiredToken.ShouldRefresh(5*time.Minute))
	assert.True(t, expiredToken.ShouldRefresh(0))
}

func TestCachedToken_Lifecycle(t *testing.T) {
	t.Parallel()

	offset := 5 * time.Minute

	// Stage 1: Fresh token just issued
	token := &CachedToken{
		Token:        "lifecycle-token",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
		RefreshToken: "refresh-token",
		Scopes:       []string{"read", "write"},
		Metadata: map[string]string{
			"backend": "github",
			"user":    "test-user",
		},
	}

	assert.False(t, token.IsExpired())
	assert.False(t, token.ShouldRefresh(offset))

	// Stage 2: Token now has 4 minutes left
	token.ExpiresAt = time.Now().Add(4 * time.Minute)

	assert.False(t, token.IsExpired())
	assert.True(t, token.ShouldRefresh(offset))

	// Stage 3: Token now has 30 seconds left
	token.ExpiresAt = time.Now().Add(30 * time.Second)

	assert.False(t, token.IsExpired())
	assert.True(t, token.ShouldRefresh(offset))

	// Stage 4: Token has expired
	token.ExpiresAt = time.Now().Add(-1 * time.Minute)

	assert.True(t, token.IsExpired())
	assert.True(t, token.ShouldRefresh(offset))
}

func TestCachedToken_IndependentExpiry(t *testing.T) {
	t.Parallel()

	now := time.Now()
	offset := 5 * time.Minute

	tokens := []*CachedToken{
		{
			Token:     "token-1",
			TokenType: "Bearer",
			ExpiresAt: now.Add(1 * time.Hour),
		},
		{
			Token:     "token-2",
			TokenType: "Bearer",
			ExpiresAt: now.Add(10 * time.Minute),
		},
		{
			Token:     "token-3",
			TokenType: "Bearer",
			ExpiresAt: now.Add(-1 * time.Hour),
		},
	}

	// Each token should have its own expiry state
	assert.False(t, tokens[0].IsExpired())
	assert.False(t, tokens[1].IsExpired())
	assert.True(t, tokens[2].IsExpired())

	// Check refresh needs with offset
	assert.False(t, tokens[0].ShouldRefresh(offset))
	assert.False(t, tokens[1].ShouldRefresh(offset))
	assert.True(t, tokens[2].ShouldRefresh(offset))
}

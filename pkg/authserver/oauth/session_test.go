package oauth

import (
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		subject        string
		idpSessionID   string
		clientID       string
		expectTsid     bool
		expectClientID bool
	}{
		{
			name:           "with all parameters",
			subject:        "user@example.com",
			idpSessionID:   "idp-session-123",
			clientID:       "test-client-id",
			expectTsid:     true,
			expectClientID: true,
		},
		{
			name:           "with both subject and IDP session ID",
			subject:        "user@example.com",
			idpSessionID:   "idp-session-123",
			clientID:       "",
			expectTsid:     true,
			expectClientID: false,
		},
		{
			name:           "with empty subject",
			subject:        "",
			idpSessionID:   "idp-session-456",
			clientID:       "",
			expectTsid:     true,
			expectClientID: false,
		},
		{
			name:           "with empty IDP session ID",
			subject:        "user@example.com",
			idpSessionID:   "",
			clientID:       "",
			expectTsid:     false,
			expectClientID: false,
		},
		{
			name:           "with both empty",
			subject:        "",
			idpSessionID:   "",
			clientID:       "",
			expectTsid:     false,
			expectClientID: false,
		},
		{
			name:           "with only clientID",
			subject:        "",
			idpSessionID:   "",
			clientID:       "my-client",
			expectTsid:     false,
			expectClientID: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := NewSession(tt.subject, tt.idpSessionID, tt.clientID)

			require.NotNil(t, session)
			require.NotNil(t, session.JWTSession)
			require.NotNil(t, session.JWTClaims)
			require.NotNil(t, session.JWTHeader)
			require.NotNil(t, session.JWTHeader.Extra)
			require.NotNil(t, session.JWTClaims.Extra)

			assert.Equal(t, tt.subject, session.JWTClaims.Subject)
			assert.Equal(t, tt.idpSessionID, session.IDPSessionID)

			// Verify tsid claim is set correctly
			if tt.expectTsid {
				tsid, ok := session.JWTClaims.Extra[TokenSessionIDClaimKey]
				assert.True(t, ok, "tsid claim should be present")
				assert.Equal(t, tt.idpSessionID, tsid)
			} else {
				_, ok := session.JWTClaims.Extra[TokenSessionIDClaimKey]
				assert.False(t, ok, "tsid claim should not be present when idpSessionID is empty")
			}

			// Verify client_id and azp claims are set correctly
			if tt.expectClientID {
				clientID, ok := session.JWTClaims.Extra[ClientIDClaimKey]
				assert.True(t, ok, "client_id claim should be present")
				assert.Equal(t, tt.clientID, clientID)

				azp, ok := session.JWTClaims.Extra[AuthorizedPartyClaimKey]
				assert.True(t, ok, "azp claim should be present")
				assert.Equal(t, tt.clientID, azp)
			} else {
				_, ok := session.JWTClaims.Extra[ClientIDClaimKey]
				assert.False(t, ok, "client_id claim should not be present when clientID is empty")
				_, ok = session.JWTClaims.Extra[AuthorizedPartyClaimKey]
				assert.False(t, ok, "azp claim should not be present when clientID is empty")
			}
		})
	}
}

func TestSession_Clone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		session  *Session
		wantNil  bool
		validate func(t *testing.T, original *Session, cloned fosite.Session)
	}{
		{
			name:    "nil session returns nil",
			session: nil,
			wantNil: true,
		},
		{
			name:    "session with nil JWTSession",
			session: &Session{IDPSessionID: "idp-123"},
			wantNil: false,
			validate: func(t *testing.T, original *Session, cloned fosite.Session) {
				t.Helper()
				clonedSession, ok := cloned.(*Session)
				require.True(t, ok)
				assert.Equal(t, original.IDPSessionID, clonedSession.IDPSessionID)
				assert.Nil(t, clonedSession.JWTSession)
			},
		},
		{
			name:    "fully populated session",
			session: NewSession("user@example.com", "idp-session-789", ""),
			wantNil: false,
			validate: func(t *testing.T, original *Session, cloned fosite.Session) {
				t.Helper()
				clonedSession, ok := cloned.(*Session)
				require.True(t, ok)

				// Verify values are copied
				assert.Equal(t, original.IDPSessionID, clonedSession.IDPSessionID)
				assert.Equal(t, original.GetSubject(), clonedSession.GetSubject())

				// Verify it's a deep copy (modifying clone doesn't affect original)
				clonedSession.IDPSessionID = "modified"
				assert.NotEqual(t, original.IDPSessionID, clonedSession.IDPSessionID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cloned := tt.session.Clone()

			if tt.wantNil {
				assert.Nil(t, cloned)
				return
			}

			require.NotNil(t, cloned)
			tt.validate(t, tt.session, cloned)
		})
	}
}

func TestSession_Clone_DeepCopy(t *testing.T) {
	t.Parallel()

	// Create original session with specific values
	original := NewSession("original-subject", "original-idp-session", "")
	original.SetUsername("original-username")
	original.SetExpiresAt(fosite.AccessToken, time.Now().Add(time.Hour))

	// Clone the session
	clonedInterface := original.Clone()
	cloned, ok := clonedInterface.(*Session)
	require.True(t, ok)

	// Modify the clone
	cloned.SetSubject("modified-subject")
	cloned.SetUsername("modified-username")
	cloned.IDPSessionID = "modified-idp-session"

	// Verify original is unchanged
	assert.Equal(t, "original-subject", original.GetSubject())
	assert.Equal(t, "original-username", original.GetUsername())
	assert.Equal(t, "original-idp-session", original.IDPSessionID)
}

func TestSession_SetExpiresAt_GetExpiresAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		session   *Session
		tokenType fosite.TokenType
		expTime   time.Time
	}{
		{
			name:      "access token expiration",
			session:   NewSession("user", "idp-123", ""),
			tokenType: fosite.AccessToken,
			expTime:   time.Now().Add(time.Hour),
		},
		{
			name:      "refresh token expiration",
			session:   NewSession("user", "idp-123", ""),
			tokenType: fosite.RefreshToken,
			expTime:   time.Now().Add(24 * time.Hour),
		},
		{
			name:      "authorize code expiration",
			session:   NewSession("user", "idp-123", ""),
			tokenType: fosite.AuthorizeCode,
			expTime:   time.Now().Add(10 * time.Minute),
		},
		{
			name:      "session with nil JWTSession initializes it",
			session:   &Session{IDPSessionID: "idp-456"},
			tokenType: fosite.AccessToken,
			expTime:   time.Now().Add(time.Hour),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.session.SetExpiresAt(tt.tokenType, tt.expTime)
			got := tt.session.GetExpiresAt(tt.tokenType)

			// Compare with tolerance for time precision
			assert.WithinDuration(t, tt.expTime, got, time.Second)
		})
	}
}

func TestSession_GetExpiresAt_NilJWTSession(t *testing.T) {
	t.Parallel()

	session := &Session{IDPSessionID: "idp-123"}
	got := session.GetExpiresAt(fosite.AccessToken)

	assert.True(t, got.IsZero())
}

func TestSession_SetSubject_GetSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		session *Session
		subject string
	}{
		{
			name:    "set subject on new session",
			session: NewSession("initial", "idp-123", ""),
			subject: "updated-subject",
		},
		{
			name:    "set empty subject",
			session: NewSession("initial", "idp-123", ""),
			subject: "",
		},
		{
			name:    "set subject on session with nil JWTSession",
			session: &Session{IDPSessionID: "idp-456"},
			subject: "new-subject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.session.SetSubject(tt.subject)
			got := tt.session.GetSubject()

			assert.Equal(t, tt.subject, got)
		})
	}
}

func TestSession_GetSubject_NilJWTSession(t *testing.T) {
	t.Parallel()

	session := &Session{IDPSessionID: "idp-123"}
	got := session.GetSubject()

	assert.Empty(t, got)
}

func TestSession_GetSubject_NilJWTClaims(t *testing.T) {
	t.Parallel()

	session := &Session{
		JWTSession:   &oauth2.JWTSession{},
		IDPSessionID: "idp-123",
	}
	got := session.GetSubject()

	assert.Empty(t, got)
}

func TestSession_SetUsername_GetUsername(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		session  *Session
		username string
	}{
		{
			name:     "set username on new session",
			session:  NewSession("subject", "idp-123", ""),
			username: "john.doe",
		},
		{
			name:     "set empty username",
			session:  NewSession("subject", "idp-123", ""),
			username: "",
		},
		{
			name:     "set username on session with nil JWTSession",
			session:  &Session{IDPSessionID: "idp-456"},
			username: "jane.doe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.session.SetUsername(tt.username)
			got := tt.session.GetUsername()

			assert.Equal(t, tt.username, got)
		})
	}
}

func TestSession_GetUsername_NilJWTSession(t *testing.T) {
	t.Parallel()

	session := &Session{IDPSessionID: "idp-123"}
	got := session.GetUsername()

	assert.Empty(t, got)
}

func TestSession_SetIDPSessionID_GetIDPSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		session      *Session
		idpSessionID string
	}{
		{
			name:         "set IDP session ID on new session",
			session:      NewSession("subject", "initial-idp", ""),
			idpSessionID: "updated-idp-session",
		},
		{
			name:         "set empty IDP session ID",
			session:      NewSession("subject", "initial-idp", ""),
			idpSessionID: "",
		},
		{
			name:         "set IDP session ID on session with nil JWTSession",
			session:      &Session{},
			idpSessionID: "new-idp-session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.session.SetIDPSessionID(tt.idpSessionID)
			got := tt.session.GetIDPSessionID()

			assert.Equal(t, tt.idpSessionID, got)
		})
	}
}

func TestSession_GetJWTClaims(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		session *Session
		wantNil bool
	}{
		{
			name:    "nil JWTSession returns nil",
			session: &Session{IDPSessionID: "idp-123"},
			wantNil: true,
		},
		{
			name:    "valid session returns claims",
			session: NewSession("subject", "idp-123", ""),
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.session.GetJWTClaims()

			if tt.wantNil {
				assert.Nil(t, got)
			} else {
				assert.NotNil(t, got)
			}
		})
	}
}

func TestSession_GetJWTHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		session *Session
		wantNil bool
	}{
		{
			name:    "nil JWTSession returns nil",
			session: &Session{IDPSessionID: "idp-123"},
			wantNil: true,
		},
		{
			name:    "valid session returns header",
			session: NewSession("subject", "idp-123", ""),
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.session.GetJWTHeader()

			if tt.wantNil {
				assert.Nil(t, got)
			} else {
				assert.NotNil(t, got)
			}
		})
	}
}

func TestSession_SetSubject_InitializesNilFields(t *testing.T) {
	t.Parallel()

	// Start with a session that has nil JWTSession
	session := &Session{IDPSessionID: "idp-123"}

	// SetSubject should initialize JWTSession and JWTClaims
	session.SetSubject("new-subject")

	require.NotNil(t, session.JWTSession)
	require.NotNil(t, session.JWTClaims)
	assert.Equal(t, "new-subject", session.JWTClaims.Subject)
}

func TestSession_SetSubject_InitializesNilJWTClaims(t *testing.T) {
	t.Parallel()

	// Start with a session that has JWTSession but nil JWTClaims
	session := &Session{
		JWTSession:   &oauth2.JWTSession{},
		IDPSessionID: "idp-123",
	}

	// SetSubject should initialize JWTClaims
	session.SetSubject("new-subject")

	require.NotNil(t, session.JWTClaims)
	assert.Equal(t, "new-subject", session.JWTClaims.Subject)
}

func TestSession_SetExpiresAt_InitializesNilJWTSession(t *testing.T) {
	t.Parallel()

	// Start with a session that has nil JWTSession
	session := &Session{IDPSessionID: "idp-123"}
	expTime := time.Now().Add(time.Hour)

	// SetExpiresAt should initialize JWTSession
	session.SetExpiresAt(fosite.AccessToken, expTime)

	require.NotNil(t, session.JWTSession)
	got := session.GetExpiresAt(fosite.AccessToken)
	assert.WithinDuration(t, expTime, got, time.Second)
}

func TestSession_SetUsername_InitializesNilJWTSession(t *testing.T) {
	t.Parallel()

	// Start with a session that has nil JWTSession
	session := &Session{IDPSessionID: "idp-123"}

	// SetUsername should initialize JWTSession
	session.SetUsername("john.doe")

	require.NotNil(t, session.JWTSession)
	assert.Equal(t, "john.doe", session.Username)
}

func TestSession_ImplementsFositeSession(t *testing.T) {
	t.Parallel()

	// Verify that Session implements fosite.Session interface
	var _ fosite.Session = (*Session)(nil)

	session := NewSession("subject", "idp-123", "")

	// Test all fosite.Session interface methods
	assert.Equal(t, "subject", session.GetSubject())
	session.SetSubject("new-subject")
	assert.Equal(t, "new-subject", session.GetSubject())

	expTime := time.Now().Add(time.Hour)
	session.SetExpiresAt(fosite.AccessToken, expTime)
	got := session.GetExpiresAt(fosite.AccessToken)
	assert.WithinDuration(t, expTime, got, time.Second)

	session.SetUsername("username")
	assert.Equal(t, "username", session.GetUsername())

	cloned := session.Clone()
	assert.NotNil(t, cloned)
}

func TestSession_GetJWTClaims_ReturnsContainer(t *testing.T) {
	t.Parallel()

	session := NewSession("test-subject", "idp-123", "")
	claims := session.GetJWTClaims()

	require.NotNil(t, claims)

	// Verify the claims container has the expected subject
	claimsMap := claims.ToMapClaims()
	assert.Equal(t, "test-subject", claimsMap["sub"])
}

func TestSession_GetJWTHeader_HasExtraMap(t *testing.T) {
	t.Parallel()

	session := NewSession("subject", "idp-123", "")
	header := session.GetJWTHeader()

	require.NotNil(t, header)
	require.NotNil(t, header.Extra)
}

func TestSession_MultipleTokenTypeExpirations(t *testing.T) {
	t.Parallel()

	session := NewSession("subject", "idp-123", "")

	accessExpTime := time.Now().Add(time.Hour)
	refreshExpTime := time.Now().Add(24 * time.Hour)
	authCodeExpTime := time.Now().Add(10 * time.Minute)

	session.SetExpiresAt(fosite.AccessToken, accessExpTime)
	session.SetExpiresAt(fosite.RefreshToken, refreshExpTime)
	session.SetExpiresAt(fosite.AuthorizeCode, authCodeExpTime)

	assert.WithinDuration(t, accessExpTime, session.GetExpiresAt(fosite.AccessToken), time.Second)
	assert.WithinDuration(t, refreshExpTime, session.GetExpiresAt(fosite.RefreshToken), time.Second)
	assert.WithinDuration(t, authCodeExpTime, session.GetExpiresAt(fosite.AuthorizeCode), time.Second)
}

func TestSession_JWTClaimsContainer_Methods(t *testing.T) {
	t.Parallel()

	session := NewSession("subject", "idp-123", "")
	claims := session.GetJWTClaims()

	require.NotNil(t, claims)

	// Verify we can call ToMapClaims
	mapClaims := claims.ToMapClaims()
	assert.NotNil(t, mapClaims)
	assert.Equal(t, "subject", mapClaims["sub"])
}

func TestSession_TsidClaimInJWT(t *testing.T) {
	t.Parallel()

	t.Run("tsid appears in JWT claims when IDP session ID is provided", func(t *testing.T) {
		t.Parallel()

		idpSessionID := "my-idp-session-id-12345"
		session := NewSession("user@example.com", idpSessionID, "")

		// Get the JWT claims container (what fosite uses to generate JWT)
		claims := session.GetJWTClaims()
		require.NotNil(t, claims)

		// Verify tsid is in the claims map that will be encoded into the JWT
		mapClaims := claims.ToMapClaims()
		tsid, ok := mapClaims[TokenSessionIDClaimKey]
		assert.True(t, ok, "tsid claim should be present in JWT claims map")
		assert.Equal(t, idpSessionID, tsid)
	})

	t.Run("tsid is absent from JWT claims when IDP session ID is empty", func(t *testing.T) {
		t.Parallel()

		session := NewSession("user@example.com", "", "")

		claims := session.GetJWTClaims()
		require.NotNil(t, claims)

		mapClaims := claims.ToMapClaims()
		_, ok := mapClaims[TokenSessionIDClaimKey]
		assert.False(t, ok, "tsid claim should not be present when IDP session ID is empty")
	})
}

func TestTokenSessionIDClaimKey(t *testing.T) {
	t.Parallel()

	// Verify the constant has the expected value
	assert.Equal(t, "tsid", TokenSessionIDClaimKey)
}

func TestClientIDClaimKey(t *testing.T) {
	t.Parallel()

	// Verify the constant has the expected value
	assert.Equal(t, "client_id", ClientIDClaimKey)
}

func TestAuthorizedPartyClaimKey(t *testing.T) {
	t.Parallel()

	// Verify the constant has the expected value
	assert.Equal(t, "azp", AuthorizedPartyClaimKey)
}

func TestSession_ClientIDAndAzpClaimsInJWT(t *testing.T) {
	t.Parallel()

	t.Run("client_id and azp appear in JWT claims when clientID is provided", func(t *testing.T) {
		t.Parallel()

		clientID := "my-oauth-client-id"
		session := NewSession("user@example.com", "idp-session", clientID)

		// Get the JWT claims container (what fosite uses to generate JWT)
		claims := session.GetJWTClaims()
		require.NotNil(t, claims)

		// Verify claims are in the claims map that will be encoded into the JWT
		mapClaims := claims.ToMapClaims()

		gotClientID, ok := mapClaims[ClientIDClaimKey]
		assert.True(t, ok, "client_id claim should be present in JWT claims map")
		assert.Equal(t, clientID, gotClientID)

		gotAzp, ok := mapClaims[AuthorizedPartyClaimKey]
		assert.True(t, ok, "azp claim should be present in JWT claims map")
		assert.Equal(t, clientID, gotAzp)
	})

	t.Run("client_id and azp are absent from JWT claims when clientID is empty", func(t *testing.T) {
		t.Parallel()

		session := NewSession("user@example.com", "idp-session", "")

		claims := session.GetJWTClaims()
		require.NotNil(t, claims)

		mapClaims := claims.ToMapClaims()
		_, ok := mapClaims[ClientIDClaimKey]
		assert.False(t, ok, "client_id claim should not be present when clientID is empty")
		_, ok = mapClaims[AuthorizedPartyClaimKey]
		assert.False(t, ok, "azp claim should not be present when clientID is empty")
	})
}

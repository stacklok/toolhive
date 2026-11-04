// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package session

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testVerifiedUpstream = UpstreamSubject{
	Issuer: "https://idp.example.com", Subject: "upstream-sub", StableClaim: "oid", StableValue: "upstream-oid",
}

func TestFactory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		subject      string
		idpSessionID string
		clientID     string
	}{
		{
			name:         "creates session with all parameters",
			subject:      "user@example.com",
			idpSessionID: "idp-session-123",
			clientID:     "client-456",
		},
		{
			name:         "creates session for deserialization (empty clientID)",
			subject:      "deserialized-user",
			idpSessionID: "deserialized-idp-session",
			clientID:     "",
		},
		{
			name:         "creates mock session for testing",
			subject:      "test-subject",
			idpSessionID: "",
			clientID:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			factory := Factory(func(subject, idpSessionID, clientID string) fosite.Session {
				return New(subject, idpSessionID, clientID, UserClaims{})
			})

			session := factory(tt.subject, tt.idpSessionID, tt.clientID)

			require.NotNil(t, session)
			assert.Equal(t, tt.subject, session.GetSubject())

			concreteSession, ok := session.(*Session)
			require.True(t, ok, "factory should return *Session")
			assert.Equal(t, tt.idpSessionID, concreteSession.UpstreamSessionID)

			// Verify UpstreamSession interface works for storage serialization
			idpSession, ok := session.(UpstreamSession)
			require.True(t, ok, "session should implement UpstreamSession")
			assert.Equal(t, tt.idpSessionID, idpSession.GetIDPSessionID())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		subject        string
		idpSessionID   string
		clientID       string
		userName       string
		userEmail      string
		expectTsid     bool
		expectClientID bool
		expectName     bool
		expectEmail    bool
	}{
		{
			name:           "with all parameters including name and email",
			subject:        "user@example.com",
			idpSessionID:   "upstream-session-123",
			clientID:       "test-client-id",
			userName:       "Joe Smith",
			userEmail:      "joe@example.com",
			expectTsid:     true,
			expectClientID: true,
			expectName:     true,
			expectEmail:    true,
		},
		{
			name:           "with subject and IDP session ID only",
			subject:        "user@example.com",
			idpSessionID:   "upstream-session-123",
			clientID:       "",
			expectTsid:     true,
			expectClientID: false,
		},
		{
			name:           "with empty subject",
			subject:        "",
			idpSessionID:   "upstream-session-456",
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
			name:           "with all empty (placeholder session)",
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
		{
			name:         "with name only",
			subject:      "user-123",
			idpSessionID: "session-1",
			clientID:     "",
			userName:     "Test User",
			expectTsid:   true,
			expectName:   true,
		},
		{
			name:         "with email only",
			subject:      "user-123",
			idpSessionID: "session-1",
			clientID:     "",
			userEmail:    "test@example.com",
			expectTsid:   true,
			expectEmail:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := New(tt.subject, tt.idpSessionID, tt.clientID, UserClaims{
				Name:  tt.userName,
				Email: tt.userEmail,
			})

			require.NotNil(t, session)
			require.NotNil(t, session.JWTSession)
			require.NotNil(t, session.JWTClaims)
			require.NotNil(t, session.JWTHeader)

			assert.Equal(t, tt.subject, session.GetSubject())
			assert.Equal(t, tt.idpSessionID, session.UpstreamSessionID)

			claimsMap := session.GetJWTClaims().ToMapClaims()

			if tt.expectTsid {
				assert.Equal(t, tt.idpSessionID, claimsMap[TokenSessionIDClaimKey])
			} else {
				_, ok := claimsMap[TokenSessionIDClaimKey]
				assert.False(t, ok, "tsid claim should not be present when idpSessionID is empty")
			}

			if tt.expectClientID {
				assert.Equal(t, tt.clientID, claimsMap[ClientIDClaimKey])
			} else {
				_, ok := claimsMap[ClientIDClaimKey]
				assert.False(t, ok, "client_id claim should not be present when clientID is empty")
			}

			if tt.expectName {
				assert.Equal(t, tt.userName, claimsMap[NameClaimKey])
			} else {
				_, ok := claimsMap[NameClaimKey]
				assert.False(t, ok, "name claim should not be present when name is empty")
			}

			if tt.expectEmail {
				assert.Equal(t, tt.userEmail, claimsMap[EmailClaimKey])
			} else {
				_, ok := claimsMap[EmailClaimKey]
				assert.False(t, ok, "email claim should not be present when email is empty")
			}
		})
	}
}

func TestSession_Clone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		session       func() *Session
		expectNil     bool
		checkDeepCopy bool
	}{
		{
			name:      "nil session returns nil",
			session:   func() *Session { return nil },
			expectNil: true,
		},
		{
			name: "session with nil JWTSession",
			session: func() *Session {
				return &Session{UpstreamSessionID: "upstream-123"}
			},
			expectNil: false,
		},
		{
			name: "fully populated session creates deep copy",
			session: func() *Session {
				s := New("user@example.com", "upstream-session-789", "client-123", UserClaims{})
				s.Username = "original-username"
				s.SetExpiresAt(fosite.AccessToken, time.Now().Add(time.Hour))
				return s
			},
			expectNil:     false,
			checkDeepCopy: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			original := tt.session()
			cloned := original.Clone()

			if tt.expectNil {
				assert.Nil(t, cloned)
				return
			}

			require.NotNil(t, cloned)
			clonedSession, ok := cloned.(*Session)
			require.True(t, ok)
			assert.Equal(t, original.UpstreamSessionID, clonedSession.UpstreamSessionID)

			if tt.checkDeepCopy {
				// Verify modifying clone doesn't affect original
				clonedSession.UpstreamSessionID = "modified"
				clonedSession.SetSubject("modified-subject")
				clonedSession.Username = "modified-username"

				assert.Equal(t, "upstream-session-789", original.UpstreamSessionID)
				assert.Equal(t, "user@example.com", original.GetSubject())
				assert.Equal(t, "original-username", original.GetUsername())
			}
		})
	}
}

func TestSession_CloneCopiesVerifiedIdentityAndIsolatesSubjectIdentifier(t *testing.T) {
	t.Parallel()

	original := New("user-1", "upstream-session", "client-1", UserClaims{})
	original.VerifiedUpstream = testVerifiedUpstream
	original.SetSubjectIdentifier(original.VerifiedUpstream)

	cloned, ok := original.Clone().(*Session)
	require.True(t, ok)
	assert.Equal(t, testVerifiedUpstream, cloned.VerifiedUpstream)
	require.Contains(t, cloned.JWTClaims.Extra, subjectIdentifierClaimKey)
	require.Contains(t, cloned.JWTClaims.Extra, UpstreamSubjectClaimKey)

	cloned.ClearSubjectIdentifier()

	assert.NotContains(t, cloned.JWTClaims.Extra, subjectIdentifierClaimKey)
	assert.NotContains(t, cloned.JWTClaims.Extra, UpstreamSubjectClaimKey)
	assert.Contains(t, original.JWTClaims.Extra, subjectIdentifierClaimKey)
	assert.Contains(t, original.JWTClaims.Extra, UpstreamSubjectClaimKey)
}

func TestSession_UpstreamSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		session *Session
		setID   string
		wantGet string
	}{
		{
			name:    "get and set on new session",
			session: New("subject", "initial-upstream", "client", UserClaims{}),
			setID:   "updated-upstream",
			wantGet: "initial-upstream",
		},
		{
			name:    "get on nil session returns empty",
			session: nil,
			wantGet: "",
		},
		{
			name:    "set on nil session does not panic",
			session: nil,
			setID:   "test-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantGet, tt.session.GetIDPSessionID())

			if tt.setID != "" {
				assert.NotPanics(t, func() {
					tt.session.SetIDPSessionID(tt.setID)
				})
				if tt.session != nil {
					assert.Equal(t, tt.setID, tt.session.GetIDPSessionID())
				}
			}
		})
	}
}

func TestSession_SubjectIdentifier_NilSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sess *Session
	}{
		{name: "nil session", sess: nil},
		{name: "nil JWTSession", sess: &Session{}},
		{name: "nil JWTClaims", sess: &Session{JWTSession: &oauth2.JWTSession{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NotPanics(t, tt.sess.ClearSubjectIdentifier)
			require.NotPanics(t, func() { tt.sess.SetSubjectIdentifier(testVerifiedUpstream) })
		})
	}
}

func TestSession_SetSubjectIdentifier_AllocatesNilExtra(t *testing.T) {
	t.Parallel()

	s := New("user-1", "", "", UserClaims{})
	s.JWTClaims.Extra = nil
	s.SetSubjectIdentifier(testVerifiedUpstream)

	assert.Equal(t, map[string]string{
		"format": "iss_sub",
		"iss":    "https://idp.example.com",
		"sub":    "upstream-sub",
	}, s.JWTClaims.Extra["sub_id"])
	assert.Equal(t, map[string]string{
		"iss":   "https://idp.example.com",
		"claim": "oid",
		"value": "upstream-oid",
	}, s.JWTClaims.Extra[UpstreamSubjectClaimKey])
}

func TestSession_SetSubjectIdentifier_RequiresCompleteIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		upstream UpstreamSubject
		want     bool
	}{
		{name: "complete sets both claims", upstream: testVerifiedUpstream, want: true},
		{name: "incomplete sets neither", upstream: UpstreamSubject{Issuer: "https://idp.example.com", Subject: "s"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := New("user-1", "", "", UserClaims{})
			s.SetSubjectIdentifier(tt.upstream)

			if tt.want {
				assert.Contains(t, s.JWTClaims.Extra, subjectIdentifierClaimKey)
				assert.Contains(t, s.JWTClaims.Extra, UpstreamSubjectClaimKey)
				return
			}
			assert.NotContains(t, s.JWTClaims.Extra, subjectIdentifierClaimKey)
			assert.NotContains(t, s.JWTClaims.Extra, UpstreamSubjectClaimKey)
		})
	}
}

func TestUpstreamSubject_IsComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*UpstreamSubject)
		want   bool
	}{
		{name: "complete", mutate: func(*UpstreamSubject) {}, want: true},
		{name: "empty issuer", mutate: func(u *UpstreamSubject) { u.Issuer = "" }},
		{name: "empty subject", mutate: func(u *UpstreamSubject) { u.Subject = "" }},
		{name: "empty stable claim", mutate: func(u *UpstreamSubject) { u.StableClaim = "" }},
		{name: "empty stable value", mutate: func(u *UpstreamSubject) { u.StableValue = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := testVerifiedUpstream
			tt.mutate(&u)
			assert.Equal(t, tt.want, u.IsComplete())
		})
	}
}

func TestSession_VerifiedUpstreamJSON(t *testing.T) {
	t.Parallel()

	t.Run("set value marshals under verified_upstream", func(t *testing.T) {
		t.Parallel()

		s := &Session{VerifiedUpstream: testVerifiedUpstream}
		data, err := json.Marshal(s)
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		assert.Equal(t, map[string]any{
			"iss":          "https://idp.example.com",
			"sub":          "upstream-sub",
			"stable_claim": "oid",
			"stable_value": "upstream-oid",
		}, raw["verified_upstream"])

		var back Session
		require.NoError(t, json.Unmarshal(data, &back))
		assert.Equal(t, testVerifiedUpstream, back.VerifiedUpstream)
	})

	t.Run("zero value is omitted", func(t *testing.T) {
		t.Parallel()

		data, err := json.Marshal(&Session{})
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		assert.NotContains(t, raw, "verified_upstream")
	})
}

package authserver

import (
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/token/jwt"
)

// TokenSessionIDClaimKey is the JWT claim key for the token session ID.
// This links JWT access tokens to stored upstream IDP tokens.
// We use "tsid" instead of "sid" to avoid confusion with OIDC session management
// which defines "sid" for different purposes (RFC 7519, OIDC Session Management).
const TokenSessionIDClaimKey = "tsid"

// ClientIDClaimKey is the JWT claim key for the OAuth client ID.
// This identifies which client was issued the token.
const ClientIDClaimKey = "client_id"

// AuthorizedPartyClaimKey is the JWT claim key for the authorized party (azp).
// This is included for OIDC compatibility and identifies the party to which
// the token was issued.
const AuthorizedPartyClaimKey = "azp"

// Session extends fosite's JWT session with an IDP session reference.
// This allows the authorization server to link issued tokens to
// upstream IDP tokens stored separately.
type Session struct {
	*oauth2.JWTSession

	// IDPSessionID links this session to stored upstream IDP tokens.
	// This ID is used to retrieve the IDP tokens from storage.
	IDPSessionID string
}

// NewSession creates a new Session with the given subject, IDP session ID, and client ID.
// If idpSessionID is provided, it will be included in the JWT claims as "tsid"
// to allow the proxy middleware to look up upstream IDP tokens.
// If clientID is provided, it will be included in the JWT claims as both "client_id"
// and "azp" (authorized party) for binding verification and OIDC compatibility.
func NewSession(subject, idpSessionID, clientID string) *Session {
	// Initialize the Extra map for JWT claims
	claimsExtra := make(map[string]interface{})

	// Add tsid claim if idpSessionID is provided
	if idpSessionID != "" {
		claimsExtra[TokenSessionIDClaimKey] = idpSessionID
	}

	// Add client_id and azp claims for binding verification
	if clientID != "" {
		claimsExtra[ClientIDClaimKey] = clientID
		claimsExtra[AuthorizedPartyClaimKey] = clientID
	}

	return &Session{
		JWTSession: &oauth2.JWTSession{
			JWTClaims: &jwt.JWTClaims{
				Subject: subject,
				Extra:   claimsExtra,
			},
			JWTHeader: &jwt.Headers{
				Extra: make(map[string]interface{}),
			},
		},
		IDPSessionID: idpSessionID,
	}
}

// Clone creates a deep copy of the session.
func (s *Session) Clone() fosite.Session {
	if s == nil {
		return nil
	}

	var jwtSession *oauth2.JWTSession
	if s.JWTSession != nil {
		if cloned := s.JWTSession.Clone(); cloned != nil {
			if js, ok := cloned.(*oauth2.JWTSession); ok {
				jwtSession = js
			}
		}
	}

	return &Session{
		JWTSession:   jwtSession,
		IDPSessionID: s.IDPSessionID,
	}
}

// SetExpiresAt sets the expiration time for a specific token type.
func (s *Session) SetExpiresAt(key fosite.TokenType, exp time.Time) {
	if s.JWTSession == nil {
		s.JWTSession = &oauth2.JWTSession{}
	}
	s.JWTSession.SetExpiresAt(key, exp)
}

// GetExpiresAt returns the expiration time for a specific token type.
func (s *Session) GetExpiresAt(key fosite.TokenType) time.Time {
	if s.JWTSession == nil {
		return time.Time{}
	}
	return s.JWTSession.GetExpiresAt(key)
}

// GetSubject returns the subject of the session.
func (s *Session) GetSubject() string {
	if s.JWTSession == nil || s.JWTClaims == nil {
		return ""
	}
	return s.JWTClaims.Subject
}

// SetSubject sets the subject of the session.
func (s *Session) SetSubject(subject string) {
	if s.JWTSession == nil {
		s.JWTSession = &oauth2.JWTSession{}
	}
	if s.JWTClaims == nil {
		s.JWTClaims = &jwt.JWTClaims{}
	}
	s.JWTClaims.Subject = subject
}

// GetUsername returns the username of the session.
func (s *Session) GetUsername() string {
	if s.JWTSession == nil {
		return ""
	}
	return s.Username
}

// SetUsername sets the username of the session.
func (s *Session) SetUsername(username string) {
	if s.JWTSession == nil {
		s.JWTSession = &oauth2.JWTSession{}
	}
	s.Username = username
}

// GetJWTClaims returns the JWT claims for this session.
func (s *Session) GetJWTClaims() jwt.JWTClaimsContainer {
	if s.JWTSession == nil {
		return nil
	}
	return s.JWTSession.GetJWTClaims()
}

// GetJWTHeader returns the JWT header for this session.
func (s *Session) GetJWTHeader() *jwt.Headers {
	if s.JWTSession == nil {
		return nil
	}
	return s.JWTSession.GetJWTHeader()
}

// SetIDPSessionID sets the IDP session ID.
func (s *Session) SetIDPSessionID(id string) {
	s.IDPSessionID = id
}

// GetIDPSessionID returns the IDP session ID.
func (s *Session) GetIDPSessionID() string {
	return s.IDPSessionID
}

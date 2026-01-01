package authserver

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/token/jwt"
)

// serializedClient represents a fosite.Client in serializable form.
type serializedClient struct {
	ID            string   `json:"id"`
	Secret        []byte   `json:"secret,omitempty"`
	RedirectURIs  []string `json:"redirect_uris"`
	GrantTypes    []string `json:"grant_types"`
	ResponseTypes []string `json:"response_types"`
	Scopes        []string `json:"scopes"`
	Audience      []string `json:"audience"`
	Public        bool     `json:"public"`
}

// serializedSession represents our Session in serializable form.
type serializedSession struct {
	Subject      string                     `json:"subject"`
	Username     string                     `json:"username,omitempty"`
	IDPSessionID string                     `json:"idp_session_id,omitempty"`
	ExpiresAt    map[fosite.TokenType]int64 `json:"expires_at,omitempty"`
	JWTClaims    *serializedJWTClaims       `json:"jwt_claims,omitempty"`
	JWTHeader    map[string]interface{}     `json:"jwt_header,omitempty"`
}

// serializedJWTClaims represents jwt.JWTClaims in serializable form.
type serializedJWTClaims struct {
	Subject   string                 `json:"sub,omitempty"`
	Issuer    string                 `json:"iss,omitempty"`
	Audience  []string               `json:"aud,omitempty"`
	ExpiresAt int64                  `json:"exp,omitempty"`
	IssuedAt  int64                  `json:"iat,omitempty"`
	NotBefore int64                  `json:"nbf,omitempty"`
	JTI       string                 `json:"jti,omitempty"`
	Scope     []string               `json:"scope,omitempty"`
	Extra     map[string]interface{} `json:"extra,omitempty"`
}

// serializedRequester represents a fosite.Requester in serializable form.
type serializedRequester struct {
	ID                string             `json:"id"`
	RequestedAt       int64              `json:"requested_at"`
	ClientID          string             `json:"client_id"`
	RequestedScopes   []string           `json:"requested_scopes"`
	GrantedScopes     []string           `json:"granted_scopes"`
	RequestedAudience []string           `json:"requested_audience"`
	GrantedAudience   []string           `json:"granted_audience"`
	Form              map[string]string  `json:"form,omitempty"`
	Session           *serializedSession `json:"session,omitempty"`
}

// serializeClient converts a fosite.Client to serializable form.
func serializeClient(client fosite.Client) *serializedClient {
	if client == nil {
		return nil
	}

	return &serializedClient{
		ID:            client.GetID(),
		Secret:        client.GetHashedSecret(),
		RedirectURIs:  client.GetRedirectURIs(),
		GrantTypes:    client.GetGrantTypes(),
		ResponseTypes: client.GetResponseTypes(),
		Scopes:        client.GetScopes(),
		Audience:      client.GetAudience(),
		Public:        client.IsPublic(),
	}
}

// deserializeClient converts serialized data back to a fosite.Client.
func deserializeClient(data *serializedClient) fosite.Client {
	if data == nil {
		return nil
	}

	return &fosite.DefaultClient{
		ID:            data.ID,
		Secret:        data.Secret,
		RedirectURIs:  data.RedirectURIs,
		GrantTypes:    data.GrantTypes,
		ResponseTypes: data.ResponseTypes,
		Scopes:        data.Scopes,
		Audience:      data.Audience,
		Public:        data.Public,
	}
}

// serializeSession converts a fosite.Session to serializable form.
func serializeSession(session fosite.Session) *serializedSession {
	if session == nil {
		return nil
	}

	serialized := &serializedSession{
		Subject:   session.GetSubject(),
		ExpiresAt: make(map[fosite.TokenType]int64),
	}

	// Extract expiration times for known token types
	tokenTypes := []fosite.TokenType{
		fosite.AccessToken,
		fosite.RefreshToken,
		fosite.AuthorizeCode,
		fosite.IDToken,
	}

	for _, tt := range tokenTypes {
		exp := session.GetExpiresAt(tt)
		if !exp.IsZero() {
			serialized.ExpiresAt[tt] = exp.Unix()
		}
	}

	// Handle our custom Session type
	if customSession, ok := session.(*Session); ok {
		serialized.IDPSessionID = customSession.IDPSessionID
		if customSession.JWTSession != nil {
			serialized.Username = customSession.Username
			if customSession.JWTClaims != nil {
				serialized.JWTClaims = &serializedJWTClaims{
					Subject:  customSession.JWTClaims.Subject,
					Issuer:   customSession.JWTClaims.Issuer,
					Audience: customSession.JWTClaims.Audience,
					JTI:      customSession.JWTClaims.JTI,
					Scope:    customSession.JWTClaims.Scope,
					Extra:    customSession.JWTClaims.Extra,
				}
				if !customSession.JWTClaims.ExpiresAt.IsZero() {
					serialized.JWTClaims.ExpiresAt = customSession.JWTClaims.ExpiresAt.Unix()
				}
				if !customSession.JWTClaims.IssuedAt.IsZero() {
					serialized.JWTClaims.IssuedAt = customSession.JWTClaims.IssuedAt.Unix()
				}
				if !customSession.JWTClaims.NotBefore.IsZero() {
					serialized.JWTClaims.NotBefore = customSession.JWTClaims.NotBefore.Unix()
				}
			}
			if customSession.JWTHeader != nil && customSession.JWTHeader.Extra != nil {
				serialized.JWTHeader = customSession.JWTHeader.Extra
			}
		}
	}

	return serialized
}

// deserializeSession converts serialized data back to a fosite.Session.
func deserializeSession(data *serializedSession) fosite.Session {
	if data == nil {
		return nil
	}

	// Rebuild JWT claims
	jwtClaims := &jwt.JWTClaims{
		Subject: data.Subject,
		Extra:   make(map[string]interface{}),
	}

	if data.JWTClaims != nil {
		jwtClaims.Subject = data.JWTClaims.Subject
		jwtClaims.Issuer = data.JWTClaims.Issuer
		jwtClaims.Audience = data.JWTClaims.Audience
		jwtClaims.JTI = data.JWTClaims.JTI
		jwtClaims.Scope = data.JWTClaims.Scope
		jwtClaims.Extra = data.JWTClaims.Extra

		if data.JWTClaims.ExpiresAt != 0 {
			jwtClaims.ExpiresAt = time.Unix(data.JWTClaims.ExpiresAt, 0)
		}
		if data.JWTClaims.IssuedAt != 0 {
			jwtClaims.IssuedAt = time.Unix(data.JWTClaims.IssuedAt, 0)
		}
		if data.JWTClaims.NotBefore != 0 {
			jwtClaims.NotBefore = time.Unix(data.JWTClaims.NotBefore, 0)
		}
	}

	// Rebuild JWT header
	jwtHeader := &jwt.Headers{
		Extra: make(map[string]interface{}),
	}
	if data.JWTHeader != nil {
		jwtHeader.Extra = data.JWTHeader
	}

	// Use our custom Session type
	session := NewSession(data.Subject, data.IDPSessionID, "")
	session.Username = data.Username
	session.JWTClaims = jwtClaims
	session.JWTHeader = jwtHeader

	// Restore expiration times
	for tokenType, exp := range data.ExpiresAt {
		session.SetExpiresAt(tokenType, time.Unix(exp, 0))
	}

	return session
}

// serializeRequester converts a fosite.Requester to serializable form.
func serializeRequester(r fosite.Requester) *serializedRequester {
	if r == nil {
		return nil
	}

	serialized := &serializedRequester{
		ID:                r.GetID(),
		RequestedAt:       r.GetRequestedAt().Unix(),
		RequestedScopes:   r.GetRequestedScopes(),
		GrantedScopes:     r.GetGrantedScopes(),
		RequestedAudience: r.GetRequestedAudience(),
		GrantedAudience:   r.GetGrantedAudience(),
		Session:           serializeSession(r.GetSession()),
	}

	// Store client ID for lookup during deserialization
	if client := r.GetClient(); client != nil {
		serialized.ClientID = client.GetID()
	}

	// Serialize form data (only first values for simplicity)
	form := r.GetRequestForm()
	if len(form) > 0 {
		serialized.Form = make(map[string]string)
		for key, values := range form {
			if len(values) > 0 {
				serialized.Form[key] = values[0]
			}
		}
	}

	return serialized
}

// deserializeRequester converts serialized data back to a fosite.Requester.
// clientLookup is used to resolve the client by ID.
func deserializeRequester(data *serializedRequester, clientLookup func(string) fosite.Client) (fosite.Requester, error) {
	if data == nil {
		return nil, nil
	}

	// Rebuild form data
	form := make(url.Values)
	for key, value := range data.Form {
		form.Set(key, value)
	}

	// Look up the client
	var client fosite.Client
	if data.ClientID != "" && clientLookup != nil {
		client = clientLookup(data.ClientID)
	}

	// Create the request
	request := &fosite.Request{
		ID:          data.ID,
		RequestedAt: time.Unix(data.RequestedAt, 0),
		Client:      client,
		Form:        form,
		Session:     deserializeSession(data.Session),
	}

	// Restore scopes
	for _, scope := range data.RequestedScopes {
		request.AppendRequestedScope(scope)
	}
	for _, scope := range data.GrantedScopes {
		request.GrantScope(scope)
	}

	// Restore audience
	for _, aud := range data.RequestedAudience {
		request.AppendRequestedAudience(aud)
	}
	for _, aud := range data.GrantedAudience {
		request.GrantAudience(aud)
	}

	return request, nil
}

// marshalRequester serializes a fosite.Requester to JSON bytes.
func marshalRequester(r fosite.Requester) ([]byte, error) {
	serialized := serializeRequester(r)
	return json.Marshal(serialized)
}

// unmarshalRequester deserializes JSON bytes back to a fosite.Requester.
func unmarshalRequester(data []byte, clientLookup func(string) fosite.Client) (fosite.Requester, error) {
	var serialized serializedRequester
	if err := json.Unmarshal(data, &serialized); err != nil {
		return nil, fmt.Errorf("failed to unmarshal requester: %w", err)
	}
	return deserializeRequester(&serialized, clientLookup)
}

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

package storage

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/token/jwt"
)

// SessionFactory is a function that creates a new session given subject and IDP session ID.
// This allows storage implementations to create sessions without importing oauth/ package.
type SessionFactory func(subject, idpSessionID, clientID string) fosite.Session

// SessionWithIDP is an interface for sessions that have an IDP session ID.
// This is implemented by oauth.Session and used for serialization.
type SessionWithIDP interface {
	fosite.Session
	GetIDPSessionID() string
}

// SessionWithJWT is an interface for sessions that have JWT claims and headers.
// This extends SessionWithIDP with JWT-specific methods.
type SessionWithJWT interface {
	SessionWithIDP
	GetJWTClaims() jwt.JWTClaimsContainer
	GetJWTHeader() *jwt.Headers
	SetExpiresAt(key fosite.TokenType, exp time.Time)
}

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

// serializedSession represents a session in serializable form.
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

	// Handle sessions with IDP session ID
	if idpSession, ok := session.(SessionWithIDP); ok {
		serialized.IDPSessionID = idpSession.GetIDPSessionID()
	}

	// Handle sessions with JWT claims
	if jwtSession, ok := session.(SessionWithJWT); ok {
		if claims := jwtSession.GetJWTClaims(); claims != nil {
			// Use ToMapClaims to extract all claims as a map
			mapClaims := claims.ToMapClaims()
			serialized.JWTClaims = &serializedJWTClaims{
				Extra: mapClaims,
			}
			// Extract standard claims from the map
			if sub, ok := mapClaims["sub"].(string); ok {
				serialized.JWTClaims.Subject = sub
			}
			if iss, ok := mapClaims["iss"].(string); ok {
				serialized.JWTClaims.Issuer = iss
			}
			if aud, ok := mapClaims["aud"].([]string); ok {
				serialized.JWTClaims.Audience = aud
			}
			if jti, ok := mapClaims["jti"].(string); ok {
				serialized.JWTClaims.JTI = jti
			}
			if scope, ok := mapClaims["scope"].([]string); ok {
				serialized.JWTClaims.Scope = scope
			} else if scopeStr, ok := mapClaims["scope"].(string); ok {
				serialized.JWTClaims.Scope = strings.Fields(scopeStr)
			}
			// Extract time claims
			if exp, ok := mapClaims["exp"].(float64); ok {
				serialized.JWTClaims.ExpiresAt = int64(exp)
			}
			if iat, ok := mapClaims["iat"].(float64); ok {
				serialized.JWTClaims.IssuedAt = int64(iat)
			}
			if nbf, ok := mapClaims["nbf"].(float64); ok {
				serialized.JWTClaims.NotBefore = int64(nbf)
			}
		}
		if header := jwtSession.GetJWTHeader(); header != nil && header.Extra != nil {
			serialized.JWTHeader = header.Extra
		}
	}

	return serialized
}

// deserializeSession converts serialized data back to a fosite.Session using the provided factory.
func deserializeSession(data *serializedSession, factory SessionFactory) fosite.Session {
	if data == nil || factory == nil {
		return nil
	}

	// Create the session using the factory
	session := factory(data.Subject, data.IDPSessionID, "")

	// Restore expiration times if supported
	if jwtSession, ok := session.(SessionWithJWT); ok {
		for tokenType, exp := range data.ExpiresAt {
			jwtSession.SetExpiresAt(tokenType, time.Unix(exp, 0))
		}
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
// sessionFactory is used to create sessions during deserialization.
func deserializeRequester(
	data *serializedRequester,
	clientLookup func(string) fosite.Client,
	sessionFactory SessionFactory,
) (fosite.Requester, error) {
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
		Session:     deserializeSession(data.Session, sessionFactory),
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
func unmarshalRequester(
	data []byte,
	clientLookup func(string) fosite.Client,
	sessionFactory SessionFactory,
) (fosite.Requester, error) {
	var serialized serializedRequester
	if err := json.Unmarshal(data, &serialized); err != nil {
		return nil, fmt.Errorf("failed to unmarshal requester: %w", err)
	}
	return deserializeRequester(&serialized, clientLookup, sessionFactory)
}

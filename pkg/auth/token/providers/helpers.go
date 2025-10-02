package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// parseIntrospectionClaims parses RFC 7662 introspection response into JWT claims
func parseIntrospectionClaims(r io.Reader) (jwt.MapClaims, error) {
	var j struct {
		Active bool                   `json:"active"`
		Exp    *float64               `json:"exp,omitempty"`
		Sub    string                 `json:"sub,omitempty"`
		Aud    interface{}            `json:"aud,omitempty"`
		Scope  string                 `json:"scope,omitempty"`
		Iss    string                 `json:"iss,omitempty"`
		Extra  map[string]interface{} `json:"-"`
	}

	if err := json.NewDecoder(r).Decode(&j); err != nil {
		return nil, fmt.Errorf("failed to decode introspection JSON: %w", err)
	}
	if !j.Active {
		return nil, ErrInvalidToken
	}

	claims := jwt.MapClaims{}
	if j.Exp != nil {
		claims["exp"] = *j.Exp
	}
	if j.Sub != "" {
		claims["sub"] = strings.TrimSpace(j.Sub)
	}
	if j.Aud != nil {
		claims["aud"] = j.Aud
	}
	if j.Scope != "" {
		claims["scope"] = strings.TrimSpace(j.Scope)
	}
	if j.Iss != "" {
		claims["iss"] = strings.TrimSpace(j.Iss)
	}
	for k, v := range j.Extra {
		claims[k] = v
	}

	return claims, nil
}

package middleware

import (
	"encoding/json"
	"net/http"

	"github.com/stacklok/toolhive/pkg/logger"
)

// RFC9728AuthInfo represents the OAuth Protected Resource metadata as defined in RFC 9728
type RFC9728AuthInfo struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	JWKSURI                string   `json:"jwks_uri"`
	ScopesSupported        []string `json:"scopes_supported"`
}

// NewAuthInfoHandler creates an HTTP handler that returns RFC-9728 compliant OAuth Protected Resource metadata
func NewAuthInfoHandler(jwksURL, resourceURL string, scopes []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers for all requests
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Allow all origins if none specified. This should be fine because this is a discovery endpoint.
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		// At least mcp-inspector requires these headers to be set for CORS. It seems to be a little
		// off since this is a discovery endpoint, but let's make the inspector happy.
		w.Header().Set("Access-Control-Allow-Headers", "mcp-protocol-version, Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400") // 24 hours

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// if resourceURL is not set, return 404 as we shouldn't presume a resource URL
		if resourceURL == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Use provided scopes or default to 'openid'
		supportedScopes := scopes
		if len(supportedScopes) == 0 {
			supportedScopes = []string{"openid"}
		}

		authInfo := RFC9728AuthInfo{
			Resource:               resourceURL,
			AuthorizationServers:   []string{jwksURL}, // Use JWKS URL as the authorization server
			BearerMethodsSupported: []string{"header"},
			JWKSURI:                jwksURL,
			ScopesSupported:        supportedScopes,
		}

		// Set content type
		w.Header().Set("Content-Type", "application/json")

		// Encode and send the response
		if err := json.NewEncoder(w).Encode(authInfo); err != nil {
			logger.Errorf("Failed to encode OAuth discovery response: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	})
}

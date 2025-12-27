package authserver

import (
	"log/slog"
	"net/http"

	"github.com/ory/fosite"
)

// Router provides HTTP handlers for the OAuth authorization server endpoints.
type Router struct {
	logger   *slog.Logger
	provider fosite.OAuth2Provider
	config   *OAuth2Config
	storage  Storage
	upstream IDPProvider
}

// RouterOption configures a Router instance.
type RouterOption func(*Router)

// WithIDPProvider sets the upstream IDP provider for the router.
func WithIDPProvider(upstream IDPProvider) RouterOption {
	return func(r *Router) {
		r.upstream = upstream
	}
}

// NewRouter creates a new Router with the given dependencies.
func NewRouter(
	logger *slog.Logger,
	provider fosite.OAuth2Provider,
	config *OAuth2Config,
	storage Storage,
	opts ...RouterOption,
) *Router {
	if logger == nil {
		logger = slog.Default()
	}

	r := &Router{
		logger:   logger,
		provider: provider,
		config:   config,
		storage:  storage,
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// Routes registers the OAuth/OIDC endpoints on the provided mux.
// This method calls both OAuthRoutes and WellKnownRoutes for backward compatibility.
func (r *Router) Routes(mux *http.ServeMux) {
	r.OAuthRoutes(mux)
	r.WellKnownRoutes(mux)
}

// OAuthRoutes registers only the OAuth endpoints (authorize, callback, token, register) on the provided mux.
func (r *Router) OAuthRoutes(mux *http.ServeMux) {
	// Authorization endpoint (initiates OAuth flow)
	mux.HandleFunc("GET /oauth/authorize", r.AuthorizeHandler)

	// Callback endpoint (receives upstream IDP callback)
	mux.HandleFunc("GET /oauth/callback", r.CallbackHandler)

	// Token endpoint
	mux.HandleFunc("POST /oauth/token", r.TokenHandler)

	// Dynamic Client Registration endpoint (RFC 7591)
	mux.HandleFunc("POST /oauth2/register", r.RegisterClientHandler)
}

// WellKnownRoutes registers only the well-known endpoints (JWKS, OIDC discovery) on the provided mux.
func (r *Router) WellKnownRoutes(mux *http.ServeMux) {
	// JWKS endpoint
	mux.HandleFunc("GET /.well-known/jwks.json", r.JWKSHandler)

	// OIDC Discovery endpoint
	mux.HandleFunc("GET /.well-known/openid-configuration", r.OIDCDiscoveryHandler)
}

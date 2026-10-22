// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	josev3 "github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"golang.org/x/time/rate"

	"github.com/stacklok/toolhive/pkg/authserver/internal/consentpolicy"
	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/deviceflow"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// server is the internal implementation of the Server interface.
type server struct {
	handler http.Handler
	// oauthHandler is the handler behind handler, kept so Close can release
	// the metric callback it registered.
	oauthHandler *handlers.Handler
	// storage is the active storage backend, potentially wrapped by decorators
	// such as CIMDStorageDecorator. Code that needs the concrete type must walk
	// the Unwrap() chain rather than asserting directly.
	storage storage.Storage
	// dcrStore is the same storage.Storage value asserted to
	// storage.DCRCredentialStore. The assertion runs once at construction
	// (newServer) so DCRStore() is a field read rather than re-asserting on
	// every call, and a backend that does not implement DCRCredentialStore
	// is rejected at boot rather than at first DCR resolve.
	dcrStore storage.DCRCredentialStore
	// upstreamRefresher is the single shared refresher instance constructed in
	// newServer. Storing the interface type (not *upstreamTokenRefresher) keeps
	// the nil-return contract: newUpstreamTokenRefresher returns a true nil
	// interface when there are no upstreams, so callers can check == nil safely.
	upstreamRefresher storage.UpstreamTokenRefresher
	upstreams         []handlers.NamedUpstream
	// trustedIssuerValidator is the single MultiIssuerTokenValidator shared by
	// the token-exchange and JWT-bearer handlers, built in buildProvider when
	// TrustedIssuers are configured (nil otherwise). Held here so Close can shut
	// down its per-issuer JWKS refresh worker pools; nothing else releases them.
	trustedIssuerValidator *tokenexchange.MultiIssuerTokenValidator
	// spiffeBundleSource supplies both X.509 and JWT bundles and owns the
	// refresh workers for the server lifetime.
	spiffeBundleSource *spiffeMultiDomainBundleSource
}

// DefaultUpstreamFactory creates the production upstream provider based on type.
// For OIDC providers, it creates an OIDCProviderImpl with discovery and ID token
// validation. For OAuth2 providers, it creates a BaseOAuth2Provider.
//
// It is exported so a Config.UpstreamFactory implementation can delegate to the
// built-in behavior for the upstreams it does not want to handle itself.
func DefaultUpstreamFactory(ctx context.Context, cfg *UpstreamConfig) (upstream.OAuth2Provider, error) {
	switch cfg.Type {
	case UpstreamProviderTypeOIDC:
		return upstream.NewOIDCProvider(ctx, cfg.OIDCConfig)
	case UpstreamProviderTypeOAuth2:
		return upstream.NewOAuth2Provider(cfg.OAuth2Config)
	default:
		return nil, fmt.Errorf("unsupported upstream type: %s", cfg.Type)
	}
}

// buildUpstreams constructs the ordered upstream provider list using
// cfg.UpstreamFactory, or DefaultUpstreamFactory when the caller supplied none.
// A provider that fails to construct aborts the whole list — but the providers
// already built have live connection pools, so they are drained before the
// error is returned.
func buildUpstreams(ctx context.Context, cfg Config) ([]handlers.NamedUpstream, error) {
	factory := cfg.UpstreamFactory
	if factory == nil {
		factory = DefaultUpstreamFactory
	}
	upstreams := make([]handlers.NamedUpstream, 0, len(cfg.Upstreams))
	for i := range cfg.Upstreams {
		upCfg := &cfg.Upstreams[i]
		slog.Debug("creating upstream IDP provider", "type", upCfg.Type, "name", upCfg.Name)
		provider, err := factory(ctx, upCfg)
		if err != nil {
			closeUpstreamIdleConnections(upstreams)
			return nil, fmt.Errorf("failed to create upstream provider %q: %w", upCfg.Name, err)
		}
		// A nil provider would reach the authorization chain and panic on the
		// first /oauth/authorize rather than failing at boot, so a custom
		// factory cannot use one to drop an upstream.
		if isNilProvider(provider) {
			closeUpstreamIdleConnections(upstreams)
			return nil, fmt.Errorf("upstream factory returned a nil provider for upstream %q", upCfg.Name)
		}
		upstreams = append(upstreams, handlers.NamedUpstream{
			Name: upCfg.Name, DisplayName: upCfg.DisplayName, Provider: provider,
		})
		slog.Debug("upstream IDP provider configured", "type", upCfg.Type, "name", upCfg.Name)
	}
	return upstreams, nil
}

// isNilProvider reports whether an OAuth2Provider is unusable — either a nil
// interface value or an interface holding a nil pointer.
func isNilProvider(provider upstream.OAuth2Provider) bool {
	if provider == nil {
		return true
	}
	v := reflect.ValueOf(provider)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// releaseOnConstructionError runs newServer's error-path cleanup: it drains the
// upstream idle connections and shuts down the trusted-issuer validator's JWKS
// worker pools (when one was built). Errors are logged, not returned — retErr is
// what the caller acts on, but a pool that fails to drain here would otherwise
// leave its goroutines running with no diagnostic.
func releaseOnConstructionError(upstreams []handlers.NamedUpstream, validator *tokenexchange.MultiIssuerTokenValidator) {
	closeUpstreamIdleConnections(upstreams)
	if validator != nil {
		if err := validator.Close(); err != nil {
			slog.Warn("failed to shut down trusted-issuer validator during server construction cleanup", "error", err)
		}
	}
}

// closeUpstreamIdleConnections drains the pooled idle connections of every
// upstream that implements the optional upstream.IdleConnectionCloser
// capability; see that interface for why it is optional and what it exempts.
func closeUpstreamIdleConnections(upstreams []handlers.NamedUpstream) {
	for _, u := range upstreams {
		if closer, ok := u.Provider.(upstream.IdleConnectionCloser); ok {
			slog.Debug("closing upstream idle connections", "name", u.Name)
			closer.CloseIdleConnections()
		}
	}
}

func closeSPIFFEBundleSourceOnConstructionError(source *spiffeMultiDomainBundleSource, retErr *error) {
	if *retErr == nil || source == nil {
		return
	}
	if err := source.Close(); err != nil {
		slog.Warn("failed to shut down SPIFFE bundle source during server construction cleanup", "error", err)
	}
}

func spiffeX509BundleSource(source *spiffeMultiDomainBundleSource) x509bundle.Source {
	if source == nil {
		return nil
	}
	return source
}

func spiffeJWTBundleSource(source *spiffeMultiDomainBundleSource) jwtbundle.Source {
	if source == nil {
		return nil
	}
	return source
}

// loadKeyMaterial returns the signing key and the public keys for the JWKS.
// Every key the provider reports (signing key plus any fallbacks configured
// for rotation) must reach the published JWKS, or promoting a fallback key to
// primary invalidates every outstanding token instead of opening the
// documented rotation overlap window (#6451).
func loadKeyMaterial(ctx context.Context, provider keys.KeyProvider) (*keys.SigningKeyData, []*keys.PublicKeyData, error) {
	signingKey, err := provider.SigningKey(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get signing key: %w", err)
	}
	publicKeys, err := provider.PublicKeys(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get public keys: %w", err)
	}
	return signingKey, publicKeys, nil
}

// authorizationServerParams maps cfg and the loaded key material onto the
// fosite provider parameters. newServer and the test provider builder both use
// it so handler selection cannot drift between them. spiffeRegistry and
// bundleSource may be nil.
func authorizationServerParams(
	cfg Config, signingKey *keys.SigningKeyData, additionalPublicKeys []*keys.PublicKeyData,
	spiffeRegistry *SPIFFEAssociationRegistry, bundleSource *spiffeMultiDomainBundleSource, stor storage.Storage,
) *oauthserver.AuthorizationServerParams {
	return &oauthserver.AuthorizationServerParams{
		Issuer:                                    cfg.Issuer,
		AccessTokenLifespan:                       cfg.AccessTokenLifespan,
		RefreshTokenLifespan:                      cfg.RefreshTokenLifespan,
		AuthCodeLifespan:                          cfg.AuthCodeLifespan,
		HMACSecrets:                               cfg.HMACSecrets,
		SigningKeyID:                              signingKey.KeyID,
		SigningKeyAlgorithm:                       signingKey.Algorithm,
		SigningKey:                                signingKey.Key,
		AdditionalPublicKeys:                      additionalPublicKeys,
		ScopesSupported:                           cfg.ScopesSupported,
		BaselineClientScopes:                      cfg.BaselineClientScopes,
		AllowedAudiences:                          cfg.AllowedAudiences,
		SubjectIdentifierAudiences:                cfg.SubjectIdentifierAudiences,
		AccessTokenType:                           cfg.AccessTokenType,
		AuthorizationEndpointBaseURL:              cfg.AuthorizationEndpointBaseURL,
		CIMDEnabled:                               cfg.CIMDEnabled,
		AllowConfidentialClientRegistration:       cfg.AllowConfidentialClientRegistration,
		AllowPrivateKeyJWTRegistration:            cfg.AllowPrivateKeyJWTRegistration,
		HasStaticDelegateClients:                  len(cfg.DelegateClients) > 0,
		InsecureAllowHTTP:                         cfg.InsecureAllowHTTP,
		InsecureAllowConfidentialOverLoopbackHTTP: cfg.InsecureAllowConfidentialOverLoopbackHTTP,
		ForceConfidentialRedirectURIs:             cfg.ForceConfidentialRedirectURIs,
		DisableTokenExchange:                      cfg.DisableTokenExchange,
		JWTBearerGrantEnabled:                     JWTBearerGrantEnabled(cfg.TrustedIssuers),
		DeviceFlowEnabled:                         cfg.DeviceFlowEnabled,
		DeviceCodeInterval:                        cfg.DeviceCodeInterval,
		SPIFFEClientResolver:                      newSPIFFEClientResolver(spiffeRegistry, stor),
		SPIFFEX509BundleSource:                    spiffeX509BundleSource(bundleSource),
		SPIFFEJWTBundleSource:                     spiffeJWTBundleSource(bundleSource),
	}
}

// newServer creates a new OAuth authorization server.
// The opts parameter allows injecting dependencies for testing.
func newServer(ctx context.Context, cfg Config, stor storage.Storage) (_ *server, retErr error) {
	slog.Debug("initializing OAuth authorization server")

	// Apply defaults to config
	if err := cfg.applyDefaults(); err != nil {
		return nil, fmt.Errorf("failed to apply config defaults: %w", err)
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	logConfidentialClientStartup(cfg.AllowConfidentialClientRegistration)

	stor, dcrStore, bundleSource, spiffeRegistry, err := prepareServerStorageAndRegisterClients(ctx, cfg, stor)
	if err != nil {
		return nil, err
	}
	defer closeSPIFFEBundleSourceOnConstructionError(bundleSource, &retErr)

	slog.Debug("creating OAuth2 configuration")

	signingKey, additionalPublicKeys, err := loadKeyMaterial(ctx, cfg.KeyProvider)
	if err != nil {
		return nil, err
	}

	oauthParams := authorizationServerParams(cfg, signingKey, additionalPublicKeys, spiffeRegistry, bundleSource, stor)
	authServerConfig, err := oauthserver.NewAuthorizationServerConfig(oauthParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth2 config: %w", err)
	}

	slog.Debug("oauth2 configuration created",
		"access_token_lifespan", cfg.AccessTokenLifespan,
		"refresh_token_lifespan", cfg.RefreshTokenLifespan,
		"auth_code_lifespan", cfg.AuthCodeLifespan,
	)

	// Build ordered upstream provider list from all configured upstreams.
	upstreams, err := buildUpstreams(ctx, cfg)
	if err != nil {
		return nil, err
	}
	// trustedIssuerValidator is assigned from buildProvider below; the deferred
	// cleanup captures it by reference so a failure after it is built releases
	// its per-issuer JWKS worker pools rather than leaking them.
	var trustedIssuerValidator *tokenexchange.MultiIssuerTokenValidator
	// Defense in depth: the failure returns below would otherwise abandon
	// upstream providers holding a live pool, or the trusted-issuer validator's
	// JWKS worker pools. The upstream drain is not reachable today (Validate
	// covers every precondition they check) — it is here so a future step that
	// fails after touching the network, or a new error return added above it, is
	// covered by default; the validator shutdown covers the reachable failure
	// between buildProvider and the return below.
	defer func() {
		if retErr != nil {
			releaseOnConstructionError(upstreams, trustedIssuerValidator)
		}
	}()

	// Run one-shot bulk migration of legacy data before handler construction.
	// TODO(migration): Remove once all deployments have upgraded past this version.
	if err := runLegacyMigration(ctx, stor, cfg.Upstreams); err != nil {
		return nil, err
	}

	// Give the handler a refresher so the authorization chain can transparently
	// refresh an expired upstream leg during login instead of skipping it and
	// failing later at MCP-request token-swap time. The same instance is stored
	// on the server so UpstreamTokenRefresher() returns the identical object,
	// and handed to the upstream refresh guard, ensuring every path shares one
	// process-local singleflight.Group keyed by opaque storage-row identity.
	refresher := newUpstreamTokenRefresher(upstreams, stor, cfg.RefreshTokenLifespan)

	// Create fosite provider with the configured storage decorators.
	slog.Debug("creating fosite OAuth2 provider")
	fositeProvider, trustedIssuerValidator, err := buildProvider(cfg, authServerConfig, stor, refresher, upstreams)
	if err != nil {
		return nil, fmt.Errorf("failed to create fosite OAuth2 provider: %w", err)
	}

	handlerInstance, err := handlers.NewHandler(fositeProvider, authServerConfig, stor, upstreams,
		buildHandlerOptions(refresher, cfg.UpstreamFilter, cfg.RegistrationRateLimit, cfg.ConsentExemptClientIDs)...)
	if err != nil {
		return nil, fmt.Errorf("failed to create handler: %w", err)
	}

	// Create HTTP handler serving all endpoints
	router := handlerInstance.Routes()

	slog.Debug("oauth authorization server initialized",
		"issuer", cfg.Issuer,
		"consent_exemption_enabled", len(cfg.ConsentExemptClientIDs) > 0,
		"consent_exempt_client_count", len(cfg.ConsentExemptClientIDs),
	)

	return &server{
		handler:                router,
		oauthHandler:           handlerInstance,
		storage:                stor,
		dcrStore:               dcrStore,
		upstreams:              upstreams,
		upstreamRefresher:      refresher,
		trustedIssuerValidator: trustedIssuerValidator,
		spiffeBundleSource:     bundleSource,
	}, nil
}

// prepareServerStorageAndRegisterClients decorates storage, validates configured
// consent-exempt clients against the prepared registration view, and registers
// delegate clients before returning the storage handles used by server construction.
func prepareServerStorageAndRegisterClients(
	ctx context.Context, cfg Config, stor storage.Storage,
) (prepared storage.Storage, dcrStore storage.DCRCredentialStore,
	bundleSource *spiffeMultiDomainBundleSource, spiffeRegistry *SPIFFEAssociationRegistry, retErr error,
) {
	if stor == nil {
		return nil, nil, nil, nil, fmt.Errorf("storage is required")
	}

	// Storage no longer embeds DCRCredentialStore (the embed widened secret
	// reach to every Storage consumer); obtain the DCR-capable handle via an
	// explicit assertion at the boundary. The per-backend
	// `var _ DCRCredentialStore = (*MemoryStorage)(nil)` /
	// `var _ DCRCredentialStore = (*RedisStorage)(nil)` checks make this
	// provably safe for the production backends; surfacing a bad backend as
	// a constructor error keeps misconfiguration fail-loud at boot rather
	// than at first DCR resolve.
	baseStore := storage.Unwrap(stor)
	var ok bool
	dcrStore, ok = baseStore.(storage.DCRCredentialStore)
	if !ok {
		return nil, nil, nil, nil, fmt.Errorf("storage backend %T does not implement storage.DCRCredentialStore", baseStore)
	}

	bundleSource, retErr = newSPIFFEMultiDomainBundleSource(ctx, cfg.SPIFFETrust)
	if retErr != nil {
		return nil, nil, nil, nil, fmt.Errorf("create SPIFFE bundle source: %w", retErr)
	}
	defer closeSPIFFEBundleSourceOnConstructionError(bundleSource, &retErr)

	admitted, err := newClientAdmissionStorage(cfg, stor)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	prepared, spiffeRegistry, retErr = decorateStorageForSPIFFE(ctx, cfg, admitted)
	if retErr != nil {
		return nil, nil, nil, nil, retErr
	}
	if retErr = validateConsentExemptClients(ctx, prepared, cfg.ConsentExemptClientIDs); retErr != nil {
		return nil, nil, nil, nil, retErr
	}
	if retErr = registerDelegateClients(ctx, prepared, cfg.DelegateClients); retErr != nil {
		return nil, nil, nil, nil, retErr
	}
	return prepared, dcrStore, bundleSource, spiffeRegistry, nil
}

// delegateClientIDs returns the client IDs of the configured delegate clients.
func delegateClientIDs(delegateClients []DelegateClient) []string {
	ids := make([]string, len(delegateClients))
	for i, c := range delegateClients {
		ids[i] = c.ClientID
	}
	return ids
}

// newClientAdmissionStorage wraps stor so a configured client is served only
// while cfg declares it. Consent-exempt IDs deliberately do not vouch. It must
// sit below the CIMD decorator: callbackClient's fallback (ClientStoreBelowCIMD)
// and CIMD's persisted-row fallback read below that decorator and must still
// pass admission.
func newClientAdmissionStorage(cfg Config, stor storage.Storage) (storage.Storage, error) {
	admission, err := storage.NewClientAdmissionStorage(stor, delegateClientIDs(cfg.DelegateClients))
	if err != nil {
		return nil, fmt.Errorf("create client admission storage: %w", err)
	}
	return admission, nil
}

func validateConsentExemptClients(ctx context.Context, stor storage.Storage, clientIDs []string) error {
	for _, clientID := range clientIDs {
		client, err := stor.GetClient(ctx, clientID)
		if err != nil {
			return fmt.Errorf("failed to resolve consent-exempt client %q: %w", clientID, err)
		}
		if !consentpolicy.IsEligibleClient(client) {
			return fmt.Errorf("consent-exempt client %q is missing or ineligible", clientID)
		}
	}
	return nil
}

func registerDelegateClients(ctx context.Context, stor storage.Storage, delegateClients []DelegateClient) error {
	for _, delegateClient := range delegateClients {
		client, err := registration.NewStaticDelegateClient(registration.Config{
			ID:                                delegateClient.ClientID,
			Secret:                            delegateClient.ClientSecret,
			TokenEndpointAuthMethod:           delegateClient.TokenEndpointAuthMethod,
			TokenEndpointAuthSigningAlgorithm: delegateClient.TokenEndpointAuthSigningAlg,
			JSONWebKeys:                       delegateClient.JSONWebKeys,
			GrantTypes:                        delegateClient.GrantTypes,
			RedirectURIs:                      delegateClient.RedirectURIs,
			Scopes:                            delegateClient.Scopes,
			Audience:                          delegateClient.Audiences,
		})
		if err != nil {
			return fmt.Errorf("failed to create delegate client %q: %w", delegateClient.ClientID, err)
		}
		if err := stor.ReconcileConfiguredClient(ctx, client); err != nil {
			return fmt.Errorf("failed to register delegate client %q: %w", delegateClient.ClientID, err)
		}
		slog.Warn("delegate client has blanket self-issued token exchange rights: "+
			"it may exchange any user's self-issued ToolHive token for a delegated token, "+
			"with no per-subject binding to the client that originally obtained that token",
			"client_id", delegateClient.ClientID, "scopes", delegateClient.Scopes, "audiences", delegateClient.Audiences)
	}
	return nil
}

// decorateStorageForSPIFFE resolves the immutable association registry from the
// validated SPIFFE trust model and installs the static overlay outside CIMD.
// A nil cfg.SPIFFETrust means no SPIFFE associations are configured, which
// yields a nil registry and leaves the storage chain unchanged. The returned
// registry is also wired onto the fosite client-authentication strategy by the
// caller (see newSPIFFEClientResolver); it is nil when no SPIFFE trust is
// configured.
func decorateStorageForSPIFFE(
	ctx context.Context, cfg Config, stor storage.Storage,
) (storage.Storage, *SPIFFEAssociationRegistry, error) {
	registry, err := NewSPIFFEAssociationRegistry(cfg.SPIFFETrust)
	if err != nil {
		return nil, nil, fmt.Errorf("create SPIFFE association registry: %w", err)
	}

	// Install dynamic CIMD lookup before the static SPIFFE overlay so configured
	// clients always take precedence over remotely resolved HTTPS client IDs.
	stor, err = decorateStorageForCIMD(cfg, stor)
	if err != nil {
		return nil, nil, err
	}
	clients, err := registry.staticClients()
	if err != nil {
		return nil, nil, fmt.Errorf("build SPIFFE static clients: %w", err)
	}
	stor, err = storage.NewSPIFFEStorageDecorator(ctx, stor, clients)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize SPIFFE client overlay: %w", err)
	}
	return stor, registry, nil
}

// newSPIFFEClientResolver returns the resolver the SPIFFE client-authentication
// strategy uses to turn a verified SPIFFE identity into its configured OAuth
// client. Both credential types resolve through this one function so that an
// X.509-SVID and a JWT-SVID for the same association cannot reach different
// authorization outcomes.
//
// It returns a nil resolver when no SPIFFE trust is configured. Returning a
// non-nil func closing over a nil registry would make the strategy's
// resolver != nil check pass when nothing is actually configured.
//
// Method uses the shared leaf-package type because package server cannot
// import authserver (authserver imports server). Package authserver aliases
// that type, so both sides share one set of method values.
func newSPIFFEClientResolver(
	registry *SPIFFEAssociationRegistry, stor storage.Storage,
) oauthserver.SPIFFEClientResolver {
	if registry == nil {
		return nil
	}
	return func(
		ctx context.Context, spiffeID, clientID string, method spiffeauth.SPIFFEAuthenticationMethod,
	) (fosite.Client, error) {
		principal, err := registry.Resolve(spiffeID, clientID, method)
		if err != nil {
			return nil, err
		}
		return stor.GetClient(ctx, principal.ClientID())
	}
}

// decorateStorageForCIMD wraps stor with the CIMD decorator when CIMD is
// enabled, so GetClient calls for HTTPS client_id values are intercepted at
// the fosite level (not just the handler level).
//
// When CIMD is disabled, stor is instead wrapped with
// storage.NewCIMDShapeGuardStorage so that a URL-shaped client_id can never
// resolve from a stale row a prior CIMD-enabled period may have
// write-through persisted (see cimdShapeGuardStorage's doc comment for why
// disabling CIMD alone does not evict or invalidate such a row).
func decorateStorageForCIMD(cfg Config, stor storage.Storage) (storage.Storage, error) {
	if !cfg.CIMDEnabled {
		return storage.NewCIMDShapeGuardStorage(stor), nil
	}
	if len(cfg.BaselineClientScopes) > 0 {
		slog.Warn("CIMD is enabled with baseline_client_scopes configured; "+
			"any third-party client resolved via CIMD will also receive these scopes — "+
			"ensure they are scopes you would grant by default to any unknown client",
			"baseline_client_scopes", cfg.BaselineClientScopes)
	}
	decorated, err := storage.NewCIMDStorageDecorator(stor, storage.CIMDDecoratorConfig{
		Enabled:      true,
		CacheMaxSize: cfg.CIMDCacheMaxSize,
		FallbackTTL:  cfg.CIMDCacheFallbackTTL,
		// A refresh arrives about one access-token lifetime after the client's
		// last use, so any smaller bound never covers a refresh: on defaults the
		// cache entry is ~55m past its 5m expiry when the refresh arrives.
		MaxStale:             cfg.AccessTokenLifespan,
		ScopesSupported:      cfg.ScopesSupported,
		BaselineClientScopes: cfg.BaselineClientScopes,
		AllowedAudiences:     cfg.AllowedAudiences,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize CIMD storage decorator: %w", err)
	}
	return decorated, nil
}

// JWTBearerGrantEnabled reports whether any trusted issuer has the RFC 7523
// JWT-bearer grant configured.
func JWTBearerGrantEnabled(trustedIssuers []tokenexchange.TrustedIssuer) bool {
	for _, issuer := range trustedIssuers {
		if issuer.JWTBearerGrant != nil {
			return true
		}
	}
	return false
}

// buildProvider assembles the fosite OAuth2 provider, registering the RFC 8693
// token-exchange handler and/or the RFC 7523 JWT-bearer handler as extension
// grants alongside the standard grants -- whichever of the two are enabled
// for cfg (token exchange can be disabled via canonical inbound grants
// configuration; JWT-bearer is enabled per JWTBearerGrantEnabled).
//
// refresher is the server's shared upstream token refresher (nil without
// upstreams) and upstreams are the resolved upstreams it refreshes, both used
// by the built-in upstream refresh guard.
//
// It returns the shared MultiIssuerTokenValidator (nil when no TrustedIssuers
// are configured, or when neither enabled grant would use it) so newServer
// can hold it and release its per-issuer JWKS worker pools on shutdown. On
// its own error paths it shuts that validator down before returning, since
// the caller never receives it.
func buildProvider(
	cfg Config, authServerConfig *oauthserver.AuthorizationServerConfig, stor storage.Storage,
	refresher storage.UpstreamTokenRefresher, upstreams []handlers.NamedUpstream,
) (_ fosite.OAuth2Provider, _ *tokenexchange.MultiIssuerTokenValidator, retErr error) {
	delegateIDs := delegateClientIDs(cfg.DelegateClients)
	jwtBearerEnabled := JWTBearerGrantEnabled(cfg.TrustedIssuers)

	// Built once, up front, and handed to whichever factories below actually
	// need it: otherwise each factory closure would build its own
	// MultiIssuerTokenValidator over the same trusted issuers at
	// fosite-compose time, doubling every issuer's JWKS cache and background
	// refresh goroutines — and, buried in a handler, leaving them unreachable
	// for shutdown. authServerConfig is the exact *AuthorizationServerConfig
	// each factory closure would otherwise receive at call time (see
	// createProvider/NewAuthorizationServer), so building it here first is
	// equivalent. Skipped entirely (shared stays nil) when neither grant that
	// consumes it is enabled -- trusted issuers configured with token
	// exchange disabled and JWT-bearer not enabled is a reachable, if
	// pointless, configuration, and building the validator anyway would
	// start live per-issuer JWKS refresh goroutines with no consumer for the
	// life of the server. NewSharedTrustedIssuerValidator itself also
	// returns nil when there are no trusted issuers.
	var shared *tokenexchange.MultiIssuerTokenValidator
	if !cfg.DisableTokenExchange || jwtBearerEnabled {
		var err error
		shared, err = tokenexchange.NewSharedTrustedIssuerValidator(authServerConfig, cfg.TrustedIssuers)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create shared trusted-issuer validator: %w", err)
		}
	}
	// Release the validator's JWKS worker pools if we fail before returning it
	// to newServer, which otherwise owns its shutdown.
	defer func() {
		if retErr != nil {
			if err := shared.Close(); err != nil {
				slog.Warn("failed to shut down trusted-issuer validator during provider build cleanup", "error", err)
			}
		}
	}()

	factories := make([]oauthserver.Factory, 0, 4)
	if !cfg.DisableTokenExchange {
		tokenExchangeFactory, err := tokenexchange.FactoryWithSharedTrustedIssuerValidator(
			cfg.DelegationTokenLifespan, cfg.TrustedIssuers, delegateIDs, shared,
			tokenexchange.WithAuthorizationDetailsRegistry(cfg.AuthorizationDetailsRegistry),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create token exchange factory: %w", err)
		}
		factories = append(factories, tokenExchangeFactory)
	}
	if jwtBearerEnabled {
		jwtBearerFactories, err := buildJWTBearerFactories(cfg.TrustedIssuers, shared)
		if err != nil {
			return nil, nil, err
		}
		factories = append(factories, jwtBearerFactories...)
	}
	if cfg.DeviceFlowEnabled {
		deviceFlowFactory, err := buildDeviceFlowFactory(cfg, stor)
		if err != nil {
			return nil, nil, err
		}
		factories = append(factories, deviceFlowFactory)
	}
	// Refresh-grant validators go last so each runs after the built-in refresh
	// handler has validated the grant.
	validators, err := refreshGrantValidatorChain(cfg, stor, refresher, upstreams)
	if err != nil {
		return nil, nil, err
	}
	factories = append(factories, refreshGrantHandlerFactories(validators)...)
	provider, err := createProvider(authServerConfig, stor, factories...)
	if err != nil {
		return nil, nil, err
	}
	return provider, shared, nil
}

// refreshGrantValidatorChain returns the refresh-grant validators in run
// order: the built-in upstream refresh guard (unless disabled or there is no
// refresher, because no upstreams are configured), then the embedder's
// Config.RefreshGrantHandlers. The guard's set of configured providers comes
// from upstreams, the same resolved list the refresher was built from.
func refreshGrantValidatorChain(
	cfg Config, stor storage.Storage, refresher storage.UpstreamTokenRefresher, upstreams []handlers.NamedUpstream,
) ([]fosite.TokenEndpointHandler, error) {
	if cfg.DisableUpstreamRefreshGuard || refresher == nil {
		return cfg.RefreshGrantHandlers, nil
	}
	upstreamNames := make([]string, len(upstreams))
	for i, u := range upstreams {
		upstreamNames[i] = u.Name
	}
	guard, err := newUpstreamRefreshGuard(stor, refresher, upstreamNames)
	if err != nil {
		return nil, fmt.Errorf("failed to create upstream refresh guard: %w", err)
	}
	return append([]fosite.TokenEndpointHandler{guard}, cfg.RefreshGrantHandlers...), nil
}

// buildJWTBearerFactories builds the RFC 7523 JWT-bearer factory together
// with the bound ID-JAG factory. The bound ID-JAG handler rides the same
// per-issuer JWT-bearer policy: enabling the grant enables both assertion
// forms, split by JOSE typ (plain assertions to JWTBearerHandler,
// oauth-id-jag+jwt to IDJAGHandler).
func buildJWTBearerFactories(
	trustedIssuers []tokenexchange.TrustedIssuer, shared *tokenexchange.MultiIssuerTokenValidator,
) ([]oauthserver.Factory, error) {
	jwtBearerFactory, err := tokenexchange.JWTBearerIssuanceFactory(trustedIssuers, shared)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT-bearer factory: %w", err)
	}
	idJAGFactory, err := tokenexchange.IDJAGIssuanceFactory(trustedIssuers, shared)
	if err != nil {
		return nil, fmt.Errorf("failed to create ID-JAG factory: %w", err)
	}
	return []oauthserver.Factory{jwtBearerFactory, idJAGFactory}, nil
}

// buildDeviceFlowFactory builds the RFC 8628 device authorization grant
// factory, validating that stor supports the device-code storage the grant
// requires.
func buildDeviceFlowFactory(cfg Config, stor storage.Storage) (oauthserver.Factory, error) {
	deviceStore, ok := storage.Unwrap(stor).(storage.DeviceCodeStorage)
	if !ok {
		return nil, fmt.Errorf("device flow enabled but storage backend %T does not implement storage.DeviceCodeStorage", stor)
	}
	interval := cfg.DeviceCodeInterval
	if interval <= 0 {
		interval = oauthserver.DefaultDeviceCodeInterval
	}
	return deviceflow.Factory(deviceStore, interval), nil
}

// buildHandlerOptions assembles the handlers.Option list for NewHandler: the
// refresher is always wired, the filter is added only when configured so a nil
// filter preserves walking every upstream, the register rate limit only when
// configured so a nil one keeps the handler default, and consent-exempt IDs
// only when the caller supplied a non-empty allowlist.
func buildHandlerOptions(
	refresher storage.UpstreamTokenRefresher,
	filter handlers.UpstreamFilter,
	registrationRateLimit *RegistrationRateLimitRunConfig,
	consentExemptClientIDs []string,
) []handlers.Option {
	opts := []handlers.Option{handlers.WithUpstreamRefresher(refresher)}
	if filter != nil {
		opts = append(opts, handlers.WithUpstreamFilter(filter))
	}
	if registrationRateLimit != nil {
		opts = append(opts, handlers.WithRegisterRateLimit(
			rate.Limit(registrationRateLimit.RequestsPerSecond), registrationRateLimit.Burst))
	}
	if len(consentExemptClientIDs) > 0 {
		opts = append(opts, handlers.WithConsentExemptClientIDs(consentExemptClientIDs))
	}
	return opts
}

// Handler returns the HTTP handler that serves all OAuth/OIDC endpoints.
func (s *server) Handler() http.Handler {
	return s.handler
}

// IDPTokenStorage returns the IDP token storage interface.
func (s *server) IDPTokenStorage() storage.UpstreamTokenStorage {
	return s.storage
}

// DCRStore returns the persistent DCR credential store the server is wired
// against. See the Server interface doc for SECURITY and lifecycle notes.
func (s *server) DCRStore() storage.DCRCredentialStore {
	return s.dcrStore
}

// UpstreamTokenRefresher returns the single shared refresher constructed in
// newServer. Both the handler's chain-walk path and the runtime token-swap
// path use this instance, ensuring concurrent refreshes for the same logical
// upstream-token row are deduplicated by one process-local singleflight.Group.
func (s *server) UpstreamTokenRefresher() storage.UpstreamTokenRefresher {
	return s.upstreamRefresher
}

// newUpstreamTokenRefresher builds a refresher over the given upstreams, or nil
// when there are none. Called once during newServer so the returned instance
// can be shared between the handler and the UpstreamTokenRefresher() accessor.
func newUpstreamTokenRefresher(
	upstreams []handlers.NamedUpstream,
	stor storage.UpstreamTokenStorage,
	refreshTokenLifespan time.Duration,
) storage.UpstreamTokenRefresher {
	if len(upstreams) == 0 {
		return nil
	}
	providers := make(map[string]upstream.OAuth2Provider, len(upstreams))
	for _, u := range upstreams {
		providers[u.Name] = u.Provider
	}
	return &upstreamTokenRefresher{
		providers:            providers,
		storage:              stor,
		refreshTokenLifespan: refreshTokenLifespan,
	}
}

// Compile-time check that the concrete server satisfies the capability the
// CloseIdleConnections function detects, so that call can never degrade to a
// no-op for a server built by New.
var _ idleConnectionCloser = (*server)(nil)
var _ spiffeX509AuthorityLister = (*server)(nil)

// SPIFFEX509Authorities returns the current X.509 authorities configured for
// SPIFFE client authentication.
func (s *server) SPIFFEX509Authorities() []*x509.Certificate {
	if s.spiffeBundleSource == nil {
		return nil
	}
	return s.spiffeBundleSource.SPIFFEX509Authorities()
}

// CloseIdleConnections drains the upstream providers' pooled connections; see
// the package-level CloseIdleConnections function for the contract.
func (s *server) CloseIdleConnections() {
	closeUpstreamIdleConnections(s.upstreams)
}

// UpdateUpstreamClientCredentials implements UpstreamCredentialsUpdater.
func (s *server) UpdateUpstreamClientCredentials(name string, creds upstream.ClientCredentials) error {
	name = ResolveUpstreamName(name)
	for _, u := range s.upstreams {
		if u.Name != name {
			continue
		}
		updater, ok := u.Provider.(upstream.ClientCredentialsUpdater)
		if !ok {
			return fmt.Errorf("upstream %q cannot replace its client registration", name)
		}
		return updater.UpdateClientCredentials(creds)
	}
	return fmt.Errorf("unknown upstream %q", name)
}

// Close releases resources held by the server: it drains upstream idle
// connections, unregisters the handler's metric callback, shuts down its
// SPIFFE bundle and trusted-issuer refresh workers, and closes storage. Shutdown errors are joined so neither hides
// the other.
func (s *server) Close() error {
	slog.Debug("closing OAuth authorization server")
	s.CloseIdleConnections()
	var errs []error
	if s.oauthHandler != nil {
		if err := s.oauthHandler.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.spiffeBundleSource != nil {
		if err := s.spiffeBundleSource.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to shut down SPIFFE bundle source: %w", err))
		}
	}
	if s.trustedIssuerValidator != nil {
		if err := s.trustedIssuerValidator.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to shut down trusted-issuer validator: %w", err))
		}
	}
	if err := s.storage.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// createProvider creates a fosite OAuth2Provider configured for the authorization code flow.
//
// Fosite is an OAuth 2.0 framework that implements the protocol details. We use
// server.NewAuthorizationServer which accepts server.Factory functions to register
// grant type handlers. The standard compose factories are wrapped via wrapComposeFactory
// and any extra factories (e.g., token exchange) are appended.
//
// The provider is configured with:
//   - JWT strategy for access tokens (asymmetric signing, distributed validation via JWKS)
//   - HMAC strategy for authorization codes and refresh tokens (symmetric, internal only)
//   - Authorization code grant (RFC 6749 Section 4.1)
//   - Refresh token grant (RFC 6749 Section 6)
//   - PKCE (RFC 7636) for public client security
//   - Any extra factories passed in (e.g., RFC 8693 token exchange, or the
//     embedder's Config.RefreshGrantHandlers), registered after the built-ins
func createProvider(
	authServerConfig *oauthserver.AuthorizationServerConfig,
	stor storage.Storage,
	extraFactories ...oauthserver.Factory,
) (fosite.OAuth2Provider, error) {
	slog.Debug("configuring fosite OAuth2 provider",
		"key_id", authServerConfig.SigningKey.KeyID,
		"algorithm", authServerConfig.SigningKey.Algorithm,
	)

	// Convert go-jose/v4 JWK to go-jose/v3 JWK for fosite compatibility.
	// Fosite v0.49.0 depends on go-jose/v3, while we use v4 internally.
	// This ensures the "kid" (key ID) is included in JWT headers so resource
	// servers can look up the correct public key from our JWKS endpoint.
	signingKeyV4 := authServerConfig.SigningKey
	signingKeyV3 := &josev3.JSONWebKey{
		Key:       signingKeyV4.Key,
		KeyID:     signingKeyV4.KeyID,
		Algorithm: signingKeyV4.Algorithm,
		Use:       signingKeyV4.Use,
	}

	// Create a composed token strategy:
	// - JWT strategy (outer): signs access tokens as JWTs using the asymmetric signing key
	// - HMAC strategy (inner): signs authorization codes and refresh tokens using HMACSecret
	//
	// Access tokens are JWTs so resource servers can validate them without calling us.
	// Auth codes and refresh tokens are opaque HMAC tokens since only we validate them.
	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (interface{}, error) { return signingKeyV3, nil },
		compose.NewOAuth2HMACStrategy(authServerConfig.Config),
		authServerConfig.Config,
	)
	// This signer signs only access tokens: auth codes and refresh tokens use
	// the HMAC strategy, and no ID-token strategy is registered. The type is
	// set at signing time, not in the session, because storage persists the
	// session's JWT header and a per-session typ would survive a configuration
	// change across refreshes. Tokens signed through this signer get typ: at+jwt
	// unless they set their own typ, so any future JWT that is not an access
	// token, such as an ID-JAG (#6898), must set its own typ or sign with its
	// own signer.
	if authServerConfig.AccessTokenType == oauthserver.AccessTokenTypeATJWT {
		jwtStrategy.Signer = oauthserver.NewATJWTAccessTokenSigner(jwtStrategy.Signer)
	}

	commonStrategy := &compose.CommonStrategy{CoreStrategy: jwtStrategy}

	// Wrap fosite's compose factories to match server.Factory signature.
	factories := []oauthserver.Factory{
		wrapComposeFactory(compose.OAuth2AuthorizeExplicitFactory), // Authorization code grant
		wrapComposeFactory(compose.OAuth2RefreshTokenGrantFactory), // Refresh token grant
		wrapComposeFactory(compose.OAuth2PKCEFactory),              // PKCE for public clients
	}
	factories = append(factories, extraFactories...)

	return oauthserver.NewAuthorizationServer(
		authServerConfig,
		stor,
		commonStrategy,
		factories...,
	)
}

// runLegacyMigration runs one-shot Redis data migrations before handlers are
// constructed. It is a no-op for non-Redis backends and passes through any
// decorator wrapping so the concrete type can be reached. The DCR client index
// backfill runs before /oauth/register is served, so the cap counts clients
// written before the index existed.
//
// A backfill failure is logged and startup continues, unlike a legacy upstream
// migration failure. Skipping the backfill only leaves the cap undercounting
// rows written before the index existed, which is how the server behaved
// before the cap, and the next start fills in whatever was missed (ZADD NX
// makes a rerun safe). Skipping an upstream migration would break existing
// data.
func runLegacyMigration(ctx context.Context, stor storage.Storage, upstreams []UpstreamConfig) error {
	base := storage.Unwrap(stor)
	rs, ok := base.(*storage.RedisStorage)
	if !ok {
		return nil
	}
	if err := rs.BackfillDCRClientIndex(ctx); err != nil {
		slog.Warn("DCR client index backfill failed; the DCR client cap may undercount existing clients until the next restart",
			"error", err)
	}
	for i := range upstreams {
		upCfg := &upstreams[i]
		if err := rs.MigrateLegacyUpstreamData(ctx, upCfg.Name, string(upCfg.Type)); err != nil {
			return fmt.Errorf("legacy data migration failed for upstream %q: %w", upCfg.Name, err)
		}
	}
	return nil
}

// logConfidentialClientStartup emits an Info line naming the consequence of
// enabling confidential-client DCR. Combining it with cleartext HTTP is
// rejected by Config.Validate (see ValidateConfidentialClientTransport), so
// that combination can no longer reach this function.
func logConfidentialClientStartup(allowConfidentialClientRegistration bool) {
	if !allowConfidentialClientRegistration {
		return
	}
	slog.Info("confidential-client dynamic registration is enabled: " +
		"this server issues client secrets over unauthenticated dynamic registration")
}

// wrapComposeFactory adapts a compose.Factory to a server.Factory.
// Compose factories take (fosite.Configurator, interface{}, interface{}) while
// server factories take (*AuthorizationServerConfig, fosite.Storage, any).
// The embedded *fosite.Config satisfies fosite.Configurator.
func wrapComposeFactory(cf compose.Factory) oauthserver.Factory {
	return func(config *oauthserver.AuthorizationServerConfig, storage fosite.Storage, strategy any) (any, error) {
		return cf(config.Config, storage, strategy), nil
	}
}

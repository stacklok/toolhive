// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ory/fosite"
	"golang.org/x/sync/singleflight"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/oauthproto/cimd"
)

// CIMDStorageDecorator wraps storage.Storage and intercepts GetClient calls
// for HTTPS client_id values, fetching and caching the corresponding Client
// ID Metadata Document instead of requiring prior DCR registration.
//
// All other Storage methods delegate to the underlying storage unchanged.
// Only GetClient is overridden. DCR clients (opaque IDs) continue to work
// exactly as before.
type CIMDStorageDecorator struct {
	Storage                                 // embed full interface — all methods delegate
	sf                   singleflight.Group // deduplicates concurrent fetches for the same URL
	cache                *lru.Cache[string, *cimdCacheEntry]
	ttl                  time.Duration
	scopesSupported      []string // AS-configured scopes; nil means accept any
	baselineClientScopes []string // unioned into every client's scope set, same as DCR
}

type cimdCacheEntry struct {
	client  fosite.Client
	expires time.Time
}

// CIMDDecoratorConfig holds the configuration for NewCIMDStorageDecorator.
// Using a struct prevents silent swaps of the two adjacent []string fields.
type CIMDDecoratorConfig struct {
	// Enabled returns base unchanged when false, avoiding an allocation.
	Enabled bool
	// CacheMaxSize is the maximum number of documents in the LRU cache (must be >= 1).
	CacheMaxSize int
	// FallbackTTL is the fixed TTL applied to every cache entry.
	FallbackTTL time.Duration
	// ScopesSupported is the AS scope allowlist; see pkg/authserver/config.go
	// applyDefaults for production guarantees. Pass nil in tests only.
	ScopesSupported []string
	// BaselineClientScopes is unioned into every CIMD client's scope set,
	// matching DCR handler behaviour.
	BaselineClientScopes []string
}

// NewCIMDStorageDecorator wraps base with CIMD client lookup.
// When cfg.Enabled is false it returns base unchanged (no allocation).
func NewCIMDStorageDecorator(base Storage, cfg CIMDDecoratorConfig) (Storage, error) {
	if !cfg.Enabled {
		return base, nil
	}

	if cfg.CacheMaxSize < 1 {
		return nil, fmt.Errorf("CIMD storage decorator cacheMaxSize must be >= 1, got %d", cfg.CacheMaxSize)
	}

	c, err := lru.New[string, *cimdCacheEntry](cfg.CacheMaxSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create CIMD LRU cache: %w", err)
	}

	return &CIMDStorageDecorator{
		Storage:              base,
		cache:                c,
		ttl:                  cfg.FallbackTTL,
		scopesSupported:      slices.Clone(cfg.ScopesSupported),
		baselineClientScopes: slices.Clone(cfg.BaselineClientScopes),
	}, nil
}

// GetClient intercepts HTTPS client_id values to resolve them via CIMD.
// Opaque DCR-issued IDs are delegated to the underlying storage unchanged.
func (d *CIMDStorageDecorator) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	if !oauthproto.IsClientIDMetadataDocumentURL(id) {
		return d.Storage.GetClient(ctx, id)
	}
	return d.fetchOrCached(ctx, id)
}

// cimdShapeGuardStorage wraps storage.Storage and rejects URL-shaped
// client_id values at GetClient without ever reading the underlying row.
//
// It exists for the case where CIMD is (or was previously) enabled and then
// disabled: the write-through persistence in CIMDStorageDecorator.fetch may
// have already left a resolved CIMD client's row in the underlying storage,
// and that row's TTL — up to DefaultDCRClientTTL — can outlive the config
// change. Without this guard, a bare Storage's GetClient does not check the
// shape of the id it is asked for, so it would happily resolve the stale row
// from a snapshot the document may have since changed or that the operator
// intended to revoke by turning CIMD off, with no re-fetch and therefore no
// re-validation of scopes, redirect URIs, or auth method. Wrapping the
// storage when CIMD is disabled closes that gap for both backends without
// changing GetClient's behavior for any opaque (non-URL) client_id.
type cimdShapeGuardStorage struct {
	Storage // embed full interface — all methods delegate
}

// GetClient rejects URL-shaped ids with fosite.ErrNotFound before consulting
// the wrapped storage, so a stale CIMD-origin row can never be resolved while
// CIMD is disabled. Opaque DCR-issued and pre-provisioned ids are delegated
// to the underlying storage unchanged.
func (g cimdShapeGuardStorage) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	if oauthproto.IsClientIDMetadataDocumentURL(id) {
		return nil, fosite.ErrNotFound.WithHint("CIMD is disabled on this server")
	}
	return g.Storage.GetClient(ctx, id)
}

// ConsumeAssertionJWT delegates assertion replay consumption to the wrapped
// backend, matching CIMDStorageDecorator's identical method: Storage embeds
// the interface, not the concrete backend, so this narrow capability is not
// promoted automatically and must be forwarded explicitly for callers (e.g.
// the RFC 7523 JWT-bearer grant factory) that need it through this wrapper.
func (g cimdShapeGuardStorage) ConsumeAssertionJWT(
	ctx context.Context, purpose, issuer, jti string, exp time.Time,
) error {
	consumer, ok := g.Storage.(AssertionJWTConsumer)
	if !ok {
		return fmt.Errorf("wrapped storage does not support assertion JWT replay consumption")
	}
	return consumer.ConsumeAssertionJWT(ctx, purpose, issuer, jti, exp)
}

// Unwrap returns the underlying storage so that type assertions (e.g. for
// storage.DCRCredentialStore in server_impl.go) can reach the concrete type.
func (g cimdShapeGuardStorage) Unwrap() Storage {
	return g.Storage
}

// NewCIMDShapeGuardStorage wraps base so that GetClient refuses any
// URL-shaped client_id, regardless of whether a matching row exists in base.
// Callers apply this when CIMD is disabled — see cimdShapeGuardStorage's doc
// comment for why a disabled decorator alone does not close this gap.
func NewCIMDShapeGuardStorage(base Storage) Storage {
	return cimdShapeGuardStorage{Storage: base}
}

// ConsumeAssertionJWT delegates assertion replay consumption to the wrapped
// backend. Storage intentionally does not include this narrow capability, so a
// decorator fails closed when its backend does not provide it.
func (d *CIMDStorageDecorator) ConsumeAssertionJWT(
	ctx context.Context, purpose, issuer, jti string, exp time.Time,
) error {
	consumer, ok := d.Storage.(AssertionJWTConsumer)
	if !ok {
		return fmt.Errorf("wrapped storage does not support assertion JWT replay consumption")
	}
	return consumer.ConsumeAssertionJWT(ctx, purpose, issuer, jti, exp)
}

// Unwrap returns the underlying storage so that type assertions (e.g. for
// storage.DCRCredentialStore in server_impl.go) can reach the concrete type.
func (d *CIMDStorageDecorator) Unwrap() Storage {
	return d.Storage
}

func (d *CIMDStorageDecorator) fetchOrCached(ctx context.Context, id string) (fosite.Client, error) {
	// Check cache first (outside singleflight to avoid holding the group lock for cache hits)
	if entry, ok := d.cache.Get(id); ok && time.Now().Before(entry.expires) {
		return entry.client, nil
	}

	// Deduplicate concurrent fetches for the same URL. The shared fetch uses a
	// context detached from the caller so that one caller cancelling does not
	// abort the in-flight request for other waiters. The HTTP client inside
	// FetchClientMetadataDocument enforces its own 5-second timeout.
	fetchCtx := context.WithoutCancel(ctx)
	result, err, _ := d.sf.Do(id, func() (interface{}, error) {
		// Re-check cache inside singleflight (another goroutine may have populated it)
		if entry, ok := d.cache.Get(id); ok && time.Now().Before(entry.expires) {
			return entry.client, nil
		}
		return d.fetch(fetchCtx, id)
	})
	if err != nil {
		return nil, err
	}
	client, ok := result.(fosite.Client)
	if !ok {
		return nil, fmt.Errorf("CIMD singleflight returned unexpected type %T", result)
	}
	return client, nil
}

func (d *CIMDStorageDecorator) fetch(ctx context.Context, id string) (fosite.Client, error) {
	doc, err := cimd.FetchClientMetadataDocument(ctx, id)
	if err != nil {
		// Log every rejection in fetch() at Warn: the errors returned here
		// surface to the client as a generic invalid_client/invalid_request
		// with the hint dropped by fosite's production error rendering, so
		// without a server-side log line these failures are undiagnosable
		// (see issue #6186).
		slog.WarnContext(ctx, "CIMD document fetch failed",
			"client_id", id, "error", err)
		return nil, fmt.Errorf("%w: %w", fosite.ErrNotFound.WithHint("CIMD fetch failed"), err)
	}

	// Negotiate the effective token_endpoint_auth_method rather than rejecting
	// outright on a declared-but-unsupported singular value. ErrInvalidClient:
	// the document was fetched successfully but its declared metadata violates
	// AS policy (distinct from ErrNotFound which means the document could not
	// be fetched at all).
	authMethod, ok := negotiateTokenEndpointAuthMethod(doc)
	if !ok {
		slog.WarnContext(ctx, "CIMD client rejected: unsupported token_endpoint_auth_method",
			"client_id", id, "token_endpoint_auth_method", doc.TokenEndpointAuthMethod)
		return nil, fmt.Errorf("%w: CIMD document at %s claims token_endpoint_auth_method %q "+
			"but this server only supports %q (token_endpoint_auth_methods_supported: %v)",
			fosite.ErrInvalidClient.WithHint("unsupported token_endpoint_auth_method"),
			id, doc.TokenEndpointAuthMethod, defaultCIMDTokenEndpointAuthMethod,
			doc.TokenEndpointAuthMethodsSupported)
	}
	if doc.TokenEndpointAuthMethod != "" && authMethod != doc.TokenEndpointAuthMethod {
		slog.Debug("CIMD: negotiated token_endpoint_auth_method from the client's supported list",
			"client_id", id, "declared", doc.TokenEndpointAuthMethod, "effective", authMethod)
	}

	// Filter — not reject — grant_types and response_types this AS does not
	// support. A CIMD document describes the client's capabilities across
	// every AS it talks to and cannot be tailored per server (VS Code
	// declares device_code alongside authorization_code, see #6290), so an
	// unsupported entry is ignored rather than failing the whole document.
	// The filters still reject when the intersection lacks the one flow this
	// server offers (authorization_code / code): such a client could never
	// complete a token exchange here, and a clear error at resolution beats
	// a client that resolves and then fails every token request.
	grantTypes, dcrErr := registration.FilterPublicGrantTypes(doc.GrantTypes)
	if dcrErr != nil {
		slog.WarnContext(ctx, "CIMD client rejected: invalid grant_types",
			"client_id", id, "error", dcrErr.ErrorDescription)
		return nil, fmt.Errorf("%w: CIMD document at %s: %s",
			fosite.ErrInvalidClient.WithHint(dcrErr.ErrorDescription), id, dcrErr.ErrorDescription)
	}
	if len(grantTypes) < len(doc.GrantTypes) {
		slog.Debug("CIMD: ignoring grant_types this server does not support",
			"client_id", id, "declared", doc.GrantTypes, "effective", grantTypes)
	}
	responseTypes, dcrErr := registration.FilterPublicResponseTypes(doc.ResponseTypes)
	if dcrErr != nil {
		slog.WarnContext(ctx, "CIMD client rejected: invalid response_types",
			"client_id", id, "error", dcrErr.ErrorDescription)
		return nil, fmt.Errorf("%w: CIMD document at %s: %s",
			fosite.ErrInvalidClient.WithHint(dcrErr.ErrorDescription), id, dcrErr.ErrorDescription)
	}
	if len(responseTypes) < len(doc.ResponseTypes) {
		slog.Debug("CIMD: ignoring response_types this server does not support",
			"client_id", id, "declared", doc.ResponseTypes, "effective", responseTypes)
	}

	resolvedScopes, err := d.resolveScopes(ctx, id, doc)
	if err != nil {
		return nil, err
	}

	client := registration.MarkDCRIssued(buildFositeClient(doc, resolvedScopes, grantTypes, responseTypes, authMethod))

	// Best-effort write-through: persist the resolved client in the underlying
	// storage so backends whose session rehydration resolves the client
	// through their own row lookup — Redis's unmarshalRequester, which never
	// consults this decorator — find CIMD clients at the token endpoint (see
	// issue #6187). The client carries the DCR-issued marker (above) so the
	// row gets the same anti-bloat TTL as DCR registrations: unauthenticated
	// /oauth/authorize traffic can mint these rows, so they must never be
	// permanent. Re-persisting on every fresh fetch keeps the snapshot
	// current with the document. A persistence failure only degrades
	// token-path rehydration, so it must not fail the resolution itself.
	if err := d.RegisterClient(ctx, client); err != nil {
		slog.WarnContext(ctx, "failed to persist resolved CIMD client",
			"client_id", id, "error", err)
	}

	d.cache.Add(id, &cimdCacheEntry{
		client:  client,
		expires: time.Now().Add(d.ttl),
	})

	return client, nil
}

// resolveScopes computes and validates the client scope list consistent
// with DCR. When d.scopesSupported is configured:
//   - Declared scopes are validated via registration.ValidateScopes (same
//     function as the DCR handler); an unsupported declared scope rejects.
//   - Omitted scope uses ValidateScopes(nil, scopesSupported), which grants
//     the intersection of DefaultScopes with ScopesSupported (matching DCR)
//     and reports the defaults it dropped; only an empty intersection is
//     rejected, in which case the document must declare scope explicitly.
//
// When d.scopesSupported is not configured: no AS-level validation; declared
// scopes are used directly, or nil to let buildFositeClient apply
// DefaultScopes. In both cases d.baselineClientScopes is unioned in after
// validation, matching the DCR handler's behaviour.
func (d *CIMDStorageDecorator) resolveScopes(
	ctx context.Context, id string, doc *cimd.ClientMetadataDocument,
) ([]string, error) {
	var resolvedScopes []string
	var droppedDefaults []string
	if len(d.scopesSupported) > 0 {
		if doc.Scope != "" {
			computed, _, dcrErr := registration.ValidateScopes(strings.Fields(doc.Scope), d.scopesSupported)
			if dcrErr != nil {
				slog.WarnContext(ctx, "CIMD client rejected: invalid scope",
					"client_id", id, "error", dcrErr.ErrorDescription)
				return nil, fmt.Errorf("%w: CIMD document at %s: %s",
					fosite.ErrInvalidClient.WithHint(dcrErr.ErrorDescription), id, dcrErr.ErrorDescription)
			}
			resolvedScopes = computed
		} else {
			// Omitted scope: match DCR — grant the intersection of DefaultScopes
			// with scopes_supported; reject only when the intersection is empty.
			computed, dropped, dcrErr := registration.ValidateScopes(nil, d.scopesSupported)
			if dcrErr != nil {
				slog.WarnContext(ctx, "CIMD client rejected: no usable default scopes",
					"client_id", id, "error", dcrErr.ErrorDescription)
				return nil, fmt.Errorf("%w: CIMD document at %s omits scope and "+
					"none of the default scopes are supported by this server — "+
					"the document must explicitly declare its required scopes",
					fosite.ErrInvalidClient.WithHint("scope field required"),
					id)
			}
			resolvedScopes = computed
			droppedDefaults = dropped
		}
	} else if doc.Scope != "" {
		resolvedScopes = strings.Fields(doc.Scope)
	}
	if len(d.baselineClientScopes) > 0 {
		resolvedScopes = registration.UnionScopes(resolvedScopes, d.baselineClientScopes)
	}
	// The document omitted scope and scopes_supported does not carry the full
	// default set: resolution proceeded with the intersection (see issue
	// #6186). Recorded after the baseline union so "scopes" is the set the
	// client actually receives; Debug because the drop is fully determined by
	// startup config — the one-time operator-facing signal lives in
	// Config.applyDefaults.
	if len(droppedDefaults) > 0 {
		slog.DebugContext(ctx, "CIMD document omits scope; granted the intersection of default scopes with scopes_supported",
			"client_id", id, "scopes", resolvedScopes, "dropped_defaults", droppedDefaults)
	}
	return resolvedScopes, nil
}

// defaultCIMDTokenEndpointAuthMethod is the token endpoint authentication
// method applied when the CIMD document omits token_endpoint_auth_method, and
// the only method this server ever selects when negotiating a fallback for a
// declared-but-unsupported value. Documents whose declared method is neither
// this value nor negotiable to it via negotiateTokenEndpointAuthMethod are
// rejected by fetch() before buildFositeClient is called.
const defaultCIMDTokenEndpointAuthMethod = "none"

// negotiateTokenEndpointAuthMethod resolves the effective token endpoint auth
// method for a CIMD document. The declared singular TokenEndpointAuthMethod is
// used when it is empty or already equal to defaultCIMDTokenEndpointAuthMethod.
// When it names anything else, the plural TokenEndpointAuthMethodsSupported
// (OpenID Connect RP Metadata Choices 1.0 — not part of the CIMD draft) is
// consulted: if it contains defaultCIMDTokenEndpointAuthMethod, the document
// is accepted with that as the negotiated method, since the client itself
// declared willingness to use it. Otherwise negotiation fails and ok is false.
//
// The supported set this function negotiates against is deliberately the
// defaultCIMDTokenEndpointAuthMethod constant alone, never the AS discovery
// document's own token_endpoint_auth_methods_supported: discovery legitimately
// advertises client_secret_basic/client_secret_post when confidential DCR or
// static delegate clients are enabled, and the CIMD draft (§4.1) forbids those
// symmetric methods in CIMD documents outright. Deriving the negotiated set
// from discovery would let a CIMD document negotiate its way into a forbidden
// method, so no such config plumbing is introduced.
func negotiateTokenEndpointAuthMethod(doc *cimd.ClientMetadataDocument) (string, bool) {
	declared := doc.TokenEndpointAuthMethod
	if declared == "" || declared == defaultCIMDTokenEndpointAuthMethod {
		return defaultCIMDTokenEndpointAuthMethod, true
	}
	if slices.Contains(doc.TokenEndpointAuthMethodsSupported, defaultCIMDTokenEndpointAuthMethod) {
		return defaultCIMDTokenEndpointAuthMethod, true
	}
	return "", false
}

// buildFositeClient converts a ClientMetadataDocument into a fosite.Client.
// RFC 8252 §7.3 loopback dynamic-port matching for a "http://localhost" redirect
// URI is provided generically by registration.RegisteredLoopbackRedirectURI
// (keyed on IsPublic() + GetRedirectURIs()), so no wrapper type is needed here.
// resolvedScopes is the already-validated scope list computed by fetch() via
// registration.ValidateScopes; when empty, DefaultScopes is used — this occurs when
// the decorator has no ScopesSupported restriction (unconstrained AS).
// grantTypes and responseTypes are the already-filtered lists computed by
// fetch() via registration.FilterPublicGrantTypes/FilterPublicResponseTypes —
// the document's declared values with unsupported entries dropped, never
// empty (the filters apply defaults and reject empty intersections). The
// stored client therefore carries only grant/response types this server can
// actually serve, not the document's full declaration.
// tokenEndpointAuthMethod is the already-negotiated value computed by fetch()
// via negotiateTokenEndpointAuthMethod; this function no longer applies its
// own empty-to-default fallback, so the resolver is the single authority over
// which method a CIMD-derived client ends up with.
func buildFositeClient(
	doc *cimd.ClientMetadataDocument, resolvedScopes, grantTypes, responseTypes []string,
	tokenEndpointAuthMethod string,
) fosite.Client {
	// Scopes were computed and validated by fetch() via registration.ValidateScopes,
	// consistent with the DCR handler. Fall back to DefaultScopes only when the
	// decorator has no ScopesSupported restriction (unconstrained AS).
	scopes := resolvedScopes
	if len(scopes) == 0 {
		scopes = slices.Clone(registration.DefaultScopes)
	}

	defaultClient := &fosite.DefaultClient{
		ID:            doc.ClientID,
		RedirectURIs:  doc.RedirectURIs,
		GrantTypes:    grantTypes,
		ResponseTypes: responseTypes,
		Scopes:        scopes,
		// CIMD clients don't pre-declare audience; leave empty so the AS
		// applies its own audience policy rather than rejecting all values.
		Audience: nil,
		Public:   true,
	}

	return &fosite.DefaultOpenIDConnectClient{
		DefaultClient:           defaultClient,
		TokenEndpointAuthMethod: tokenEndpointAuthMethod,
	}
}

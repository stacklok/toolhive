// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ory/fosite"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/oauthproto/cimd"
)

// ErrCIMDRateLimited is wrapped by the fosite.ErrNotFound error that GetClient
// returns when resolving a CIMD client_id would exceed the decorator's outbound
// fetch budget and no cached entry within MaxStale of its expiry can be served
// instead.
// On a request whose context carries WithPersistedClientFallback (the token
// endpoint), it is also not returned when a persisted client row that still
// matches the configuration exists; that row is served instead.
// Handlers map it to HTTP 429 instead of invalid_client.
var ErrCIMDRateLimited = errors.New("CIMD document fetch rate limit exceeded")

// CIMDStorageDecorator wraps storage.Storage and intercepts GetClient calls
// for HTTPS client_id values, fetching and caching the corresponding Client
// ID Metadata Document instead of requiring prior DCR registration.
//
// All other Storage methods delegate to the underlying storage unchanged,
// except RenewClientTTL (CIMD ids are skipped), and ConsumeAssertionJWT and
// Unwrap, which forward to the wrapped backend. GetClient is the main
// override. DCR clients (opaque IDs) continue to work exactly as before.
//
// Every route that resolves a CIMD client_id (authorize, token, device
// authorization) reaches the outbound fetch and write-through persistence
// through GetClient, so fetchLimiter bounds both for all of them. When the
// limiter refuses a fetch, or the fetch is attempted and fails transiently
// (cimd.ErrDocumentUnavailable), an expired cache entry is served for up to
// maxStale past its expiry so a known client is not locked out by traffic
// that drains the shared budget or by a document host that is briefly
// unreachable. A re-fetch that fails definitively evicts the entry, so
// neither path serves a document the host withdrew or this server rejected
// from the cache again; see fetchFailed for both rules. On the token endpoint
// (WithPersistedClientFallback), a refused lookup with no servable cache entry
// is answered from the persisted client row when one exists; that row is not
// evicted, so it can still answer a refusal after a definitive failure.
type CIMDStorageDecorator struct {
	Storage                                 // embed full interface — all methods delegate
	sf                   singleflight.Group // deduplicates concurrent fetches for the same URL
	cache                *lru.Cache[string, *cimdCacheEntry]
	ttl                  time.Duration
	scopesSupported      []string // AS-configured scopes; nil means accept any
	baselineClientScopes []string // unioned into every client's scope set, same as DCR
	allowedAudiences     []string // AS-configured audiences granted to every CIMD client
	// maxStale is how long past expiry a cached entry may be served when
	// fetchLimiter refuses a fetch or the fetch fails transiently; zero
	// disables stale serving.
	maxStale time.Duration
	// fetchLimiter bounds outbound document fetches (and therefore persisted
	// rows) triggered by unauthenticated callers. Per-process, so N replicas
	// allow N times the rate, like the handlers' registerLimiter. A refusal
	// returns ErrCIMDRateLimited unless an entry within maxStale is cached or,
	// for a WithPersistedClientFallback context, a matching persisted row exists.
	fetchLimiter *rate.Limiter
}

type cimdCacheEntry struct {
	client  fosite.Client
	expires time.Time
}

// CIMDDecoratorConfig holds the configuration for NewCIMDStorageDecorator.
// Using a struct prevents silent swaps of the three adjacent []string fields.
type CIMDDecoratorConfig struct {
	// Enabled returns base unchanged when false, avoiding an allocation.
	Enabled bool
	// CacheMaxSize is the maximum number of documents in the LRU cache (must be >= 1).
	CacheMaxSize int
	// FallbackTTL is the fixed TTL applied to every cache entry.
	FallbackTTL time.Duration
	// MaxStale is how long past its expiry a cached entry may still be served
	// when the fetch rate limiter refuses a re-fetch or the re-fetch fails
	// transiently (cimd.ErrDocumentUnavailable). Zero disables stale serving;
	// negative values are rejected.
	MaxStale time.Duration
	// ScopesSupported is the AS scope allowlist; see pkg/authserver/config.go
	// applyDefaults for production guarantees. Pass nil in tests only.
	ScopesSupported []string
	// BaselineClientScopes is unioned into every CIMD client's scope set,
	// matching DCR handler behaviour.
	BaselineClientScopes []string
	// AllowedAudiences is the server's own allowed-audience list, granted to
	// every CIMD client exactly as registration.New() grants it to DCR clients
	// (see #3796). CIMD documents don't declare audience, so without this a
	// CIMD client's fosite.Client.Audience stays empty and every refresh_token
	// grant is rejected by fosite's DefaultAudienceMatchingStrategy, which
	// matches the granted audience against the client's own audience list.
	AllowedAudiences []string
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

	if cfg.MaxStale < 0 {
		return nil, fmt.Errorf("CIMD storage decorator maxStale must be >= 0, got %s", cfg.MaxStale)
	}

	c, err := lru.New[string, *cimdCacheEntry](cfg.CacheMaxSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create CIMD LRU cache: %w", err)
	}

	return &CIMDStorageDecorator{
		Storage:              base,
		cache:                c,
		ttl:                  cfg.FallbackTTL,
		maxStale:             cfg.MaxStale,
		scopesSupported:      slices.Clone(cfg.ScopesSupported),
		baselineClientScopes: slices.Clone(cfg.BaselineClientScopes),
		allowedAudiences:     slices.Clone(cfg.AllowedAudiences),
		// Same rate as the handlers' registerLimiter: both gate the same kind
		// of unauthenticated persisted-state minting.
		fetchLimiter: rate.NewLimiter(rate.Limit(1), 5),
	}, nil
}

type persistedClientFallbackKey struct{}

// WithPersistedClientFallback marks ctx as belonging to the token endpoint. It
// must not be used anywhere else.
//
// fosite calls Storage.GetClient itself inside NewAccessRequest, so a context
// value is the only seam the handler has. The mark permits
// CIMDStorageDecorator.GetClient, when the fetch limiter refused and no stale
// cache entry is servable, to answer from the persisted client row instead of
// returning ErrCIMDRateLimited. It applies to any URL-shaped GetClient made
// with the returned context. The fallback is read-only and serves a snapshot
// that was validated when it was last fetched; it is not re-validated against
// the document.
func WithPersistedClientFallback(ctx context.Context) context.Context {
	return context.WithValue(ctx, persistedClientFallbackKey{}, true)
}

// PersistedClientFallbackAllowed reports whether ctx carries the mark set by
// WithPersistedClientFallback. The decorator reads it, and tests use it to
// check which endpoints set it.
func PersistedClientFallbackAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(persistedClientFallbackKey{}).(bool)
	return allowed
}

// GetClient intercepts HTTPS client_id values to resolve them via CIMD.
// Opaque DCR-issued IDs are delegated to the underlying storage unchanged.
func (d *CIMDStorageDecorator) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	if !oauthproto.IsClientIDMetadataDocumentURL(id) {
		return d.Storage.GetClient(ctx, id)
	}
	client, err := d.fetchOrCached(ctx, id)
	// Outside singleflight and with the caller's ctx on purpose: the mark is
	// per request and must not leak to other callers sharing a flight. The
	// row is deliberately neither cached nor renewed (a cached row would be
	// served to /oauth/authorize as a fresh hit), and every failure returns
	// the original rate-limit error, unlike handlers' callbackClient which
	// returns the base lookup's error.
	if errors.Is(err, ErrCIMDRateLimited) && PersistedClientFallbackAllowed(ctx) {
		row, rowErr := d.Storage.GetClient(ctx, id)
		if rowErr == nil && row != nil && d.persistedRowMatchesConfig(id, row) {
			slog.DebugContext(ctx, "CIMD document fetch rate limited; serving persisted client row", "client_id", id)
			return row, nil
		}
	}
	return client, err
}

// persistedRowMatchesConfig reports whether the persisted row for id is a
// snapshot this decorator wrote and still agrees with the current AS
// configuration, failing closed. Only the CIMD fetch writes rows at URL-shaped
// ids, so the row must carry that id, be a public client and carry the
// DCR-issued marker; anything else at such an id was not validated by us.
// Every grant type on the row must survive registration.FilterPublicGrantTypes
// unchanged, i.e. be one the fetch path can still produce (a row may hold a
// subset). That filter is static: the AS configuration cannot narrow it.
//
// A restart is the usual trigger for the persisted-row fallback and the
// operator may have changed AllowedAudiences or ScopesSupported before
// restarting; the row would carry the old values. The audience must equal
// allowedAudiences (order-insensitive, which is all buildFositeClient puts
// there) and, when scopesSupported is configured, every row scope must be in
// scopesSupported or baselineClientScopes. It never mutates the row.
func (d *CIMDStorageDecorator) persistedRowMatchesConfig(id string, client fosite.Client) bool {
	if client.GetID() != id || !client.IsPublic() || !registration.DCRIssued(client) {
		return false
	}
	rowGrants := client.GetGrantTypes()
	allowed, dcrErr := registration.FilterPublicGrantTypes(rowGrants)
	if dcrErr != nil || len(allowed) != len(rowGrants) {
		return false
	}
	rowAud := slices.Sorted(slices.Values(client.GetAudience()))
	wantAud := slices.Sorted(slices.Values(d.allowedAudiences))
	if !slices.Equal(rowAud, wantAud) {
		return false
	}
	if len(d.scopesSupported) == 0 {
		return true
	}
	for _, scope := range client.GetScopes() {
		if !slices.Contains(d.scopesSupported, scope) && !slices.Contains(d.baselineClientScopes, scope) {
			return false
		}
	}
	return true
}

// RenewClientTTL skips URL-shaped (CIMD) client ids and delegates every other
// id to the wrapped storage. Renewal is redundant for CIMD rows because every
// successful fetch upserts the row and resets its TTL. Not renewing keeps the
// row TTL equal to the time since the last successful fetch, which is the age
// bound for the persisted-row fallback on the token endpoint on both backends.
// Opaque DCR and static ids are renewed as before.
func (d *CIMDStorageDecorator) RenewClientTTL(ctx context.Context, client fosite.Client) error {
	if client != nil && oauthproto.IsClientIDMetadataDocumentURL(client.GetID()) {
		return nil
	}
	return d.Storage.RenewClientTTL(ctx, client)
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
	// FetchClientMetadataDocument enforces its own 5-second timeout. The detached
	// context still carries context values such as the persisted-fallback mark;
	// nothing inside fetch reads it, and fetch must not call this decorator's
	// GetClient.
	fetchCtx := context.WithoutCancel(ctx)
	result, err, _ := d.sf.Do(id, func() (interface{}, error) {
		// Re-check cache inside singleflight (another goroutine may have populated
		// it). The entry is reused below for the stale fallback.
		entry, ok := d.cache.Get(id)
		if ok && time.Now().Before(entry.expires) {
			return entry.client, nil
		}
		// Allow, not Wait: fetchCtx is detached from the caller, so a waiting
		// call could never be cancelled. Cache hits and callers sharing this
		// singleflight call cost nothing; a failed fetch costs a token.
		// The stale check sits after the limiter so that with budget left an
		// expired entry is still re-fetched and re-validated; a refusal falls
		// back to the expired entry within maxStale, and so does a fetch that
		// is attempted and fails transiently (fetchFailed, which also evicts
		// the entry on every other failure, so the pre-failure document cannot
		// come back from the cache through a later refusal or blip).
		if !d.fetchLimiter.Allow() {
			if d.servableStale(entry, ok) {
				// DEBUG: callers can trigger this at request rate.
				slog.DebugContext(ctx, "CIMD document fetch rate limited; serving stale cache entry",
					"client_id", id, "age_past_expiry", time.Since(entry.expires))
				return entry.client, nil
			}
			slog.DebugContext(ctx, "CIMD document fetch rate limited", "client_id", id)
			return nil, fosite.ErrNotFound.WithWrap(ErrCIMDRateLimited)
		}
		client, err := d.fetch(fetchCtx, id)
		if err != nil {
			return d.fetchFailed(ctx, id, err)
		}
		return client, nil
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

// servableStale reports whether entry, the cache entry found for an id (ok is
// false when there is none), may still answer a lookup whose re-fetch the
// limiter refused or that failed transiently: maxStale > 0 makes the zero
// value explicit (stale serving disabled), and the entry must be less than
// maxStale past its expiry. The window is anchored to the entry's original
// expiry; a stale answer never moves it.
func (d *CIMDStorageDecorator) servableStale(entry *cimdCacheEntry, ok bool) bool {
	return ok && d.maxStale > 0 && time.Now().Before(entry.expires.Add(d.maxStale))
}

func (d *CIMDStorageDecorator) fetch(ctx context.Context, id string) (fosite.Client, error) {
	doc, err := cimd.FetchClientMetadataDocument(ctx, id)
	if err != nil {
		// Logged by fetchFailed once the stale decision is made. Every other
		// rejection below is logged at Warn here: the errors returned from
		// fetch surface to the client as a generic invalid_client with the
		// hint dropped by fosite's production error rendering, so without a
		// server-side log line these failures are undiagnosable (see issue
		// #6186).
		return nil, fosite.ErrNotFound.WithHint("CIMD fetch failed").WithWrap(err)
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
		return nil, fosite.ErrInvalidClient.WithHintf("CIMD document at %s claims token_endpoint_auth_method %q "+
			"but this server only supports %q (token_endpoint_auth_methods_supported: %v)",
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
		return nil, fosite.ErrInvalidClient.WithHintf("CIMD document at %s: %s", id, dcrErr.ErrorDescription)
	}
	if len(grantTypes) < len(doc.GrantTypes) {
		slog.Debug("CIMD: ignoring grant_types this server does not support",
			"client_id", id, "declared", doc.GrantTypes, "effective", grantTypes)
	}
	responseTypes, dcrErr := registration.FilterPublicResponseTypes(doc.ResponseTypes)
	if dcrErr != nil {
		slog.WarnContext(ctx, "CIMD client rejected: invalid response_types",
			"client_id", id, "error", dcrErr.ErrorDescription)
		return nil, fosite.ErrInvalidClient.WithHintf("CIMD document at %s: %s", id, dcrErr.ErrorDescription)
	}
	if len(responseTypes) < len(doc.ResponseTypes) {
		slog.Debug("CIMD: ignoring response_types this server does not support",
			"client_id", id, "declared", doc.ResponseTypes, "effective", responseTypes)
	}

	resolvedScopes, err := d.resolveScopes(ctx, id, doc)
	if err != nil {
		return nil, err
	}

	client := registration.MarkDCRIssued(buildFositeClient(fositeClientParams{
		doc:                     doc,
		resolvedScopes:          resolvedScopes,
		grantTypes:              grantTypes,
		responseTypes:           responseTypes,
		tokenEndpointAuthMethod: authMethod,
		allowedAudiences:        d.allowedAudiences,
	}))

	// Best-effort write-through: persist the resolved client in the underlying
	// storage so backends whose session rehydration resolves the client
	// through their own row lookup — Redis's unmarshalRequester, which never
	// consults this decorator — find CIMD clients at the token endpoint (see
	// issue #6187). The client carries the DCR-issued marker (above) so the
	// row gets the same anti-bloat TTL as DCR registrations: unauthenticated
	// traffic can mint these rows (fetchLimiter bounds the rate), so they must
	// never be permanent. UpsertDCRIssuedClient (not RegisterClient) is used because
	// RegisterClient is strictly create-only and would fail every fetch after
	// the first for the same client_id -- this method re-persists on every
	// fresh fetch, keeping the stored snapshot current with the document,
	// while still refusing to clobber a configured/SPIFFE-reconciled client
	// at the same ID. A persistence failure only degrades token-path
	// rehydration, so it must not fail the resolution itself.
	// A backend at its CIMD row cap refuses new rows; unauthenticated
	// callers set that volume, so it stays at Debug.
	if err := d.UpsertDCRIssuedClient(ctx, client); errors.Is(err, ErrClientCapacity) {
		slog.DebugContext(ctx, "CIMD client not persisted: storage at capacity",
			"client_id", id, "error", err)
	} else if err != nil {
		slog.WarnContext(ctx, "failed to persist resolved CIMD client",
			"client_id", id, "error", err)
	}

	d.cache.Add(id, &cimdCacheEntry{
		client:  client,
		expires: time.Now().Add(d.ttl),
	})

	return client, nil
}

// fetchFailed answers a re-fetch of id that returned err. It runs inside the
// singleflight call, so every caller sharing the flight gets one decision and
// one log line, and no concurrent fetch for id can add a fresh entry between
// the decision below and the eviction.
//
// A transient failure (cimd.ErrDocumentUnavailable: the host delivered no
// usable response or answered 5xx, 408, or 429) is answered from the expired
// cache entry while servableStale allows it, the same window the limiter's
// refusal path uses. The entry is neither renewed nor re-persisted, so the
// next lookup re-fetches again and the stale answer never outlives maxStale.
//
// Every other outcome evicts the entry and returns err: a definitive fetch
// failure (a 3xx or any other 4xx whatever happened to its body, an
// unexpected content type, a malformed body, a document that fails
// validation), a document this server's policy rejects, and a transient
// failure with no servable entry. The cache must not hand a document the host
// withdrew or this server rejected back through a later limiter refusal or
// transient failure, and an entry past the window can never answer again; the
// next lookup needs a successful fetch. The persisted row is left in place
// (Storage has no client delete): on a later limiter refusal the token
// endpoint's fallback in GetClient can still answer from it for up to
// DefaultDCRClientTTL after the last successful fetch, and handlers'
// callbackClient reads it only to finish an authorization already in
// progress, within the bounds docs/arch/11-auth-server-storage.md gives.
// fosite renders the returned error as invalid_client exactly as before, so a
// client this server has never resolved still fails closed.
//
// fetch logs a rejected document's cause itself (fosite.ErrInvalidClient). A
// document that could not be fetched is logged here, once, after the
// decision, so the line carries the cause and the outcome. fetch returns it
// as fosite.ErrNotFound with the cause wrapped, and fosite renders that
// wrapper as the bare code "not_found" and drops the hint in its production
// error rendering, so the line names the wrapped cause (the status or the
// transport error); without it the failure is undiagnosable (see issue
// #6186).
func (d *CIMDStorageDecorator) fetchFailed(ctx context.Context, id string, err error) (fosite.Client, error) {
	cause := cmp.Or(errors.Unwrap(err), err)
	entry, ok := d.cache.Get(id)
	if errors.Is(err, cimd.ErrDocumentUnavailable) && d.servableStale(entry, ok) {
		slog.WarnContext(ctx, "CIMD document fetch failed",
			"client_id", id, "error", cause,
			"fallback", "expired_cache", "age_past_expiry", time.Since(entry.expires))
		return entry.client, nil
	}
	d.cache.Remove(id)
	if !errors.Is(err, fosite.ErrInvalidClient) {
		slog.WarnContext(ctx, "CIMD document fetch failed",
			"client_id", id, "error", cause, "fallback", "none")
	}
	return nil, err
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
				return nil, fosite.ErrInvalidClient.WithHintf("CIMD document at %s: %s", id, dcrErr.ErrorDescription)
			}
			resolvedScopes = computed
		} else {
			// Omitted scope: match DCR — grant the intersection of DefaultScopes
			// with scopes_supported; reject only when the intersection is empty.
			computed, dropped, dcrErr := registration.ValidateScopes(nil, d.scopesSupported)
			if dcrErr != nil {
				slog.WarnContext(ctx, "CIMD client rejected: no usable default scopes",
					"client_id", id, "error", dcrErr.ErrorDescription)
				return nil, fosite.ErrInvalidClient.WithHint("scope field required")
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
// advertises client_secret_basic/client_secret_post when confidential DCR is
// enabled or a configured delegate client uses a secret, and the CIMD draft (§4.1) forbids those
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

// fositeClientParams carries the inputs buildFositeClient turns into a
// fosite.Client. Every field is computed by fetch() beforehand, so the
// builder applies no validation, filtering, or negotiation of its own. A
// struct keeps the four []string values from being swapped silently at the
// call site, for the same reason CIMDDecoratorConfig is one.
type fositeClientParams struct {
	doc *cimd.ClientMetadataDocument
	// resolvedScopes is the already-validated scope list computed by fetch()
	// via registration.ValidateScopes; when empty, DefaultScopes is used —
	// this occurs when the decorator has no ScopesSupported restriction
	// (unconstrained AS).
	resolvedScopes []string
	// grantTypes and responseTypes are the already-filtered lists computed by
	// fetch() via registration.FilterPublicGrantTypes/FilterPublicResponseTypes —
	// the document's declared values with unsupported entries dropped, never
	// empty (the filters apply defaults and reject empty intersections). The
	// stored client therefore carries only grant/response types this server
	// can actually serve, not the document's full declaration.
	grantTypes    []string
	responseTypes []string
	// tokenEndpointAuthMethod is the already-negotiated value computed by
	// fetch() via negotiateTokenEndpointAuthMethod; the builder applies no
	// empty-to-default fallback of its own, so the resolver is the single
	// authority over which method a CIMD-derived client ends up with.
	tokenEndpointAuthMethod string
	// allowedAudiences is the decorator's configured AllowedAudiences, granted
	// to the client verbatim (mirroring registration.New() for DCR clients,
	// #3796): CIMD documents never declare audience, so the AS applies its own
	// audience policy by granting the audiences it would validate a "resource"
	// parameter against, rather than leaving the client's audience list empty.
	// An empty list reproduces the prior nil behavior.
	allowedAudiences []string
}

// buildFositeClient converts a ClientMetadataDocument into a fosite.Client.
// RFC 8252 §7.3 loopback dynamic-port matching for a "http://localhost" redirect
// URI is provided generically by registration.RegisteredLoopbackRedirectURI
// (keyed on IsPublic() + GetRedirectURIs()), so no wrapper type is needed here.
func buildFositeClient(p fositeClientParams) fosite.Client {
	// Scopes were computed and validated by fetch() via registration.ValidateScopes,
	// consistent with the DCR handler. Fall back to DefaultScopes only when the
	// decorator has no ScopesSupported restriction (unconstrained AS).
	scopes := p.resolvedScopes
	if len(scopes) == 0 {
		scopes = slices.Clone(registration.DefaultScopes)
	}

	defaultClient := &fosite.DefaultClient{
		ID:            p.doc.ClientID,
		RedirectURIs:  p.doc.RedirectURIs,
		GrantTypes:    p.grantTypes,
		ResponseTypes: p.responseTypes,
		Scopes:        scopes,
		// CIMD clients don't pre-declare audience; the AS applies its own
		// audience policy by granting the audiences it would validate a
		// "resource" parameter against (see the allowedAudiences field), rather
		// than rejecting all values with an empty list.
		Audience: slices.Clone(p.allowedAudiences),
		Public:   true,
	}

	return &fosite.DefaultOpenIDConnectClient{
		DefaultClient:           defaultClient,
		TokenEndpointAuthMethod: p.tokenEndpointAuthMethod,
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"fmt"
	"net/url"
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
		return nil, fmt.Errorf("%w: %w", fosite.ErrNotFound.WithHint("CIMD fetch failed"), err)
	}

	// Reject documents that declare an auth method this AS does not support.
	// ErrInvalidClient: the document was fetched successfully but its declared
	// metadata violates AS policy (distinct from ErrNotFound which means the
	// document could not be fetched at all).
	if m := doc.TokenEndpointAuthMethod; m != "" && m != defaultCIMDTokenEndpointAuthMethod {
		return nil, fmt.Errorf("%w: CIMD document at %s claims token_endpoint_auth_method %q "+
			"but this server only supports %q",
			fosite.ErrInvalidClient.WithHint("unsupported token_endpoint_auth_method"),
			id, m, defaultCIMDTokenEndpointAuthMethod)
	}

	// Reject documents that declare grant_types or response_types the embedded AS
	// does not support for public clients. Uses the same validators as DCR so the
	// error messages and allowed sets are identical on both registration paths.
	if _, dcrErr := registration.ValidatePublicGrantTypes(doc.GrantTypes); dcrErr != nil {
		return nil, fmt.Errorf("%w: CIMD document at %s: %s",
			fosite.ErrInvalidClient.WithHint(dcrErr.ErrorDescription), id, dcrErr.ErrorDescription)
	}
	if _, dcrErr := registration.ValidatePublicResponseTypes(doc.ResponseTypes); dcrErr != nil {
		return nil, fmt.Errorf("%w: CIMD document at %s: %s",
			fosite.ErrInvalidClient.WithHint(dcrErr.ErrorDescription), id, dcrErr.ErrorDescription)
	}

	// Compute and validate the client scope list consistent with DCR.
	// When ScopesSupported is configured:
	//   - Declared scopes are validated via registration.ValidateScopes (same
	//     function as the DCR handler).
	//   - Omitted scope uses ValidateScopes(nil, scopesSupported) which returns
	//     DefaultScopes when DefaultScopes ⊆ ScopesSupported, matching DCR.
	//     If DefaultScopes ⊄ ScopesSupported the document must declare scope
	//     explicitly to avoid ambiguous privilege grant.
	// When ScopesSupported is not configured: no AS-level validation; declared
	// scopes are used directly, or nil to let buildFositeClient apply DefaultScopes.
	// In both cases BaselineClientScopes is unioned in after validation,
	// matching the DCR handler's behaviour.
	var resolvedScopes []string
	if len(d.scopesSupported) > 0 {
		if doc.Scope != "" {
			computed, dcrErr := registration.ValidateScopes(strings.Fields(doc.Scope), d.scopesSupported)
			if dcrErr != nil {
				return nil, fmt.Errorf("%w: CIMD document at %s: %s",
					fosite.ErrInvalidClient.WithHint(dcrErr.ErrorDescription), id, dcrErr.ErrorDescription)
			}
			resolvedScopes = computed
		} else {
			// Omitted scope: match DCR — give DefaultScopes when they fit, else require explicit scope.
			computed, dcrErr := registration.ValidateScopes(nil, d.scopesSupported)
			if dcrErr != nil {
				return nil, fmt.Errorf("%w: CIMD document at %s omits scope but "+
					"DefaultScopes are not a subset of this server's scopes_supported — "+
					"the document must explicitly declare its required scopes",
					fosite.ErrInvalidClient.WithHint("scope field required"),
					id)
			}
			resolvedScopes = computed
		}
	} else if doc.Scope != "" {
		resolvedScopes = strings.Fields(doc.Scope)
	}
	if len(d.baselineClientScopes) > 0 {
		resolvedScopes = registration.UnionScopes(resolvedScopes, d.baselineClientScopes)
	}

	client := buildFositeClient(doc, resolvedScopes)

	d.cache.Add(id, &cimdCacheEntry{
		client:  client,
		expires: time.Now().Add(d.ttl),
	})

	return client, nil
}

// defaultCIMDGrantTypes are the OAuth 2.0 grant types applied when the CIMD
// document omits grant_types. CIMD clients are typically public native apps
// that use the authorization code flow with refresh token rotation.
var defaultCIMDGrantTypes = []string{"authorization_code", "refresh_token"}

// defaultCIMDResponseTypes are the OAuth 2.0 response types applied when the
// CIMD document omits response_types.
var defaultCIMDResponseTypes = []string{"code"}

// defaultCIMDTokenEndpointAuthMethod is the token endpoint authentication
// method applied when the CIMD document omits token_endpoint_auth_method.
// Documents that declare any other value are rejected by fetch() before
// buildFositeClient is called.
const defaultCIMDTokenEndpointAuthMethod = "none"

// buildFositeClient converts a ClientMetadataDocument into a fosite.Client.
// Redirect URIs containing http://localhost are wrapped in a LoopbackClient
// so that RFC 8252 §7.3 dynamic port matching applies.
// resolvedScopes is the already-validated scope list computed by fetch() via
// registration.ValidateScopes; when empty, DefaultScopes is used — this occurs when
// the decorator has no ScopesSupported restriction (unconstrained AS).
func buildFositeClient(doc *cimd.ClientMetadataDocument, resolvedScopes []string) fosite.Client {
	grantTypes := doc.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = defaultCIMDGrantTypes
	}

	responseTypes := doc.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = defaultCIMDResponseTypes
	}

	tokenEndpointAuthMethod := doc.TokenEndpointAuthMethod
	if tokenEndpointAuthMethod == "" {
		tokenEndpointAuthMethod = defaultCIMDTokenEndpointAuthMethod
	}

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

	openIDClient := &fosite.DefaultOpenIDConnectClient{
		DefaultClient:           defaultClient,
		TokenEndpointAuthMethod: tokenEndpointAuthMethod,
	}

	// Wrap in LoopbackClient when any redirect URI targets localhost so that
	// RFC 8252 §7.3 dynamic port matching works for native app clients.
	// Pass openIDClient directly so TokenEndpointAuthMethod is preserved —
	// LoopbackClient now embeds *fosite.DefaultOpenIDConnectClient.
	if hasLoopbackRedirectURI(doc.RedirectURIs) {
		return registration.NewLoopbackClient(openIDClient)
	}

	return openIDClient
}

// hasLoopbackRedirectURI returns true when any of the redirect URIs in the
// list targets a loopback address over HTTP. The host is parsed from each URI
// to prevent bypass via hosts like "http://localhost.evil.com/".
func hasLoopbackRedirectURI(uris []string) bool {
	for _, uri := range uris {
		parsed, err := url.Parse(uri)
		if err != nil {
			continue
		}
		if parsed.Scheme == "http" && oauthproto.IsLoopbackHost(parsed.Hostname()) {
			return true
		}
	}
	return false
}

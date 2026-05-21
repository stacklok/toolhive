// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"fmt"
	"net/url"
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
	Storage                    // embed full interface — all methods delegate
	sf      singleflight.Group // deduplicates concurrent fetches for the same URL
	cache   *lru.Cache[string, *cimdCacheEntry]
	ttl     time.Duration
	enabled bool
}

type cimdCacheEntry struct {
	client  fosite.Client
	expires time.Time
}

// NewCIMDStorageDecorator wraps base with CIMD client lookup. When enabled=false
// it returns base unchanged (no allocation). cacheMaxSize must be >= 1;
// fallbackTTL is the fixed TTL applied to every cache entry (Cache-Control
// header parsing is not yet implemented; all entries use this value).
func NewCIMDStorageDecorator(
	base Storage,
	enabled bool,
	cacheMaxSize int,
	fallbackTTL time.Duration,
) (Storage, error) {
	if !enabled {
		return base, nil
	}

	if cacheMaxSize < 1 {
		return nil, fmt.Errorf("CIMD storage decorator cacheMaxSize must be >= 1, got %d", cacheMaxSize)
	}

	c, err := lru.New[string, *cimdCacheEntry](cacheMaxSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create CIMD LRU cache: %w", err)
	}

	return &CIMDStorageDecorator{
		Storage: base,
		cache:   c,
		ttl:     fallbackTTL,
		enabled: true,
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
	return result.(fosite.Client), nil //nolint:forcetypeassert // fetch always returns fosite.Client
}

func (d *CIMDStorageDecorator) fetch(ctx context.Context, id string) (fosite.Client, error) {
	doc, err := cimd.FetchClientMetadataDocument(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", fosite.ErrNotFound.WithHint("CIMD fetch failed"), err)
	}

	client := buildFositeClient(doc)

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
// CIMD clients are always public — no pre-shared secret is established.
const defaultCIMDTokenEndpointAuthMethod = "none"

// buildFositeClient converts a ClientMetadataDocument into a fosite.Client.
// Redirect URIs containing http://localhost are wrapped in a LoopbackClient
// so that RFC 8252 §7.3 dynamic port matching applies.
func buildFositeClient(doc *cimd.ClientMetadataDocument) fosite.Client {
	grantTypes := doc.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = defaultCIMDGrantTypes
	}

	responseTypes := doc.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = defaultCIMDResponseTypes
	}

	// The embedded AS advertises only "none" in token_endpoint_auth_methods_supported.
	// Override any other value from the document to avoid an inconsistent client
	// configuration — CIMD clients are always public and have no pre-shared secret.
	tokenEndpointAuthMethod := defaultCIMDTokenEndpointAuthMethod

	var scopes []string
	if doc.Scope != "" {
		scopes = strings.Fields(doc.Scope)
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
	// Use openIDClient.DefaultClient so TokenEndpointAuthMethod is preserved
	// inside the wrapper — LoopbackClient embeds DefaultClient, not the OIDC
	// client, so passing defaultClient directly would drop the auth method.
	if hasLoopbackRedirectURI(doc.RedirectURIs) {
		return registration.NewLoopbackClient(openIDClient.DefaultClient)
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

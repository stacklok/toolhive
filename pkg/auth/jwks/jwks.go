// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package jwks provides a shared JWKS fetch-and-cache primitive used by every
// component that resolves verification keys over HTTP (pkg/auth's
// TokenValidator and the auth server's token-exchange validator).
//
// Fetcher consolidates machinery that previously existed as two diverging
// implementations: URL validation (ValidateJWKSURL), per-client response-body
// capping (limitedBodyTransport), lazy registration with refresh-on-retry,
// stale-on-error caching, rate-limited refresh on unknown key IDs, and a
// fetch-failure backoff gate before the first successful fetch.
//
// Stale-on-error requires no extra caching layer here: httprc only stores a
// resource's value after a successful transform, jwk.Cache.Lookup returns the
// last stored set, and a failed Refresh leaves that set in place — so a
// transient outage at an endpoint that has already been reached once never
// surfaces as a lookup failure.
package jwks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"

	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// DefaultBodyLimit caps every JWKS response body at 1 MiB. This prevents
	// resource exhaustion from unexpectedly large responses: the JWKS fetch is
	// handed to jwx's jwk.Cache, which has no equivalent cap of its own
	// (httprc.MaxBufferSize is ~1000 MiB, and its transformer does an
	// unbounded io.ReadAll under that ceiling before parsing).
	DefaultBodyLimit = 1 << 20

	// DefaultMaxKeys caps the number of keys accepted from a JWKS to prevent
	// CPU amplification from a hostile endpoint serving many keys.
	DefaultMaxKeys = 100

	// DefaultFetchFailureBackoff bounds how often EnsureRegistered retries a
	// JWKS fetch that has never once succeeded. Key resolution runs before a
	// token's signature is checked, so without this an authenticated client
	// could force a real outbound fetch to a broken endpoint on every single
	// request.
	DefaultFetchFailureBackoff = 30 * time.Second

	// DefaultMinKidRefreshInterval bounds how often Lookup forces the cache to
	// fetch its JWKS ahead of jwx's own background refresh schedule when a
	// token names a kid the cached set doesn't have. Key resolution runs
	// before the token's signature is trusted, so without this floor a client
	// presenting a syntactically valid token that merely names a made-up kid
	// could force a fresh fetch on every attempt.
	DefaultMinKidRefreshInterval = 30 * time.Second

	// DefaultRegistrationTimeout bounds the initial registration's ready-wait:
	// Register blocks until the resource's first fetch completes, so without a
	// budget a slow or broken endpoint would block callers for the full
	// context duration.
	DefaultRegistrationTimeout = 10 * time.Second
)

// Fetcher fetches, caches, and refreshes one issuer's (or validator's) JWKS.
//
// Each Fetcher MUST have its own jwk.Cache — never share a cache across
// Fetcher instances. A cache per Fetcher, rather than one shared across every
// configured issuer, is what makes two issuers resolving to the same jwks_url
// (e.g. two Microsoft Entra v1 tenants, which share one tenant-independent
// JWKS endpoint) a non-event: httprc keys a cached resource by URL alone and
// only honors jwk.WithHTTPClient on a URL's first Register call, so a shared
// cache would have the second such issuer silently inherit the first one's
// *http.Client — defeating InsecureAllowHTTP/AllowPrivateIPs's per-issuer
// guarantee for it. Splitting the cache per Fetcher makes that collision
// unrepresentable instead of guarding against it.
//
// A Fetcher is safe for concurrent use. All JWKS objects retrieved through it
// should be treated read-only, as they are shared among all consumers and the
// underlying jwk.Cache. No key material is ever logged — errors report key
// counts and key IDs only.
type Fetcher struct {
	// Configuration, immutable after construction.
	httpClient              *http.Client // dedicated to this Fetcher; see buildHTTPClient
	insecureAllowHTTP       bool
	allowPrivateIPs         bool
	caBundlePath            string
	caBundleUsesSystemRoots bool
	authTokenFile           string
	timeout                 time.Duration // 0 = networking.HttpClientBuilder default
	disableKeepAlives       bool
	sameHostRedirects       bool
	workers                 int // 0 = httprc default worker pool
	bodyLimit               int64
	maxKeys                 int
	refreshInterval         time.Duration // 0 = derive from response headers
	fetchFailureBackoff     time.Duration
	minKidRefreshInterval   time.Duration
	registrationTimeout     time.Duration

	// cache is this Fetcher's own jwk.Cache — see the type doc comment for
	// why it must never be shared.
	cache *jwk.Cache

	mu sync.Mutex
	// fetched is true once at least one JWKS fetch has succeeded since
	// process start, for fetchedURL. Until then, EnsureRegistered forces a
	// synchronous fetch on every call, since there is no cached value yet to
	// fall back on and jwx's own background schedule offers no way to wait
	// for its result.
	fetched bool
	// fetchedURL is the URL EnsureRegistered last succeeded for. A call for a
	// different URL (e.g. after OIDC discovery resolves a new jwks_uri) is
	// treated as a first fetch for that URL: it must actually register the
	// new resource, not be short-circuited by the previous URL's success.
	fetchedURL string
	// lastKidRefresh is the last time Lookup forced a fetch for an unknown
	// kid; see minKidRefreshInterval.
	lastKidRefresh time.Time
	// fetchFailedAt and fetchErr gate retries once registered but never
	// fetched: key resolution runs before signature verification, so without
	// this an authenticated client can otherwise drive one real outbound fetch
	// per request just by naming an issuer whose endpoint is down. fetchErr is
	// served directly while the gate is closed, so the caller still gets a
	// specific error instead of a generic "try later". Not consulted once
	// fetched is true: a healthy issuer, or one serving stale-but-valid keys
	// through a later background-refresh failure, must never be gated.
	fetchFailedAt time.Time
	fetchErr      error
}

// NewFetcher creates a Fetcher with its own HTTP client (built from the given
// options) and its own jwk.Cache running its own background worker pool for
// the life of the process.
//
// ctx is used only to start the cache's background worker pool;
// context.Background() semantics apply — there is no per-call context to root
// it in, and the pool is meant to outlive any single call.
func NewFetcher(ctx context.Context, opts ...Option) (*Fetcher, error) {
	f := &Fetcher{
		bodyLimit:             DefaultBodyLimit,
		maxKeys:               DefaultMaxKeys,
		fetchFailureBackoff:   DefaultFetchFailureBackoff,
		minKidRefreshInterval: DefaultMinKidRefreshInterval,
		registrationTimeout:   DefaultRegistrationTimeout,
	}
	for _, opt := range opts {
		opt(f)
	}

	if f.httpClient == nil {
		client, err := f.buildHTTPClient()
		if err != nil {
			return nil, err
		}
		f.httpClient = client
	}

	// One jwk.Cache per Fetcher (see the type doc comment for why), each
	// running its own background worker pool. WithWorkers caps that pool —
	// jwx's cache via httprc defaults to five workers, roughly three
	// goroutines per resource including its controller loop and wait-group
	// waiter.
	var httprcOpts []httprc.NewClientOption
	if f.workers > 0 {
		httprcOpts = append(httprcOpts, httprc.WithWorkers(f.workers))
	}
	cache, err := jwk.NewCache(ctx, httprc.NewClient(httprcOpts...))
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS cache: %w", err)
	}
	f.cache = cache

	return f, nil
}

// applyCABundle configures the builder's CA trust mode: pinned-only when
// WithCABundle was used, additive to the system roots when
// WithSystemRootsPlusCABundle was used.
func applyCABundle(builder *networking.HttpClientBuilder, f *Fetcher) {
	if f.caBundlePath == "" {
		return
	}
	if f.caBundleUsesSystemRoots {
		builder.WithSystemRootsPlusCABundle(f.caBundlePath)
	} else {
		builder.WithCABundle(f.caBundlePath)
	}
}

// buildHTTPClient constructs the Fetcher's dedicated HTTP client from its
// configuration. Deliberately networking.NewHttpClientBuilder(), not
// NewHostScopedClientBuilder: that helper ORs INSECURE_DISABLE_URL_VALIDATION
// and an auto-localhost exemption into BOTH the HTTP-scheme and private-IP
// gates, so an unrelated env var — or an issuer that merely happens to be on
// localhost — would silently widen AllowPrivateIPs regardless of what the
// operator set, defeating the point of splitting the two flags per issuer.
func (f *Fetcher) buildHTTPClient() (*http.Client, error) {
	builder := networking.NewHttpClientBuilder().
		WithInsecureAllowHTTP(f.insecureAllowHTTP).
		WithPrivateIPs(f.allowPrivateIPs)
	if f.timeout > 0 {
		builder = builder.WithTimeout(f.timeout)
	}
	if f.disableKeepAlives {
		// Keep-alive connections are disabled: this client dials a jwks_url —
		// a host taken from an untrusted discovery document — only on a fixed
		// refresh schedule plus occasional on-demand refreshes; no hot path
		// here to trade the per-dial SSRF check away for.
		builder = builder.WithDisableKeepAlives(true)
	}
	if f.authTokenFile != "" {
		builder = builder.WithTokenFromFile(f.authTokenFile)
	}
	applyCABundle(builder, f)
	httpClient, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP client: %w", err)
	}
	if f.sameHostRedirects {
		// Guard against a discovery/JWKS redirect hop landing on a different,
		// unvetted host — the same policy the transparent proxy data path
		// applies to a response derived from an untrusted remote server (see
		// SameHostRedirectPolicy's doc comment).
		httpClient.CheckRedirect = networking.SameHostRedirectPolicy()
	}
	if f.bodyLimit > 0 {
		// Cap every response body this client reads — the JWKS fetch is
		// handed to jwx's jwk.Cache, which has no equivalent cap of its own.
		// Wrapped OUTSIDE httpClient.Transport (which Build() always sets —
		// see networking's builder) so the private-IP dial guard and
		// ValidatingTransport's scheme check still run first, on the inner,
		// unwrapped transport.
		httpClient.Transport = &limitedBodyTransport{
			base: httpClient.Transport,
			max:  f.bodyLimit,
		}
	}
	return httpClient, nil
}

// HTTPClient returns the Fetcher's dedicated HTTP client: the client every
// JWKS fetch goes through, carrying this Fetcher's own transport policy
// (scheme and private-IP guards, CA trust, body cap). Callers that need an
// auxiliary request to the same issuer under the same policy — most notably
// OIDC discovery to resolve the jwks_url — must use this client, so their
// requests are guarded by exactly the policy the JWKS fetch is guarded by.
func (f *Fetcher) HTTPClient() *http.Client {
	return f.httpClient
}

// EnsureRegistered registers jwksURL with the Fetcher's cache, fetching it for
// the first time if needed. It is called lazily by Lookup and KeySet to avoid
// blocking construction.
//
// The JWKS fetch is retried until one succeeds (fetched stays false
// otherwise), gated by fetchFailureBackoff: key resolution runs before the
// token's signature is checked, so without this gate an authenticated client
// could force a real outbound attempt on every request to an endpoint that is
// down. The gate only applies before the first successful fetch — once
// fetched is true, a healthy issuer or one serving stale-but-valid keys
// through a later refresh failure is unaffected. While closed, the last error
// is replayed directly rather than retried.
func (f *Fetcher) EnsureRegistered(ctx context.Context, jwksURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fetched && f.fetchedURL == jwksURL {
		return nil
	}
	if time.Since(f.fetchFailedAt) < f.fetchFailureBackoff {
		return f.fetchErr
	}

	if err := f.registerOrRefresh(ctx, jwksURL); err != nil {
		f.fetchErr = err
		f.fetchFailedAt = time.Now()
		return err
	}
	f.fetched = true
	f.fetchedURL = jwksURL
	f.fetchErr = nil
	return nil
}

// registerOrRefresh performs the actual registration/fetch attempt for
// jwksURL; EnsureRegistered holds f.mu across this call, single-flighting it
// so concurrent callers don't pile up N redundant fetches.
//
// Whether jwksURL is already registered is asked of f.cache.IsRegistered
// directly, never remembered in a field: Register's own registration step is a
// channel send to the cache's backend goroutine and can fail after enqueue but
// before receipt (context deadline), in which case nothing was actually
// registered. A locally remembered "we called Register" flag can't distinguish
// that from "registered, only the fetch failed", and would wrongly keep
// retrying via Refresh — which errors on a URL the cache never heard of —
// forever after. Asking the cache directly is authoritative either way.
//
// IsRegistered makes no network call but isn't free: it's a round-trip over
// that same channel, so it blocks if the backend is busy and returns false
// (not an error) on context expiry. Its own timeout below matters for that
// reason — a false from an expired context just routes to Register, whose
// "already registered" error is transient and absorbed below.
func (f *Fetcher) registerOrRefresh(ctx context.Context, jwksURL string) error {
	// Detach from the caller's request context throughout this function:
	// net/http cancels ctx when the client disconnects, and this runs before
	// the token's signature is even checked, so an aborted connection must not
	// cut off work other in-flight validations are waiting on (mu, held by the
	// caller), nor let repeating the abort drive unbounded outbound requests
	// to the JWKS endpoint.
	detached := context.WithoutCancel(ctx)

	// Validate the JWKS URL in the single choke point every registration
	// passes through — whether it was hand-configured or just discovered. A
	// configured URL may never pass through any discovery step, so checking
	// only there would leave hand-configured URLs unvalidated.
	if err := ValidateJWKSURL(jwksURL, f.insecureAllowHTTP, f.allowPrivateIPs); err != nil {
		return fmt.Errorf("jwks_url %q is invalid: %w", jwksURL, err)
	}

	registeredCtx, cancel := context.WithTimeout(detached, f.registrationTimeout)
	registered := f.cache.IsRegistered(registeredCtx, jwksURL)
	cancel()

	if registered {
		// Already registered with this Fetcher's own cache, on a prior call
		// whose own fetch never completed successfully — the only way this
		// can be true, now that each Fetcher has its own cache. Register
		// would error on an already-tracked URL, so retry via Refresh
		// instead.
		fetchCtx, cancel := context.WithTimeout(detached, f.registrationTimeout)
		defer cancel()
		if _, err := f.cache.Refresh(fetchCtx, jwksURL); err != nil {
			return fmt.Errorf("failed to fetch JWKS: %w", err)
		}
		return nil
	}

	// A newly created httprc.Resource is always scheduled to fetch
	// immediately, so Register's own default WithWaitReady(true) blocks on
	// that single automatic fetch. An explicit Refresh call right here would
	// race it and issue a genuine second outbound request — that's why the
	// registered branch above, not this one, is where Refresh is used.
	//
	// The CA-aware client must be passed per-resource: jwx >= 3.1.0 injects
	// its own default client at the resource level when none is given here,
	// which takes precedence over any client-level configuration and silently
	// drops custom CA support.
	fetchCtx, cancel := context.WithTimeout(detached, f.registrationTimeout)
	defer cancel()
	registerOpts := []jwk.RegisterOption{jwk.WithHTTPClient(f.httpClient)}
	if f.refreshInterval > 0 {
		registerOpts = append(registerOpts, jwk.WithConstantInterval(f.refreshInterval))
	}
	if err := f.cache.Register(fetchCtx, jwksURL, registerOpts...); err != nil {
		switch {
		case errors.Is(err, httprc.ErrResourceAlreadyExists()):
			// Absorbed as non-fatal: the URL is already registered with this
			// cache (e.g. a previous attempt registered it before this state
			// was tracked), which is exactly the success state EnsureRegistered
			// exists to reach.
		default:
			// Note this includes httprc.ErrNotReady: Register's ready-wait
			// only ever ends in nil (fetch succeeded) or the context error
			// that interrupted it (see httprc's controller.Add), so a
			// not-ready here always means the first fetch did not complete
			// within the registration budget — i.e. a failed fetch. Treating
			// it as success would strand a never-successfully-fetched
			// resource with no retry path other than jwx's own background
			// schedule, so it is gated and replayed like any other fetch
			// failure instead.
			return fmt.Errorf("failed to register JWKS: %w", err)
		}
	}
	return nil
}

// Lookup returns the key with the given key ID from the cached JWKS, fetching
// it first if this is the first use (see EnsureRegistered). When the kid is
// absent from the cached set — the situation a legitimate key rotation
// produces — Lookup forces an immediate re-fetch ahead of jwx's own background
// refresh schedule and retries once.
//
// The forced refresh is gated by minKidRefreshInterval and single-flighted via
// f.mu: a token naming a kid absent from the cached set runs before signature
// verification, so without the gate a client presenting made-up kids could
// force a fresh fetch on every attempt.
func (f *Fetcher) Lookup(ctx context.Context, jwksURL, kid string) (jwk.Key, error) {
	if err := f.EnsureRegistered(ctx, jwksURL); err != nil {
		return nil, err
	}

	set, err := f.cache.Lookup(ctx, jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup JWKS: %w", err)
	}
	if err := f.checkKeyCount(set); err != nil {
		return nil, err
	}

	key, found := set.LookupKeyID(kid)
	if found {
		return key, nil
	}

	// The kid isn't among the keys we have cached — possibly a legitimate
	// rotation the cache hasn't caught up with yet (jwx's own background
	// refresh floor is 15 minutes by default). Force an immediate re-fetch and
	// retry once before giving up.
	f.RefreshOnUnknownKid(ctx, jwksURL)

	set, err = f.cache.Lookup(ctx, jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup JWKS: %w", err)
	}
	if err := f.checkKeyCount(set); err != nil {
		return nil, err
	}

	key, found = set.LookupKeyID(kid)
	if !found {
		return nil, fmt.Errorf("key ID %s not found in JWKS", kid)
	}
	return key, nil
}

// KeySet returns the whole cached JWKS for jwksURL, fetching it first if this
// is the first use (see EnsureRegistered). Callers that must inspect every key
// (e.g. to bridge the set into another JWT library) use this instead of
// Lookup.
func (f *Fetcher) KeySet(ctx context.Context, jwksURL string) (jwk.Set, error) {
	if err := f.EnsureRegistered(ctx, jwksURL); err != nil {
		return nil, err
	}

	set, err := f.cache.Lookup(ctx, jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup JWKS: %w", err)
	}
	if err := f.checkKeyCount(set); err != nil {
		return nil, err
	}
	return set, nil
}

// RefreshOnUnknownKid forces the cache to re-fetch its JWKS immediately, ahead
// of jwx's own background refresh schedule, when a token names a kid the last
// cached JWKS doesn't have. It is used internally by Lookup, and by callers
// that verify signatures over the whole key set themselves (and therefore
// detect an unknown kid outside of Lookup).
//
// Gated by minKidRefreshInterval and single-flighted via f.mu, the same mutex
// EnsureRegistered uses for its own initial fetch: a token naming a kid absent
// from the cached set runs before signature verification, so without the gate
// a client presenting made-up kids could force a fresh fetch on every attempt.
//
// Errors are logged, not returned: the caller has already failed to find the
// kid once and will simply fail again if the refresh didn't produce a usable
// key, which is the correct outcome for a genuinely invalid token. Only the
// kid's absence is logged, never any key material.
func (f *Fetcher) RefreshOnUnknownKid(ctx context.Context, jwksURL string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if time.Since(f.lastKidRefresh) < f.minKidRefreshInterval {
		return
	}
	f.lastKidRefresh = time.Now()

	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*f.registrationTimeout)
	defer cancel()
	if _, err := f.cache.Refresh(fetchCtx, jwksURL); err != nil {
		//nolint:gosec // G706: JWKS URL is from server configuration or OIDC discovery
		slog.Debug("JWKS refresh on unknown kid failed", "jwks_url", jwksURL, "error", err)
	}
}

// checkKeyCount enforces the maxKeys cap on a fetched set: a hostile endpoint
// serving many keys must not amplify verification CPU. Only counts are
// reported — never key material.
func (f *Fetcher) checkKeyCount(set jwk.Set) error {
	if f.maxKeys <= 0 {
		return nil
	}
	if n := set.Len(); n > f.maxKeys {
		return fmt.Errorf("JWKS contains too many keys: %d (max %d)", n, f.maxKeys)
	}
	return nil
}

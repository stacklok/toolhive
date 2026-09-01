// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package jwks

import (
	"net/http"
	"time"
)

// Option is a functional option for NewFetcher.
type Option func(*Fetcher)

// WithHTTPClient supplies a pre-built *http.Client to use as-is for every
// JWKS fetch (and exposed via HTTPClient). When set, the Fetcher skips
// building its own client from the flag options below — but still wraps the
// client's Transport with the body-cap transport unless the body limit is
// disabled, since jwx's cache has no cap of its own.
func WithHTTPClient(client *http.Client) Option {
	return func(f *Fetcher) {
		f.httpClient = client
	}
}

// WithInsecureAllowHTTP permits plain-HTTP JWKS URLs. Development and testing
// only — never set in production.
func WithInsecureAllowHTTP(allow bool) Option {
	return func(f *Fetcher) {
		f.insecureAllowHTTP = allow
	}
}

// WithAllowPrivateIPs permits JWKS endpoints resolving to private or loopback
// addresses. Use only when the issuer is hosted inside the same cluster and
// has no public endpoint.
func WithAllowPrivateIPs(allow bool) Option {
	return func(f *Fetcher) {
		f.allowPrivateIPs = allow
	}
}

// WithCABundle sets the path to a PEM CA certificate bundle. When set, ONLY
// certificates from that bundle are trusted (system roots are not included) —
// pinned-only trust, mirroring networking's HttpClientBuilder.WithCABundle.
func WithCABundle(path string) Option {
	return func(f *Fetcher) {
		f.caBundlePath = path
		f.caBundleUsesSystemRoots = false
	}
}

// WithSystemRootsPlusCABundle sets the path to a PEM CA certificate bundle and
// preserves trust in the system root pool — additive trust, mirroring
// networking's HttpClientBuilder.WithSystemRootsPlusCABundle. Use this when
// the upstream may use either a publicly trusted certificate or a private CA.
func WithSystemRootsPlusCABundle(path string) Option {
	return func(f *Fetcher) {
		f.caBundlePath = path
		f.caBundleUsesSystemRoots = true
	}
}

// WithAuthTokenFile sets the path to a file containing a bearer token sent as
// Authorization on every JWKS fetch.
func WithAuthTokenFile(path string) Option {
	return func(f *Fetcher) {
		f.authTokenFile = path
	}
}

// WithTimeout sets the HTTP client timeout for JWKS fetches. Zero keeps
// networking's default.
func WithTimeout(timeout time.Duration) Option {
	return func(f *Fetcher) {
		f.timeout = timeout
	}
}

// WithDisableKeepAlives disables HTTP keep-alive on the transport. When true,
// each request uses a fresh connection, ensuring the per-dial SSRF check fires
// on every request rather than being bypassed by a reused connection.
func WithDisableKeepAlives(disable bool) Option {
	return func(f *Fetcher) {
		f.disableKeepAlives = disable
	}
}

// WithSameHostRedirects restricts redirect hops to the host of the original
// request, guarding against a discovery/JWKS redirect landing on a different,
// unvetted host.
func WithSameHostRedirects(enable bool) Option {
	return func(f *Fetcher) {
		f.sameHostRedirects = enable
	}
}

// WithWorkers caps the background worker pool of the Fetcher's own cache.
// Zero keeps httprc's default of five workers.
func WithWorkers(workers int) Option {
	return func(f *Fetcher) {
		f.workers = workers
	}
}

// WithBodyLimit caps every JWKS response body at limit bytes. Zero disables
// the cap — not recommended for endpoints derived from untrusted discovery
// documents. Defaults to DefaultBodyLimit.
func WithBodyLimit(limit int64) Option {
	return func(f *Fetcher) {
		f.bodyLimit = limit
	}
}

// WithMaxKeys caps the number of keys accepted from a JWKS. Zero disables the
// cap. Defaults to DefaultMaxKeys.
func WithMaxKeys(limit int) Option {
	return func(f *Fetcher) {
		f.maxKeys = limit
	}
}

// WithRefreshInterval pins the fixed interval at which the cache re-fetches
// its JWKS in the background. It is passed to Register via
// jwk.WithConstantInterval, which makes the resource ignore the response's
// Cache-Control/Expires headers entirely rather than merely bounding them —
// deliberately: absent this override, httprc derives the interval from those
// headers, clamped to [15m, 30 days], so a hostile or misconfigured issuer
// could otherwise extend our own key-retention window up to a month simply by
// setting a long max-age. Zero (the default) keeps header-derived scheduling.
func WithRefreshInterval(interval time.Duration) Option {
	return func(f *Fetcher) {
		f.refreshInterval = interval
	}
}

// WithFetchFailureBackoff bounds how often EnsureRegistered retries a JWKS
// fetch that has never once succeeded. Defaults to DefaultFetchFailureBackoff.
func WithFetchFailureBackoff(backoff time.Duration) Option {
	return func(f *Fetcher) {
		f.fetchFailureBackoff = backoff
	}
}

// WithMinKidRefreshInterval bounds how often Lookup forces a refresh for an
// unknown key ID. Defaults to DefaultMinKidRefreshInterval.
func WithMinKidRefreshInterval(interval time.Duration) Option {
	return func(f *Fetcher) {
		f.minKidRefreshInterval = interval
	}
}

// WithRegistrationTimeout bounds the initial registration's ready-wait.
// Defaults to DefaultRegistrationTimeout.
func WithRegistrationTimeout(timeout time.Duration) Option {
	return func(f *Fetcher) {
		f.registrationTimeout = timeout
	}
}

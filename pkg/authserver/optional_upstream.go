// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v7"

	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

const (
	// optionalUpstreamStartupTimeout bounds the first construction attempt of
	// an optional upstream, so an unreachable issuer delays startup by at most
	// this much per optional upstream instead of the HTTP client's own
	// timeout. Like optionalUpstreamAttemptTimeout it is a context deadline,
	// so it holds for DefaultUpstreamFactory and for custom factories that
	// honor their context.
	optionalUpstreamStartupTimeout = 10 * time.Second
	// optionalUpstreamAttemptTimeout bounds each background retry.
	optionalUpstreamAttemptTimeout = 30 * time.Second
	// optionalUpstreamInitialInterval and optionalUpstreamMaxInterval shape the
	// exponential backoff between background retries.
	optionalUpstreamInitialInterval = 5 * time.Second
	optionalUpstreamMaxInterval     = 5 * time.Minute
)

// optionalUpstream wraps the provider of an optional upstream
// (UpstreamConfig.Required false). When the provider could not be constructed
// at startup, typically because OIDC discovery failed, the wrapper holds no
// provider and every method that would reach the upstream fails closed with
// upstream.ErrUpstreamUnavailable, so no authorization leg can complete
// through it. A background retry then constructs the provider, publishes it
// atomically, and all calls delegate to it from then on. An optional upstream
// that was constructed at startup is wrapped too, already resolved, so the
// server can report the state of every optional upstream in one place.
//
// The real provider is replaced, never mutated: readers load the pointer once
// per call, so a call that started before publication finishes against the
// placeholder and the next one sees the provider.
type optionalUpstream struct {
	name     string
	typ      upstream.ProviderType
	cfg      *UpstreamConfig
	factory  UpstreamProviderFactory
	resolved atomic.Pointer[resolvedUpstream]
	// initialInterval and maxInterval shape the exponential backoff between
	// background retries.
	initialInterval time.Duration
	maxInterval     time.Duration
}

// resolvedUpstream boxes the provider so it can be published through an
// atomic.Pointer.
type resolvedUpstream struct {
	provider upstream.OAuth2Provider
}

var (
	_ upstream.OAuth2Provider           = (*optionalUpstream)(nil)
	_ upstream.IdleConnectionCloser     = (*optionalUpstream)(nil)
	_ upstream.ClientCredentialsUpdater = (*optionalUpstream)(nil)
)

// newOptionalUpstream wraps provider, which is nil when it could not be
// constructed at startup. It does not start retrying; see
// (*optionalUpstream).retry.
func newOptionalUpstream(
	cfg *UpstreamConfig, factory UpstreamProviderFactory, provider upstream.OAuth2Provider,
) *optionalUpstream {
	o := &optionalUpstream{
		name:            cfg.Name,
		typ:             upstream.ProviderType(cfg.Type),
		cfg:             cfg,
		factory:         factory,
		initialInterval: optionalUpstreamInitialInterval,
		maxInterval:     optionalUpstreamMaxInterval,
	}
	if provider != nil {
		o.resolved.Store(&resolvedUpstream{provider: provider})
	}
	return o
}

// Type returns the configured provider type.
func (d *optionalUpstream) Type() upstream.ProviderType {
	if p := d.provider(); p != nil {
		return p.Type()
	}
	return d.typ
}

// AuthorizationURL delegates to the real provider, or fails with
// upstream.ErrUpstreamUnavailable while it is not constructed yet.
func (d *optionalUpstream) AuthorizationURL(
	state, codeChallenge string, opts ...upstream.AuthorizationOption,
) (string, error) {
	p := d.provider()
	if p == nil {
		return "", d.unavailable()
	}
	return p.AuthorizationURL(state, codeChallenge, opts...)
}

// ExchangeCodeForIdentity delegates to the real provider, or fails with
// upstream.ErrUpstreamUnavailable while it is not constructed yet.
func (d *optionalUpstream) ExchangeCodeForIdentity(
	ctx context.Context, code, codeVerifier, nonce string,
) (*upstream.Identity, error) {
	p := d.provider()
	if p == nil {
		return nil, d.unavailable()
	}
	return p.ExchangeCodeForIdentity(ctx, code, codeVerifier, nonce)
}

// RefreshTokens delegates to the real provider, or fails with
// upstream.ErrUpstreamUnavailable while it is not constructed yet.
func (d *optionalUpstream) RefreshTokens(
	ctx context.Context, refreshToken, expectedSubject string,
) (*upstream.Tokens, error) {
	p := d.provider()
	if p == nil {
		return nil, d.unavailable()
	}
	return p.RefreshTokens(ctx, refreshToken, expectedSubject)
}

// CloseIdleConnections forwards to the real provider when it has been
// constructed and supports it; the placeholder itself holds no connections.
func (d *optionalUpstream) CloseIdleConnections() {
	if closer, ok := d.provider().(upstream.IdleConnectionCloser); ok {
		closer.CloseIdleConnections()
	}
}

// UpdateClientCredentials forwards to the real provider when it has been
// constructed and supports replacing its client. While the provider is not
// constructed it fails with upstream.ErrUpstreamUnavailable rather than
// remembering the credentials, so a caller that re-checks periodically tries
// again later.
func (d *optionalUpstream) UpdateClientCredentials(creds upstream.ClientCredentials) error {
	p := d.provider()
	if p == nil {
		return d.unavailable()
	}
	updater, ok := p.(upstream.ClientCredentialsUpdater)
	if !ok {
		return fmt.Errorf("upstream %q cannot replace its client registration", d.name)
	}
	return updater.UpdateClientCredentials(creds)
}

// available reports whether the real provider has been constructed.
func (d *optionalUpstream) available() bool {
	return d.provider() != nil
}

// retry constructs the real provider with exponential backoff until it
// succeeds or ctx is cancelled. The first attempt waits initialInterval, since
// startup has just failed one. Failures are logged at DEBUG, and at WARN at
// most once per maxInterval so that an upstream that never recovers, for
// example because of a configuration error, stays visible in the logs. A
// provider constructed after ctx was cancelled is released and not published,
// so a closing server is not handed a live connection pool it no longer drains.
func (d *optionalUpstream) retry(ctx context.Context) {
	if !sleepContext(ctx, d.initialInterval) {
		return
	}

	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = d.initialInterval
	expBackoff.MaxInterval = d.maxInterval
	expBackoff.Multiplier = 2

	attempt := func() (upstream.OAuth2Provider, error) {
		attemptCtx, cancel := context.WithTimeout(ctx, optionalUpstreamAttemptTimeout)
		defer cancel()
		return constructProvider(attemptCtx, d.factory, d.cfg)
	}
	// notify runs on this goroutine only, so lastWarn needs no synchronization.
	lastWarn := time.Now() // startup has just logged the failure at WARN
	notify := func(err error, next time.Duration) {
		if time.Since(lastWarn) >= d.maxInterval {
			lastWarn = time.Now()
			slog.Warn("optional upstream is still unavailable; retrying in the background",
				"upstream", d.name, "error", err, "next_attempt_in", next)
			return
		}
		slog.Debug("optional upstream still unavailable, retrying",
			"upstream", d.name, "error", err, "next_attempt_in", next)
	}

	provider, err := backoff.Retry(ctx, attempt,
		backoff.WithBackOff(expBackoff),
		backoff.WithMaxElapsedTime(0), // retry until ctx is cancelled
		backoff.WithNotify(notify),
	)
	if err != nil {
		// The only stop condition left is ctx cancellation: the server closes.
		slog.Debug("stopped retrying optional upstream", "upstream", d.name, "error", err)
		return
	}
	if ctx.Err() != nil {
		if closer, ok := provider.(upstream.IdleConnectionCloser); ok {
			closer.CloseIdleConnections()
		}
		return
	}
	d.resolved.Store(&resolvedUpstream{provider: provider})
	// INFO, not DEBUG: it closes the WARN logged when the upstream failed.
	slog.Info("optional upstream is available", "upstream", d.name)
}

// sleepContext waits for d or until ctx is cancelled, and reports whether the
// full wait elapsed.
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// provider returns the real provider, or nil while it is not constructed.
func (d *optionalUpstream) provider() upstream.OAuth2Provider {
	if r := d.resolved.Load(); r != nil {
		return r.provider
	}
	return nil
}

func (d *optionalUpstream) unavailable() error {
	return fmt.Errorf("upstream %q: %w", d.name, upstream.ErrUpstreamUnavailable)
}

// constructProvider runs factory for an optional upstream and treats a nil
// provider, which would otherwise reach the authorization chain and panic on
// first use, as a failed attempt. Callers log the upstream's name with it.
func constructProvider(
	ctx context.Context, factory UpstreamProviderFactory, cfg *UpstreamConfig,
) (upstream.OAuth2Provider, error) {
	provider, err := factory(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if isNilProvider(provider) {
		return nil, errors.New("upstream factory returned a nil provider")
	}
	return provider, nil
}

// optionalRetries runs the background retries of the unavailable optional upstreams for
// the lifetime of the server.
type optionalRetries struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// startOptionalRetries starts one retry goroutine per unavailable optional upstream in
// upstreams and returns a handle that stops them, or nil when there is none.
// The goroutines run on a context of their own, detached from the startup
// context, so they outlive construction and stop only through stop.
func startOptionalRetries(upstreams []handlers.NamedUpstream) *optionalRetries {
	var pending []*optionalUpstream
	for _, u := range upstreams {
		if d, ok := u.Provider.(*optionalUpstream); ok && !d.available() {
			pending = append(pending, d)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &optionalRetries{cancel: cancel}
	for _, d := range pending {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			d.retry(ctx)
		}()
	}
	return r
}

// stop cancels the retries and waits for them to return. It is safe on a nil
// receiver.
func (r *optionalRetries) stop() {
	if r == nil {
		return
	}
	r.cancel()
	r.wg.Wait()
}

// UpstreamAvailability is the startup state of one optional upstream.
type UpstreamAvailability struct {
	// Name is the upstream's configured name.
	Name string
	// Available is false while the upstream's provider could not be
	// constructed and is being retried in the background.
	Available bool
}

// upstreamAvailabilityReporter is the optional capability of a Server to
// report the state of its optional upstreams.
type upstreamAvailabilityReporter interface {
	OptionalUpstreams() []UpstreamAvailability
}

// OptionalUpstreams returns the state of every optional upstream of s, in
// configured order, or nil when s has none or lacks the capability. Required
// upstreams are not listed: a server only runs once all of them were built.
func OptionalUpstreams(s Server) []UpstreamAvailability {
	reporter, ok := s.(upstreamAvailabilityReporter)
	if !ok {
		return nil
	}
	return reporter.OptionalUpstreams()
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"

	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	spiffeBundleInitialLoadTimeout = 30 * time.Second
	spiffeBundleMaxResponseSize    = 1 << 20
	spiffeBundleMaxResponseDrain   = 64 << 10
	spiffeBundleMaxStaleAge        = 24 * time.Hour
	spiffeBundleDefaultRefresh     = 5 * time.Minute
	spiffeBundleFailureRefresh     = time.Minute
	spiffeBundleMinimumRefresh     = 30 * time.Second
)

// spiffeMultiDomainBundleSource provides live trust bundles for the configured
// SPIFFE trust domains. It is both an x509bundle.Source and jwtbundle.Source.
type spiffeMultiDomainBundleSource struct {
	byTrustDomain map[spiffeid.TrustDomain]*spiffeLiveBundleSource
	cancel        context.CancelFunc
	workers       sync.WaitGroup
	closers       []func() error
	closeOnce     sync.Once
	closeErr      error
}

type spiffeLiveBundleSource struct {
	x509    x509bundle.Source
	jwt     jwtbundle.Source
	methods map[SPIFFEAuthenticationMethod]struct{}
}

type bundleHolder struct {
	current atomic.Pointer[bundleSnapshot]
}

type bundleSnapshot struct {
	bundle    *spiffebundle.Bundle
	fetchedAt time.Time
}

// store publishes bundle as a complete valid snapshot. Once a sequence number
// has been observed, it rejects a missing, lower, or conflicting equal sequence
// so replayed endpoint responses cannot refresh the last-known-good age.
func (h *bundleHolder) store(bundle *spiffebundle.Bundle) error {
	for {
		current := h.current.Load()
		if err := validateBundleSnapshotUpdate(current, bundle); err != nil {
			return err
		}
		next := &bundleSnapshot{bundle: bundle, fetchedAt: time.Now()}
		if h.current.CompareAndSwap(current, next) {
			return nil
		}
	}
}

func validateBundleSnapshotUpdate(current *bundleSnapshot, bundle *spiffebundle.Bundle) error {
	if current == nil || current.bundle == nil {
		return nil
	}
	currentSequence, currentHasSequence := current.bundle.SequenceNumber()
	if !currentHasSequence {
		return nil
	}
	sequence, hasSequence := bundle.SequenceNumber()
	if !hasSequence {
		return fmt.Errorf("SPIFFE bundle omits sequence number after sequence %d was observed", currentSequence)
	}
	if sequence < currentSequence {
		return fmt.Errorf("SPIFFE bundle sequence %d is lower than current sequence %d", sequence, currentSequence)
	}
	if sequence == currentSequence && !bundle.Equal(current.bundle) {
		return fmt.Errorf("SPIFFE bundle sequence %d conflicts with the current bundle", sequence)
	}
	return nil
}

// newSPIFFEMultiDomainBundleSource constructs all configured bundle sources and
// completes their initial load before returning. The supplied context governs
// the source lifetime; the initial-load timeout does not govern refresh workers.
func newSPIFFEMultiDomainBundleSource(
	ctx context.Context, cfg *SPIFFETrustConfig,
) (*spiffeMultiDomainBundleSource, error) {
	if cfg == nil || len(cfg.trustDomains) == 0 {
		return nil, nil
	}

	runtimeCtx, cancel := context.WithCancel(ctx)
	source := &spiffeMultiDomainBundleSource{
		byTrustDomain: make(map[spiffeid.TrustDomain]*spiffeLiveBundleSource, len(cfg.trustDomains)),
		cancel:        cancel,
	}
	initialCtx, initialCancel := context.WithTimeout(ctx, spiffeBundleInitialLoadTimeout)
	defer initialCancel()

	trustDomainNames := make([]string, 0, len(cfg.trustDomains))
	for name := range cfg.trustDomains {
		trustDomainNames = append(trustDomainNames, name)
	}
	sort.Strings(trustDomainNames)

	var workloadSources workloadAPIBundleSources
	for _, name := range trustDomainNames {
		domain := cfg.trustDomains[name]
		trustDomain, err := spiffeid.TrustDomainFromString(domain.TrustDomain())
		if err != nil {
			return nil, closeSPIFFEBundleSourceAfterError(source, fmt.Errorf("SPIFFE trust domain %q: %w", name, err))
		}
		if _, exists := source.byTrustDomain[trustDomain]; exists {
			return nil, closeSPIFFEBundleSourceAfterError(source, fmt.Errorf("duplicate SPIFFE trust domain %q", trustDomain))
		}

		live, err := newSPIFFELiveBundleSource(initialCtx, runtimeCtx, trustDomain, domain, source, &workloadSources)
		if err != nil {
			return nil, closeSPIFFEBundleSourceAfterError(source, fmt.Errorf("load SPIFFE trust bundle for %q: %w", trustDomain, err))
		}
		source.byTrustDomain[trustDomain] = live
	}

	return source, nil
}

// closeSPIFFEBundleSourceAfterError preserves the construction failure while
// reporting a secondary resource-cleanup failure.
func closeSPIFFEBundleSourceAfterError(source *spiffeMultiDomainBundleSource, primary error) error {
	if err := source.Close(); err != nil {
		slog.Warn("failed to clean up SPIFFE bundle source after construction failure", "error", err)
	}
	return primary
}

func (s *spiffeMultiDomainBundleSource) GetX509BundleForTrustDomain(
	trustDomain spiffeid.TrustDomain,
) (*x509bundle.Bundle, error) {
	live, err := s.sourceFor(trustDomain, SPIFFEAuthenticationMethodX509)
	if err != nil {
		return nil, err
	}
	return live.x509.GetX509BundleForTrustDomain(trustDomain)
}

func (s *spiffeMultiDomainBundleSource) GetJWTBundleForTrustDomain(
	trustDomain spiffeid.TrustDomain,
) (*jwtbundle.Bundle, error) {
	live, err := s.sourceFor(trustDomain, SPIFFEAuthenticationMethodJWT)
	if err != nil {
		return nil, err
	}
	return live.jwt.GetJWTBundleForTrustDomain(trustDomain)
}

// Close stops all bundle refresh workers and releases their clients. It is safe
// to call more than once.
func (s *spiffeMultiDomainBundleSource) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.cancel()
		s.workers.Wait()
		errs := make([]error, 0, len(s.closers))
		for index := len(s.closers) - 1; index >= 0; index-- {
			if err := s.closers[index](); err != nil {
				errs = append(errs, err)
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// workloadAPIBundleSources shares the method-specific Workload API sources
// across all configured trust domains. Only sources for enabled methods are
// created, so a disabled credential stream cannot block initial validation.
type workloadAPIBundleSources struct {
	client *workloadapi.Client
	x509   *workloadapi.X509Source
	jwt    *workloadapi.JWTSource
}

func newSPIFFELiveBundleSource(
	initialCtx, runtimeCtx context.Context,
	trustDomain spiffeid.TrustDomain,
	domain SPIFFETrustDomain,
	multi *spiffeMultiDomainBundleSource,
	workloadSources *workloadAPIBundleSources,
) (*spiffeLiveBundleSource, error) {
	methods := make(map[SPIFFEAuthenticationMethod]struct{}, len(domain.Methods()))
	for _, method := range domain.Methods() {
		methods[method] = struct{}{}
	}
	live := &spiffeLiveBundleSource{methods: methods}

	switch domain.BundleSource().Type() {
	case SPIFFEBundleSourceTypeWorkloadAPI:
		if _, enabled := methods[SPIFFEAuthenticationMethodX509]; enabled {
			x509Source, err := workloadSources.x509Source(initialCtx, runtimeCtx, multi)
			if err != nil {
				return nil, fmt.Errorf("create Workload API X.509 bundle source: %w", err)
			}
			live.x509 = x509Source
		}
		if _, enabled := methods[SPIFFEAuthenticationMethodJWT]; enabled {
			jwtSource, err := workloadSources.jwtSource(initialCtx, runtimeCtx, multi)
			if err != nil {
				return nil, fmt.Errorf("create Workload API JWT bundle source: %w", err)
			}
			live.jwt = jwtSource
		}
	case SPIFFEBundleSourceTypeEndpoint:
		if domain.BundleSource().Profile() != SPIFFEBundleEndpointProfileHTTPSWeb {
			return nil, fmt.Errorf("https_spiffe bundle endpoints are not supported: configure bootstrap trust with https_web")
		}
		endpoint, err := newHTTPSWebBundleEndpoint(trustDomain, domain.BundleSource().Endpoint())
		if err != nil {
			return nil, err
		}
		multi.closers = append(multi.closers, func() error {
			endpoint.client.CloseIdleConnections()
			return nil
		})
		bundle, err := endpoint.fetch(initialCtx)
		if err != nil {
			return nil, fmt.Errorf("initial bundle fetch: %w", err)
		}
		holder := &bundleHolder{}
		if err := holder.store(bundle); err != nil {
			return nil, fmt.Errorf("store initial SPIFFE bundle: %w", err)
		}
		live.x509 = holder
		live.jwt = holder
		multi.workers.Add(1)
		go endpoint.poll(runtimeCtx, holder, &multi.workers)
	default:
		return nil, fmt.Errorf("unsupported SPIFFE bundle source type %q", domain.BundleSource().Type())
	}
	if err := validateInitialBundles(live, trustDomain); err != nil {
		return nil, err
	}
	return live, nil
}

func (s *workloadAPIBundleSources) clientFor(
	runtimeCtx context.Context, multi *spiffeMultiDomainBundleSource,
) (*workloadapi.Client, error) {
	if s.client != nil {
		return s.client, nil
	}
	client, err := workloadapi.New(runtimeCtx)
	if err != nil {
		return nil, err
	}
	s.client = client
	multi.closers = append(multi.closers, client.Close)
	return client, nil
}

func (s *workloadAPIBundleSources) x509Source(
	initialCtx, runtimeCtx context.Context, multi *spiffeMultiDomainBundleSource,
) (*workloadapi.X509Source, error) {
	if s.x509 != nil {
		return s.x509, nil
	}
	client, err := s.clientFor(runtimeCtx, multi)
	if err != nil {
		return nil, err
	}
	source, err := awaitWorkloadSource(initialCtx, func() (*workloadapi.X509Source, error) {
		return workloadapi.NewX509Source(runtimeCtx, workloadapi.WithClient(client))
	})
	if err != nil {
		return nil, err
	}
	s.x509 = source
	multi.closers = append(multi.closers, source.Close)
	return source, nil
}

func (s *workloadAPIBundleSources) jwtSource(
	initialCtx, runtimeCtx context.Context, multi *spiffeMultiDomainBundleSource,
) (*workloadapi.JWTSource, error) {
	if s.jwt != nil {
		return s.jwt, nil
	}
	client, err := s.clientFor(runtimeCtx, multi)
	if err != nil {
		return nil, err
	}
	source, err := awaitWorkloadSource(initialCtx, func() (*workloadapi.JWTSource, error) {
		return workloadapi.NewJWTSource(runtimeCtx, workloadapi.WithClient(client))
	})
	if err != nil {
		return nil, err
	}
	s.jwt = source
	multi.closers = append(multi.closers, source.Close)
	return source, nil
}

func awaitWorkloadSource[T interface {
	Close() error
	comparable
}](
	initialCtx context.Context, create func() (T, error),
) (T, error) {
	type result struct {
		source T
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		source, err := create()
		resultCh <- result{source: source, err: err}
	}()
	select {
	case result := <-resultCh:
		return result.source, result.err
	case <-initialCtx.Done():
		go func() {
			result := <-resultCh
			var zero T
			if result.source != zero {
				if err := result.source.Close(); err != nil {
					slog.Warn("failed to shut down timed-out SPIFFE Workload API bundle source", "error", err)
				}
			}
		}()
		var zero T
		return zero, initialCtx.Err()
	}
}

func validateInitialBundles(live *spiffeLiveBundleSource, trustDomain spiffeid.TrustDomain) error {
	if _, enabled := live.methods[SPIFFEAuthenticationMethodX509]; enabled {
		bundle, err := live.x509.GetX509BundleForTrustDomain(trustDomain)
		if err != nil {
			return fmt.Errorf("initial X.509 bundle: %w", err)
		}
		if len(bundle.X509Authorities()) == 0 {
			return fmt.Errorf("initial X.509 bundle has no authorities")
		}
	}
	if _, enabled := live.methods[SPIFFEAuthenticationMethodJWT]; enabled {
		bundle, err := live.jwt.GetJWTBundleForTrustDomain(trustDomain)
		if err != nil {
			return fmt.Errorf("initial JWT bundle: %w", err)
		}
		if len(bundle.JWTAuthorities()) == 0 {
			return fmt.Errorf("initial JWT bundle has no authorities")
		}
	}
	return nil
}

func (s *spiffeMultiDomainBundleSource) sourceFor(
	trustDomain spiffeid.TrustDomain, method SPIFFEAuthenticationMethod,
) (*spiffeLiveBundleSource, error) {
	live, ok := s.byTrustDomain[trustDomain]
	if !ok {
		return nil, fmt.Errorf("no SPIFFE bundle source configured for trust domain %q", trustDomain)
	}
	if _, ok := live.methods[method]; !ok {
		return nil, fmt.Errorf("SPIFFE authentication method %q is not enabled for trust domain %q", method, trustDomain)
	}
	return live, nil
}

func (h *bundleHolder) GetX509BundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	bundle, err := h.bundle(trustDomain)
	if err != nil {
		return nil, err
	}
	return bundle.GetX509BundleForTrustDomain(trustDomain)
}

func (h *bundleHolder) GetJWTBundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*jwtbundle.Bundle, error) {
	bundle, err := h.bundle(trustDomain)
	if err != nil {
		return nil, err
	}
	return bundle.GetJWTBundleForTrustDomain(trustDomain)
}

func (h *bundleHolder) bundle(trustDomain spiffeid.TrustDomain) (*spiffebundle.Bundle, error) {
	snapshot := h.current.Load()
	if snapshot == nil || snapshot.bundle == nil {
		return nil, fmt.Errorf("no SPIFFE bundle loaded for trust domain %q", trustDomain)
	}
	if time.Since(snapshot.fetchedAt) > spiffeBundleMaxStaleAge {
		return nil, fmt.Errorf("SPIFFE bundle for trust domain %q is older than %s", trustDomain, spiffeBundleMaxStaleAge)
	}
	return snapshot.bundle, nil
}

type httpsWebBundleEndpoint struct {
	trustDomain spiffeid.TrustDomain
	url         string
	client      *http.Client
}

func newHTTPSWebBundleEndpoint(trustDomain spiffeid.TrustDomain, endpoint string) (*httpsWebBundleEndpoint, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint URL: %w", err)
	}
	client, err := networking.NewHostScopedClientBuilder(parsed.Host, false, false).
		WithDisableKeepAlives(true).
		Build()
	if err != nil {
		return nil, fmt.Errorf("build endpoint HTTP client: %w", err)
	}
	client.CheckRedirect = networking.SameHostRedirectPolicy()
	return &httpsWebBundleEndpoint{trustDomain: trustDomain, url: endpoint, client: client}, nil
}

func (e *httpsWebBundleEndpoint) fetch(ctx context.Context) (*spiffebundle.Bundle, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request bundle endpoint: %w", err)
	}
	defer func() {
		// Keep cleanup bounded: keep-alives are disabled, so draining beyond this
		// limit cannot improve connection reuse.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, spiffeBundleMaxResponseDrain))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("bundle endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, spiffeBundleMaxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read bundle endpoint response: %w", err)
	}
	if len(body) > spiffeBundleMaxResponseSize {
		return nil, fmt.Errorf("bundle endpoint response exceeds %d bytes", spiffeBundleMaxResponseSize)
	}
	bundle, err := spiffebundle.Read(e.trustDomain, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("read SPIFFE bundle: %w", err)
	}
	return bundle, nil
}

func (e *httpsWebBundleEndpoint) poll(ctx context.Context, holder *bundleHolder, worker *sync.WaitGroup) {
	defer worker.Done()
	interval := refreshInterval(holder.current.Load().bundle)
	failed := false
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}

		bundle, err := e.fetch(ctx)
		if err == nil {
			err = holder.store(bundle)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !failed {
				slog.Warn("SPIFFE bundle refresh failed; retaining last known good bundle", "trust_domain", e.trustDomain, "error", err)
				failed = true
			}
			interval = spiffeBundleFailureRefresh
			continue
		}
		if failed {
			slog.Debug("SPIFFE bundle refresh recovered", "trust_domain", e.trustDomain)
			failed = false
		}
		interval = refreshInterval(bundle)
	}
}

func refreshInterval(bundle *spiffebundle.Bundle) time.Duration {
	if hint, ok := bundle.RefreshHint(); ok && hint > 0 {
		return min(max(hint, spiffeBundleMinimumRefresh), spiffeBundleMaxStaleAge)
	}
	return spiffeBundleDefaultRefresh
}

var _ x509bundle.Source = (*spiffeMultiDomainBundleSource)(nil)
var _ jwtbundle.Source = (*spiffeMultiDomainBundleSource)(nil)

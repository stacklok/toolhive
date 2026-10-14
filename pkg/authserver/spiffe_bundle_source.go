// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

const (
	spiffeBundleMaxStaleAge      = 24 * time.Hour
	spiffeBundleFilePollInterval = 30 * time.Second
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
// so a replayed reload cannot refresh the last-known-good age.
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
// completes their initial load before returning. The supplied context controls
// refresh workers. Initial regular-file reads are synchronous and cannot be
// canceled.
//
//nolint:unused // wired into server construction in the next PR of the stack
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

	trustDomainNames := make([]string, 0, len(cfg.trustDomains))
	for name := range cfg.trustDomains {
		trustDomainNames = append(trustDomainNames, name)
	}
	sort.Strings(trustDomainNames)

	for _, name := range trustDomainNames {
		domain := cfg.trustDomains[name]
		trustDomain, err := spiffeid.TrustDomainFromString(domain.TrustDomain())
		if err != nil {
			return nil, closeSPIFFEBundleSourceAfterError(source, fmt.Errorf("SPIFFE trust domain %q: %w", name, err))
		}
		if _, exists := source.byTrustDomain[trustDomain]; exists {
			return nil, closeSPIFFEBundleSourceAfterError(source, fmt.Errorf("duplicate SPIFFE trust domain %q", trustDomain))
		}

		live, err := newSPIFFELiveBundleSource(runtimeCtx, trustDomain, domain, source)
		if err != nil {
			return nil, closeSPIFFEBundleSourceAfterError(source, fmt.Errorf("load SPIFFE trust bundle for %q: %w", trustDomain, err))
		}
		source.byTrustDomain[trustDomain] = live
	}

	return source, nil
}

// closeSPIFFEBundleSourceAfterError preserves the construction failure while
// reporting a secondary resource-cleanup failure.
//
//nolint:unused // wired into server construction in the next PR of the stack
func closeSPIFFEBundleSourceAfterError(source *spiffeMultiDomainBundleSource, primary error) error {
	if err := source.Close(); err != nil {
		slog.Warn("failed to clean up SPIFFE bundle source after construction failure", "error", err)
	}
	return primary
}

// SPIFFEX509Authorities returns the current X.509 authorities for every
// trust domain that enables X.509 authentication. A bundle that cannot be
// read during rotation is skipped so one stale domain does not hide others.
func (s *spiffeMultiDomainBundleSource) SPIFFEX509Authorities() []*x509.Certificate {
	if s == nil {
		return nil
	}
	authorities := make([]*x509.Certificate, 0)
	domains := make([]spiffeid.TrustDomain, 0, len(s.byTrustDomain))
	for trustDomain := range s.byTrustDomain {
		domains = append(domains, trustDomain)
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i].String() < domains[j].String() })
	for _, trustDomain := range domains {
		live := s.byTrustDomain[trustDomain]
		if live == nil {
			continue
		}
		if _, enabled := live.methods[SPIFFEAuthenticationMethodX509]; !enabled {
			continue
		}
		bundle, err := s.GetX509BundleForTrustDomain(trustDomain)
		if err != nil {
			slog.Debug("failed to load SPIFFE X.509 bundle authorities", "trust_domain", trustDomain, "error", err)
			continue
		}
		if bundle != nil {
			authorities = append(authorities, bundle.X509Authorities()...)
		}
	}
	return authorities
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

func newSPIFFELiveBundleSource(
	runtimeCtx context.Context,
	trustDomain spiffeid.TrustDomain,
	domain SPIFFETrustDomain,
	multi *spiffeMultiDomainBundleSource,
) (*spiffeLiveBundleSource, error) {
	methods := make(map[SPIFFEAuthenticationMethod]struct{}, len(domain.Methods()))
	for _, method := range domain.Methods() {
		methods[method] = struct{}{}
	}
	live := &spiffeLiveBundleSource{methods: methods}

	switch domain.BundleSource().Type() {
	case SPIFFEBundleSourceTypeFile:
		path := domain.BundleSource().Path()
		bundle, err := loadSPIFFEBundleFile(trustDomain, path)
		if err != nil {
			return nil, fmt.Errorf("initial bundle load: %w", err)
		}
		if err := validateSPIFFEBundleAuthorities(bundle, trustDomain, methods); err != nil {
			return nil, fmt.Errorf("initial bundle validation: %w", err)
		}
		holder := &bundleHolder{}
		if err := holder.store(bundle); err != nil {
			return nil, fmt.Errorf("store initial SPIFFE bundle: %w", err)
		}
		live.x509 = holder
		live.jwt = holder
		multi.workers.Add(1)
		go pollFileBundle(runtimeCtx, trustDomain, path, holder, live.methods, &multi.workers)
	case SPIFFEBundleSourceTypeEndpoint, SPIFFEBundleSourceTypeWorkloadAPI:
		return nil, fmt.Errorf("unsupported SPIFFE bundle source type %q: no loader in this build", domain.BundleSource().Type())
	default:
		return nil, fmt.Errorf("unsupported SPIFFE bundle source type %q", domain.BundleSource().Type())
	}
	return live, nil
}

func loadSPIFFEBundleFile(trustDomain spiffeid.TrustDomain, path string) (*spiffebundle.Bundle, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat SPIFFE bundle file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("SPIFFE bundle path %q is not a regular file", path)
	}
	return spiffebundle.Load(trustDomain, path)
}

func validateSPIFFEBundleAuthorities(
	bundle *spiffebundle.Bundle,
	trustDomain spiffeid.TrustDomain,
	methods map[SPIFFEAuthenticationMethod]struct{},
) error {
	if _, enabled := methods[SPIFFEAuthenticationMethodX509]; enabled {
		x509Bundle, err := bundle.GetX509BundleForTrustDomain(trustDomain)
		if err != nil {
			return fmt.Errorf("X.509 bundle: %w", err)
		}
		if len(x509Bundle.X509Authorities()) == 0 {
			return fmt.Errorf("X.509 bundle has no authorities")
		}
	}
	if _, enabled := methods[SPIFFEAuthenticationMethodJWT]; enabled {
		jwtBundle, err := bundle.GetJWTBundleForTrustDomain(trustDomain)
		if err != nil {
			return fmt.Errorf("JWT bundle: %w", err)
		}
		if len(jwtBundle.JWTAuthorities()) == 0 {
			return fmt.Errorf("JWT bundle has no authorities")
		}
	}
	return nil
}

func reloadSPIFFEBundleFile(
	trustDomain spiffeid.TrustDomain,
	path string,
	holder *bundleHolder,
	methods map[SPIFFEAuthenticationMethod]struct{},
) error {
	bundle, err := loadSPIFFEBundleFile(trustDomain, path)
	if err != nil {
		return err
	}
	if err := validateSPIFFEBundleAuthorities(bundle, trustDomain, methods); err != nil {
		return err
	}
	return holder.store(bundle)
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

// pollFileBundle reloads a locally mounted SPIFFE bundle file on a fixed
// interval. A ConfigMap-mounted file is updated via a symlink swap the
// kubelet performs on its own sync period, which polling observes correctly
// and inotify on the mounted path frequently misses. A failed reload is
// logged and the last known good bundle is retained.
func pollFileBundle(
	ctx context.Context,
	trustDomain spiffeid.TrustDomain,
	path string,
	holder *bundleHolder,
	methods map[SPIFFEAuthenticationMethod]struct{},
	worker *sync.WaitGroup,
) {
	defer worker.Done()
	failed := false
	ticker := time.NewTicker(spiffeBundleFilePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		err := reloadSPIFFEBundleFile(trustDomain, path, holder, methods)
		if err != nil {
			if !failed {
				slog.Warn("SPIFFE bundle file reload failed; retaining last known good bundle",
					"trust_domain", trustDomain, "path", path, "error", err)
				failed = true
			}
			continue
		}
		if failed {
			slog.Debug("SPIFFE bundle file reload recovered", "trust_domain", trustDomain)
			failed = false
		}
	}
}

var _ x509bundle.Source = (*spiffeMultiDomainBundleSource)(nil)
var _ jwtbundle.Source = (*spiffeMultiDomainBundleSource)(nil)

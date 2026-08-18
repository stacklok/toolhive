// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
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

	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

const spiffeBundleFileRefreshInterval = time.Minute

// spiffeBundleFileSource loads and atomically replaces one locally mounted SPIFFE
// trust bundle. Failed reloads retain the complete last-known-good bundle.
type spiffeBundleFileSource struct {
	trustDomain spiffeid.TrustDomain
	methods     map[SPIFFEAuthenticationMethod]struct{}
	path        string
	interval    time.Duration

	bundle atomic.Pointer[spiffebundle.Bundle]

	lifecycleMu sync.Mutex
	closed      bool
	cancel      context.CancelFunc
	done        chan struct{}
}

var _ spiffeBundleSource = (*spiffeBundleFileSource)(nil)

func newSPIFFEBundleFileSource(
	trustDomain spiffeid.TrustDomain,
	methods []SPIFFEAuthenticationMethod,
	path string,
	interval time.Duration,
) (*spiffeBundleFileSource, error) {
	if interval <= 0 {
		return nil, errors.New("SPIFFE bundle file refresh interval must be positive")
	}
	return &spiffeBundleFileSource{
		trustDomain: trustDomain,
		methods:     enabledSPIFFEBundleMethods(methods),
		path:        path,
		interval:    interval,
	}, nil
}

func (s *spiffeBundleFileSource) start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return errSPIFFEBundleSourceClosed
	}
	if s.cancel != nil {
		s.lifecycleMu.Unlock()
		return errors.New("SPIFFE bundle file source is already started")
	}
	_, initialCancel := context.WithCancel(ctx)
	initialDone := make(chan struct{})
	s.cancel = initialCancel
	s.done = initialDone
	s.lifecycleMu.Unlock()

	err := s.refresh()
	s.lifecycleMu.Lock()
	if err != nil {
		initialCancel()
		if s.done == initialDone {
			s.cancel = nil
			s.done = nil
		}
		close(initialDone)
		s.lifecycleMu.Unlock()
		return err
	}
	if s.closed || ctx.Err() != nil {
		initialCancel()
		s.cancel = nil
		s.done = nil
		close(initialDone)
		s.lifecycleMu.Unlock()
		return errSPIFFEBundleSourceClosed
	}

	initialCancel()
	refreshCtx, refreshCancel := context.WithCancel(context.Background())
	refreshDone := make(chan struct{})
	s.cancel = refreshCancel
	s.done = refreshDone
	close(initialDone)
	s.lifecycleMu.Unlock()
	go s.run(refreshCtx, refreshDone)
	return nil
}

func (s *spiffeBundleFileSource) close() {
	s.lifecycleMu.Lock()
	s.closed = true
	cancel := s.cancel
	done := s.done
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *spiffeBundleFileSource) GetX509BundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	bundle, err := s.bundleForTrustDomain(trustDomain)
	if err != nil {
		return nil, err
	}
	return bundle.X509Bundle(), nil
}

func (s *spiffeBundleFileSource) GetJWTBundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*jwtbundle.Bundle, error) {
	bundle, err := s.bundleForTrustDomain(trustDomain)
	if err != nil {
		return nil, err
	}
	return bundle.JWTBundle(), nil
}

func (s *spiffeBundleFileSource) bundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*spiffebundle.Bundle, error) {
	if trustDomain != s.trustDomain {
		return nil, fmt.Errorf("no SPIFFE bundle configured for trust domain %q", trustDomain)
	}
	bundle := s.bundle.Load()
	if bundle == nil {
		return nil, errSPIFFEBundleUnavailable
	}
	return bundle, nil
}

func (s *spiffeBundleFileSource) run(ctx context.Context, done chan struct{}) {
	ticker := time.NewTicker(s.interval)
	defer func() {
		ticker.Stop()
		s.lifecycleMu.Lock()
		if s.done == done {
			s.cancel = nil
		}
		s.lifecycleMu.Unlock()
		close(done)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.refresh(); err != nil {
				slog.Default().Warn("SPIFFE bundle file refresh failed", "trust_domain", s.trustDomain.String(), "source_type", "file", "error", err)
				continue
			}
			slog.Default().Debug("SPIFFE bundle file refresh succeeded", "trust_domain", s.trustDomain.String(), "source_type", "file")
		}
	}
}

func (s *spiffeBundleFileSource) refresh() error {
	bundle, err := spiffebundle.Load(s.trustDomain, s.path)
	if err != nil {
		return fmt.Errorf("load SPIFFE bundle file: %w", err)
	}
	if _, enabled := s.methods[SPIFFEAuthenticationMethodX509]; enabled && len(bundle.X509Authorities()) == 0 {
		return errors.New("SPIFFE bundle file has no X.509 authorities for an enabled method")
	}
	if _, enabled := s.methods[SPIFFEAuthenticationMethodJWT]; enabled && len(bundle.JWTAuthorities()) == 0 {
		return errors.New("SPIFFE bundle file has no JWT authorities for an enabled method")
	}
	s.bundle.Store(bundle)
	return nil
}

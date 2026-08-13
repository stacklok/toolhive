// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

const spiffeBundleRegistryReadinessTimeout = 10 * time.Second

// SPIFFEBundleRegistry provides X.509 and JWT bundles only for the SPIFFE trust
// domains explicitly declared in a validated SPIFFE trust configuration.
type SPIFFEBundleRegistry struct {
	bundles map[spiffeid.TrustDomain]spiffeBundleRegistration

	endpointSources []*spiffeBundleEndpointSource
	workloadSource  *workloadapi.BundleSource
	workloadCancel  context.CancelFunc
	hasWorkloadAPI  bool

	lifecycleMu sync.Mutex
	started     bool
	closed      bool
}

type spiffeBundleRegistration struct {
	source  spiffeBundleSource
	methods map[SPIFFEAuthenticationMethod]struct{}
}

type spiffeBundleSource interface {
	GetX509BundleForTrustDomain(spiffeid.TrustDomain) (*x509bundle.Bundle, error)
	GetJWTBundleForTrustDomain(spiffeid.TrustDomain) (*jwtbundle.Bundle, error)
}

type endpointBundleSource struct {
	source *spiffeBundleEndpointSource
}

var (
	_ x509bundle.Source = (*SPIFFEBundleRegistry)(nil)
	_ jwtbundle.Source  = (*SPIFFEBundleRegistry)(nil)
)

// NewSPIFFEBundleRegistry creates a trust-domain-keyed bundle registry from a
// validated trust configuration. A nil configuration means SPIFFE is absent and
// returns nil without starting a bundle source.
func NewSPIFFEBundleRegistry(trust *SPIFFETrustConfig) (*SPIFFEBundleRegistry, error) {
	if trust == nil {
		return nil, nil
	}
	if !trust.validated {
		return nil, fmt.Errorf("SPIFFE trust config must be constructed with NewSPIFFETrustConfig")
	}

	registry := &SPIFFEBundleRegistry{
		bundles: make(map[spiffeid.TrustDomain]spiffeBundleRegistration, len(trust.domains)),
	}
	for _, domain := range trust.domains {
		trustDomain := domain.trustDomain
		registration := spiffeBundleRegistration{
			methods: make(map[SPIFFEAuthenticationMethod]struct{}, len(domain.methods)),
		}
		for _, method := range domain.methods {
			registration.methods[method] = struct{}{}
		}
		switch source := domain.bundleSource; source.Type() {
		case SPIFFEBundleSourceTypeEndpoint:
			endpoint, err := newSPIFFEBundleEndpointSource(trustDomain, domain.methods, source.Endpoint())
			if err != nil {
				return nil, fmt.Errorf("create SPIFFE bundle source for trust domain %q: %w", trustDomain, err)
			}
			registration.source = endpointBundleSource{source: endpoint}
			registry.endpointSources = append(registry.endpointSources, endpoint)
		case SPIFFEBundleSourceTypeWorkloadAPI:
			registry.hasWorkloadAPI = true
		default:
			return nil, fmt.Errorf("unsupported SPIFFE bundle source type %q", source.Type())
		}
		registry.bundles[trustDomain] = registration
	}
	return registry, nil
}

// Start waits for every declared trust domain to have authority material for its
// explicitly enabled credential methods before this registry serves lookups. The
// initial readiness wait is limited to 10 seconds, while caller cancellation or
// an earlier caller deadline can bound it further.
func (r *SPIFFEBundleRegistry) Start(ctx context.Context) error {
	if r == nil {
		return nil
	}

	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.closed {
		return errors.New("SPIFFE bundle registry is closed")
	}
	if r.started {
		return nil
	}

	readinessCtx, cancel := context.WithTimeout(ctx, spiffeBundleRegistryReadinessTimeout)
	defer cancel()
	if err := r.start(readinessCtx); err != nil {
		return errors.Join(fmt.Errorf("start SPIFFE bundle registry: %w", err), r.closeLocked())
	}
	r.started = true
	return nil
}

// Close stops all bundle sources. It is safe to call repeatedly.
func (r *SPIFFEBundleRegistry) Close() error {
	if r == nil {
		return nil
	}

	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	return r.closeLocked()
}

// GetX509BundleForTrustDomain returns the X.509 bundle for a declared trust
// domain with X.509 authentication enabled. It rejects undeclared domains before
// consulting a backing source.
func (r *SPIFFEBundleRegistry) GetX509BundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	source, err := r.sourceForTrustDomain(trustDomain, SPIFFEAuthenticationMethodX509)
	if err != nil {
		return nil, err
	}
	return source.GetX509BundleForTrustDomain(trustDomain)
}

// GetJWTBundleForTrustDomain returns the JWT bundle for a declared trust domain
// with JWT authentication enabled. It rejects undeclared domains before
// consulting a backing source.
func (r *SPIFFEBundleRegistry) GetJWTBundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*jwtbundle.Bundle, error) {
	source, err := r.sourceForTrustDomain(trustDomain, SPIFFEAuthenticationMethodJWT)
	if err != nil {
		return nil, err
	}
	return source.GetJWTBundleForTrustDomain(trustDomain)
}

func (r *SPIFFEBundleRegistry) start(ctx context.Context) error {
	if r.hasWorkloadAPI {
		workloadCtx, workloadCancel := context.WithCancel(context.Background())
		result := make(chan struct {
			source *workloadapi.BundleSource
			err    error
		}, 1)
		go func() {
			source, err := workloadapi.NewBundleSource(workloadCtx)
			result <- struct {
				source *workloadapi.BundleSource
				err    error
			}{source: source, err: err}
		}()

		select {
		case workloadResult := <-result:
			if workloadResult.err != nil {
				workloadCancel()
				return fmt.Errorf("create SPIFFE Workload API bundle source: %w", workloadResult.err)
			}
			r.workloadSource = workloadResult.source
			r.workloadCancel = workloadCancel
			for trustDomain, registration := range r.bundles {
				if registration.source == nil {
					registration.source = workloadResult.source
					r.bundles[trustDomain] = registration
				}
			}
		case <-ctx.Done():
			workloadCancel()
			workloadResult := <-result
			if workloadResult.source != nil {
				_ = workloadResult.source.Close()
			}
			return ctx.Err()
		}
	}
	for _, source := range r.endpointSources {
		if err := source.start(ctx); err != nil {
			return fmt.Errorf("start SPIFFE bundle endpoint source for trust domain %q: %w", source.trustDomain, err)
		}
	}
	return r.ensureReadiness()
}

func (r *SPIFFEBundleRegistry) ensureReadiness() error {
	for trustDomain, registration := range r.bundles {
		if _, enabled := registration.methods[SPIFFEAuthenticationMethodX509]; enabled {
			bundle, err := registration.source.GetX509BundleForTrustDomain(trustDomain)
			if err != nil {
				return fmt.Errorf("SPIFFE X.509 bundle for trust domain %q is not ready: %w", trustDomain, err)
			}
			if len(bundle.X509Authorities()) == 0 {
				return fmt.Errorf("SPIFFE X.509 bundle for trust domain %q is not ready: no X.509 authorities", trustDomain)
			}
		}
		if _, enabled := registration.methods[SPIFFEAuthenticationMethodJWT]; enabled {
			bundle, err := registration.source.GetJWTBundleForTrustDomain(trustDomain)
			if err != nil {
				return fmt.Errorf("SPIFFE JWT bundle for trust domain %q is not ready: %w", trustDomain, err)
			}
			if len(bundle.JWTAuthorities()) == 0 {
				return fmt.Errorf("SPIFFE JWT bundle for trust domain %q is not ready: no JWT authorities", trustDomain)
			}
		}
	}
	return nil
}

func (r *SPIFFEBundleRegistry) closeLocked() error {
	if r.closed {
		return nil
	}
	r.closed = true

	var errs []error
	for _, source := range r.endpointSources {
		source.close()
	}
	if r.workloadCancel != nil {
		r.workloadCancel()
	}
	if r.workloadSource != nil {
		errs = append(errs, r.workloadSource.Close())
	}
	return errors.Join(errs...)
}

func (r *SPIFFEBundleRegistry) sourceForTrustDomain(
	trustDomain spiffeid.TrustDomain,
	method SPIFFEAuthenticationMethod,
) (spiffeBundleSource, error) {
	if r == nil {
		return nil, errors.New("no SPIFFE bundle registry is configured")
	}
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if !r.started || r.closed {
		return nil, errors.New("SPIFFE bundle registry is not started")
	}
	registration, ok := r.bundles[trustDomain]
	if !ok {
		return nil, fmt.Errorf("no SPIFFE bundle configured for trust domain %q", trustDomain)
	}
	if _, enabled := registration.methods[method]; !enabled {
		return nil, fmt.Errorf("SPIFFE authentication method %q is not enabled for trust domain %q", method, trustDomain)
	}
	return registration.source, nil
}

func (s endpointBundleSource) GetX509BundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*x509bundle.Bundle, error) {
	bundle, err := s.source.GetBundleForTrustDomain(trustDomain)
	if err != nil {
		return nil, err
	}
	return bundle.X509Bundle(), nil
}

func (s endpointBundleSource) GetJWTBundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*jwtbundle.Bundle, error) {
	bundle, err := s.source.GetBundleForTrustDomain(trustDomain)
	if err != nil {
		return nil, err
	}
	return bundle.JWTBundle(), nil
}

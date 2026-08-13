// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/spiffebundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"

	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	spiffeBundleEndpointTimeout      = 10 * time.Second
	spiffeBundleEndpointMaxBodyBytes = 1 << 20

	// Bundle endpoint refresh hints are advisory. These bounds avoid both a
	// busy-loop from a bad document and an excessively stale refresh cadence.
	spiffeBundleEndpointDefaultRefresh = 5 * time.Minute
	spiffeBundleEndpointMinRefresh     = time.Minute
	spiffeBundleEndpointMaxRefresh     = time.Hour

	spiffeBundleEndpointInitialBackoff = 5 * time.Second
	spiffeBundleEndpointMaxBackoff     = 5 * time.Minute
)

var (
	errSPIFFEBundleUnavailable  = errors.New("SPIFFE bundle is unavailable")
	errSPIFFEBundleSourceClosed = errors.New("SPIFFE bundle endpoint source is closed")
)

// spiffeBundleEndpointSource fetches and atomically replaces the bundle for
// one configured trust domain. Last-known-good material remains available until
// the source closes; authority expiry governs validity. TODO: add a configurable
// maximum staleness policy when deployments need one.
type spiffeBundleEndpointSource struct {
	trustDomain spiffeid.TrustDomain
	methods     map[SPIFFEAuthenticationMethod]struct{}
	endpoint    url.URL
	client      *http.Client

	bundle atomic.Pointer[spiffebundle.Bundle]

	lifecycleMu sync.RWMutex
	// Values captured from time.Now retain monotonic clock readings, so wall-clock
	// rollbacks cannot affect refresh diagnostics.
	lastSuccess time.Time
	closed      bool
	cancel      context.CancelFunc
	done        chan struct{}
}

var _ spiffebundle.Source = (*spiffeBundleEndpointSource)(nil)

func newSPIFFEBundleEndpointSource(
	trustDomain spiffeid.TrustDomain,
	methods []SPIFFEAuthenticationMethod,
	endpoint string,
) (*spiffeBundleEndpointSource, error) {
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("parse SPIFFE bundle endpoint")
	}
	if parsedEndpoint.Scheme != networking.HttpsScheme || parsedEndpoint.Host == "" {
		return nil, errors.New("SPIFFE bundle endpoint must be an absolute HTTPS URL")
	}
	if parsedEndpoint.User != nil || parsedEndpoint.RawQuery != "" || parsedEndpoint.Fragment != "" ||
		parsedEndpoint.ForceQuery || strings.Contains(endpoint, "?") || strings.Contains(endpoint, "#") {
		return nil, errors.New("SPIFFE bundle endpoint must not contain credentials, query, or fragment")
	}

	if networking.IsLoopbackHost(parsedEndpoint.Hostname()) {
		return nil, errors.New("SPIFFE bundle endpoint must not use a loopback host")
	}

	client, err := networking.NewHttpClientBuilder().
		WithTimeout(spiffeBundleEndpointTimeout).
		WithDisableKeepAlives(true).
		Build()
	if err != nil {
		return nil, fmt.Errorf("build SPIFFE bundle endpoint client: %w", err)
	}
	client.CheckRedirect = networking.SameHostRedirectPolicy()

	return &spiffeBundleEndpointSource{
		trustDomain: trustDomain,
		methods:     enabledSPIFFEBundleMethods(methods),
		endpoint:    *parsedEndpoint,
		client:      client,
	}, nil
}

// start obtains initial material using ctx before launching a refresh loop with
// a source-owned context. This allows callers to bound startup without their
// readiness context inadvertently stopping later refreshes.
func (s *spiffeBundleEndpointSource) start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return errSPIFFEBundleSourceClosed
	}
	if s.cancel != nil {
		s.lifecycleMu.Unlock()
		return errors.New("SPIFFE bundle endpoint source is already started")
	}
	initialCtx, initialCancel := context.WithCancel(ctx)
	initialDone := make(chan struct{})
	s.cancel = initialCancel
	s.done = initialDone
	s.lifecycleMu.Unlock()

	err := s.refresh(initialCtx)
	initialCancel()
	s.lifecycleMu.Lock()
	if err != nil {
		if s.done == initialDone {
			s.cancel = nil
			s.done = nil
		}
		close(initialDone)
		s.lifecycleMu.Unlock()
		s.logRefresh(slog.Default(), spiffeBundleEndpointInitialBackoff, err)
		return err
	}
	if s.closed {
		s.cancel = nil
		s.done = nil
		close(initialDone)
		s.lifecycleMu.Unlock()
		return errSPIFFEBundleSourceClosed
	}

	interval := s.refreshInterval()
	refreshCtx, refreshCancel := context.WithCancel(context.Background())
	refreshDone := make(chan struct{})
	s.cancel = refreshCancel
	s.done = refreshDone
	close(initialDone)
	s.lifecycleMu.Unlock()
	s.logRefresh(slog.Default(), interval, nil)
	go s.run(refreshCtx, refreshDone, interval)
	return nil
}

// close terminally stops an in-progress initial fetch or refresh loop and waits
// for it. It is safe before start and when called repeatedly.
func (s *spiffeBundleEndpointSource) close() {
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

// GetBundleForTrustDomain returns only this source's configured domain.
func (s *spiffeBundleEndpointSource) GetBundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*spiffebundle.Bundle, error) {
	if trustDomain != s.trustDomain {
		return nil, fmt.Errorf("no SPIFFE bundle configured for trust domain %q", trustDomain)
	}
	bundle := s.bundle.Load()
	if bundle == nil {
		return nil, errSPIFFEBundleUnavailable
	}
	return bundle, nil
}

func (s *spiffeBundleEndpointSource) run(ctx context.Context, done chan struct{}, interval time.Duration) {
	defer func() {
		s.lifecycleMu.Lock()
		if s.done == done {
			s.cancel = nil
		}
		s.lifecycleMu.Unlock()
		close(done)
	}()

	backoff := spiffeBundleEndpointInitialBackoff
	for {
		if err := waitForSPIFFEBundleRefresh(ctx, interval); err != nil {
			return
		}
		if err := s.refresh(ctx); err != nil {
			interval = jitterSPIFFEBundleInterval(backoff)
			s.logRefresh(slog.Default(), interval, err)
			backoff = min(backoff*2, spiffeBundleEndpointMaxBackoff)
			continue
		}
		backoff = spiffeBundleEndpointInitialBackoff
		interval = s.refreshInterval()
		s.logRefresh(slog.Default(), interval, nil)
	}
}

func (s *spiffeBundleEndpointSource) refresh(ctx context.Context) error {
	requestURL := s.endpoint
	query := requestURL.Query()
	query.Set("spiffe_id", "spiffe://"+s.trustDomain.String())
	requestURL.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return errors.New("create SPIFFE bundle endpoint request")
	}
	response, err := s.client.Do(request)
	if err != nil {
		return errors.New("fetch SPIFFE bundle endpoint")
	}
	defer drainAndCloseSPIFFEBundleResponse(response.Body)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("SPIFFE bundle endpoint returned status %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, spiffeBundleEndpointMaxBodyBytes+1))
	if err != nil {
		return errors.New("read SPIFFE bundle endpoint response")
	}
	if len(body) > spiffeBundleEndpointMaxBodyBytes {
		return fmt.Errorf("SPIFFE bundle endpoint response exceeds %d bytes", spiffeBundleEndpointMaxBodyBytes)
	}
	bundle, err := spiffebundle.Read(s.trustDomain, bytes.NewReader(body))
	if err != nil {
		return errors.New("parse SPIFFE bundle endpoint response")
	}
	if err := s.validateBundle(bundle); err != nil {
		return err
	}
	_, err = s.storeBundle(bundle)
	if err != nil {
		return err
	}
	s.lifecycleMu.Lock()
	s.lastSuccess = time.Now()
	s.lifecycleMu.Unlock()
	return nil
}

func drainAndCloseSPIFFEBundleResponse(body io.ReadCloser) {
	// Limit draining so a malicious endpoint cannot keep this request active by
	// streaming an unbounded response before the body is closed.
	_, _ = io.Copy(io.Discard, io.LimitReader(body, spiffeBundleEndpointMaxBodyBytes))
	_ = body.Close()
}

func (s *spiffeBundleEndpointSource) validateBundle(bundle *spiffebundle.Bundle) error {
	if _, enabled := s.methods[SPIFFEAuthenticationMethodX509]; enabled && len(bundle.X509Authorities()) == 0 {
		return errors.New("SPIFFE bundle endpoint response has no X.509 authorities for an enabled method")
	}
	if _, enabled := s.methods[SPIFFEAuthenticationMethodJWT]; enabled && len(bundle.JWTAuthorities()) == 0 {
		return errors.New("SPIFFE bundle endpoint response has no JWT authorities for an enabled method")
	}
	return nil
}

func enabledSPIFFEBundleMethods(methods []SPIFFEAuthenticationMethod) map[SPIFFEAuthenticationMethod]struct{} {
	enabled := make(map[SPIFFEAuthenticationMethod]struct{}, len(methods))
	for _, method := range methods {
		enabled[method] = struct{}{}
	}
	return enabled
}

func (s *spiffeBundleEndpointSource) storeBundle(next *spiffebundle.Bundle) (bool, error) {
	for {
		current := s.bundle.Load()
		if current == nil {
			if s.bundle.CompareAndSwap(nil, next) {
				return true, nil
			}
			continue
		}
		if sequenceRollback(current, next) {
			return false, errors.New("SPIFFE bundle endpoint sequence number regressed")
		}
		if sequenceEqual(current, next) {
			return false, nil
		}
		if s.bundle.CompareAndSwap(current, next) {
			return true, nil
		}
	}
}

// A missing sequence is older than a present sequence. Once a publisher starts
// sequencing bundles it cannot roll back to an unsequenced document. Two
// unsequenced documents replace each other; equal sequence numbers retain the
// existing whole bundle.
func sequenceRollback(current, next *spiffebundle.Bundle) bool {
	currentSequence, currentOK := current.SequenceNumber()
	nextSequence, nextOK := next.SequenceNumber()
	return currentOK && (!nextOK || nextSequence < currentSequence)
}

func sequenceEqual(current, next *spiffebundle.Bundle) bool {
	currentSequence, currentOK := current.SequenceNumber()
	nextSequence, nextOK := next.SequenceNumber()
	return currentOK && nextOK && currentSequence == nextSequence
}

func (s *spiffeBundleEndpointSource) refreshInterval() time.Duration {
	bundle := s.bundle.Load()
	if bundle == nil {
		return jitterSPIFFEBundleInterval(spiffeBundleEndpointDefaultRefresh)
	}
	hint, ok := bundle.RefreshHint()
	if !ok {
		hint = spiffeBundleEndpointDefaultRefresh
	}
	return jitterSPIFFEBundleInterval(min(max(hint, spiffeBundleEndpointMinRefresh), spiffeBundleEndpointMaxRefresh))
}

func (s *spiffeBundleEndpointSource) lastSuccessAge() time.Duration {
	s.lifecycleMu.RLock()
	defer s.lifecycleMu.RUnlock()
	if s.lastSuccess.IsZero() {
		return 0
	}
	return time.Since(s.lastSuccess)
}

func (s *spiffeBundleEndpointSource) logRefresh(logger *slog.Logger, nextRefreshInterval time.Duration, err error) {
	bundle := s.bundle.Load()
	var sequence uint64
	var sequencePresent bool
	var x509AuthorityCount, jwtAuthorityCount int
	if bundle != nil {
		sequence, sequencePresent = bundle.SequenceNumber()
		x509AuthorityCount = len(bundle.X509Authorities())
		jwtAuthorityCount = len(bundle.JWTAuthorities())
	}

	args := []any{
		"trust_domain", s.trustDomain.String(),
		"source_type", "bundle_endpoint",
		"sequence_present", sequencePresent,
		"sequence", sequence,
		"x509_authority_count", x509AuthorityCount,
		"jwt_authority_count", jwtAuthorityCount,
		"next_refresh_interval", nextRefreshInterval,
		"last_success_age", s.lastSuccessAge(),
	}
	if err != nil {
		args = append(args, "error_class", spiffeBundleEndpointErrorClass(err))
		logger.Warn("SPIFFE bundle endpoint refresh failed", args...)
		return
	}
	logger.Debug("SPIFFE bundle endpoint refresh succeeded", args...)
}

func waitForSPIFFEBundleRefresh(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func jitterSPIFFEBundleInterval(interval time.Duration) time.Duration {
	jitter, err := cryptorand.Int(cryptorand.Reader, big.NewInt(2001))
	if err != nil {
		return interval
	}
	return interval * time.Duration(9000+jitter.Int64()) / 10000
}

func spiffeBundleEndpointErrorClass(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "transport"
	}
	return "fetch"
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// dcrRecheckTimeout bounds one re-check: a cache read, one probe request
// and, when the probe rejects the client, one registration.
const dcrRecheckTimeout = time.Minute

// dcrRecheck repeats an upstream's start-time DCR resolution while the
// server runs (DCRUpstreamConfig.RegistrationProbeInterval). Each tick calls
// dcr.ResolveCredentials, which re-reads the cached row and checks it with the
// configured probe, replaces a rejected registration and registers a missing
// one. When the result names a different client than the one in use, the
// upstream provider switches to it in place.
type dcrRecheck struct {
	upstream string
	req      *dcr.Request
	store    dcr.CredentialStore
	interval time.Duration
	// initialAccessTokenFile, when set, is re-read on every tick so a
	// registration uses the token currently mounted rather than the one
	// read at start, which a single-use or rotated token would make stale.
	initialAccessTokenFile string
	// adoptAuthorizationEndpoint and adoptTokenEndpoint are set where the
	// run config left the endpoint empty, so the provider follows the
	// endpoints of the registration in use, as consumeResolution does at
	// start.
	adoptAuthorizationEndpoint bool
	adoptTokenEndpoint         bool
	// current is the resolution the provider presents; only the loop
	// goroutine touches it after construction.
	current *dcr.Resolution
}

// newDCRRecheck returns the re-check for rc, or nil when rc has no
// RegistrationProbeInterval or no DCR resolution happened.
func newDCRRecheck(
	rc *authserver.UpstreamRunConfig, req *dcr.Request, resolution *dcr.Resolution, store dcr.CredentialStore,
) (*dcrRecheck, error) {
	if req == nil || resolution == nil {
		return nil, nil
	}
	var (
		dcrCfg                     *authserver.DCRUpstreamConfig
		adoptAuthorize, adoptToken bool
	)
	switch {
	case rc.OAuth2Config != nil:
		dcrCfg = rc.OAuth2Config.DCRConfig
		adoptAuthorize = rc.OAuth2Config.AuthorizationEndpoint == ""
		adoptToken = rc.OAuth2Config.TokenEndpoint == ""
	case rc.OIDCConfig != nil:
		dcrCfg = rc.OIDCConfig.DCRConfig
	}
	if dcrCfg == nil {
		return nil, nil
	}
	interval, err := dcrCfg.ProbeInterval()
	if err != nil || interval == 0 {
		return nil, err
	}
	periodic := *req
	periodic.Periodic = true
	return &dcrRecheck{
		upstream:                   rc.Name,
		req:                        &periodic,
		store:                      store,
		interval:                   interval,
		initialAccessTokenFile:     dcrCfg.InitialAccessTokenFile,
		adoptAuthorizationEndpoint: adoptAuthorize,
		adoptTokenEndpoint:         adoptToken,
		current:                    resolution,
	}, nil
}

// startDCRRechecks runs every re-check until the returned stop function is
// called; stop waits for in-flight ticks to return.
func startDCRRechecks(server authserver.Server, rechecks []*dcrRecheck) (stop func()) {
	if len(rechecks) == 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, r := range rechecks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.run(ctx, server)
		}()
	}
	return func() {
		cancel()
		wg.Wait()
	}
}

// run ticks until ctx is cancelled. Replicas of one rollout start together,
// so the first tick waits a random part of the interval and each later one
// the interval ±10%; otherwise every replica probes, and on a rejection
// registers a new client, at the same moment.
func (r *dcrRecheck) run(ctx context.Context, server authserver.Server) {
	timer := time.NewTimer(rand.N(r.interval)) //nolint:gosec // G404: scheduling jitter, not a secret
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			r.tick(ctx, server)
			timer.Reset(jitterInterval(r.interval))
		}
	}
}

// jitterInterval returns d varied uniformly by up to ±10%.
func jitterInterval(d time.Duration) time.Duration {
	spread := d / 5
	if spread <= 0 {
		return d
	}
	return d - spread/2 + rand.N(spread) //nolint:gosec // G404: scheduling jitter, not a secret
}

// tick runs one re-check. Failures are logged and leave the provider on its
// current client; the next tick tries again.
func (r *dcrRecheck) tick(ctx context.Context, server authserver.Server) {
	ctx, cancel := context.WithTimeout(ctx, dcrRecheckTimeout)
	defer cancel()

	req := r.req
	if r.initialAccessTokenFile != "" {
		token, err := resolveSecret(r.initialAccessTokenFile, "")
		if err != nil {
			slog.Error("dcr: could not re-read the initial access token",
				"upstream", authserver.ResolveUpstreamName(r.upstream), "error", err)
			return
		}
		fresh := *r.req
		fresh.InitialAccessToken = token
		req = &fresh
	}

	resolution, err := dcr.ResolveCredentials(ctx, req, r.store)
	if err != nil {
		// A cancelled context is the server shutting down, not a failure.
		if !errors.Is(ctx.Err(), context.Canceled) {
			dcr.LogStepError(r.upstream, err)
		}
		return
	}
	creds := r.credentials(resolution)
	if creds == r.credentials(r.current) {
		return
	}
	if err := authserver.UpdateUpstreamClientCredentials(server, r.upstream, creds); err != nil {
		slog.Error("dcr: could not switch the upstream to its current client registration",
			"upstream", authserver.ResolveUpstreamName(r.upstream), "error", err)
		return
	}
	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Warn("dcr: switched the upstream to its current client registration; "+
		"authorizations started under the previous client may fail their code exchange",
		"upstream", authserver.ResolveUpstreamName(r.upstream),
		"previous_client_id", r.current.ClientID,
		"client_id", resolution.ClientID,
	)
	r.current = resolution
}

// credentials is what the provider presents for res: its client, and its
// endpoints where the run config left them to the registration.
func (r *dcrRecheck) credentials(res *dcr.Resolution) upstream.ClientCredentials {
	creds := upstream.ClientCredentials{
		ClientID:                res.ClientID,
		ClientSecret:            res.ClientSecret,
		TokenEndpointAuthMethod: res.TokenEndpointAuthMethod,
	}
	if r.adoptAuthorizationEndpoint {
		creds.AuthorizationEndpoint = res.AuthorizationEndpoint
	}
	if r.adoptTokenEndpoint {
		creds.TokenEndpoint = res.TokenEndpoint
	}
	return creds
}

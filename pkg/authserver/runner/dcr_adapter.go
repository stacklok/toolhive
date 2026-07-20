// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// This file contains the embedded-authserver-specific adapter layer between
// pkg/auth/dcr's profile-neutral API and the authserver's domain types
// (authserver.OAuth2UpstreamRunConfig, upstream.OAuth2Config). Each helper
// here:
//
//   - needsDCR reports whether an OAuth2UpstreamRunConfig requires DCR.
//   - newDCRRequest converts an OAuth2UpstreamRunConfig into the
//     profile-neutral dcr.Request the resolver expects, including resolving
//     the file-or-env InitialAccessToken into an inline string.
//   - consumeResolution / applyResolutionToOAuth2Config fold the returned
//     dcr.Resolution back into the run-config and built OAuth2Config.
//     Both are value-in / value-out: the caller's original is never
//     observably mutated, and the no-mutation contract is compile-time
//     enforced by the signatures rather than a prose discipline.
//
// Keeping these helpers in the runner package (not pkg/auth/dcr) is the
// architectural split the dcr package's profile-agnostic charter requires:
// dcr cannot import authserver, but the embedded authserver freely depends
// on dcr.

// needsDCR reports whether rc requires runtime Dynamic Client Registration.
// A run-config needs DCR exactly when ClientID is empty and DCRConfig is
// non-nil (the mutually-exclusive constraint is enforced by
// OAuth2UpstreamRunConfig.Validate; this helper is a convenience check).
func needsDCR(rc *authserver.OAuth2UpstreamRunConfig) bool {
	if rc == nil {
		return false
	}
	return rc.ClientID == "" && rc.DCRConfig != nil
}

// newDCRRequest builds the profile-neutral dcr.Request from an
// OAuth2UpstreamRunConfig and this auth server's local issuer. The caller
// has already validated the run-config (Validate() enforces
// ClientID xor DCRConfig and the DCRConfig-internal one-of constraint), so
// this function does not re-check those invariants — it only resolves the
// file-or-env InitialAccessToken reference into an inline string.
//
// localIssuer is *this* auth server's issuer identifier, used by the
// resolver for cache keying and to default the redirect URI to
// {localIssuer}/oauth/callback when rc.RedirectURI is empty. The upstream
// issuer is recovered separately from rc.DCRConfig.DiscoveryURL inside the
// resolver and is used solely for RFC 8414 §3.3 metadata verification.
func newDCRRequest(rc *authserver.OAuth2UpstreamRunConfig, localIssuer string) (*dcr.Request, error) {
	if rc == nil {
		return nil, fmt.Errorf("oauth2 upstream run-config is required")
	}
	if rc.DCRConfig == nil {
		return nil, fmt.Errorf("dcr: oauth2 upstream has no dcr_config")
	}

	initialAccessToken, err := resolveSecret(
		rc.DCRConfig.InitialAccessTokenFile,
		rc.DCRConfig.InitialAccessTokenEnvVar,
	)
	if err != nil {
		return nil, fmt.Errorf("dcr: resolve initial access token: %w", err)
	}

	return &dcr.Request{
		Issuer:                localIssuer,
		RedirectURI:           rc.RedirectURI,
		Scopes:                rc.Scopes,
		DiscoveryURL:          rc.DCRConfig.DiscoveryURL,
		RegistrationEndpoint:  rc.DCRConfig.RegistrationEndpoint,
		AuthorizationEndpoint: rc.AuthorizationEndpoint,
		TokenEndpoint:         rc.TokenEndpoint,
		InitialAccessToken:    initialAccessToken,
		// Reuse the upstream's private-IP policy so the DCR discovery and
		// registration calls share the same SSRF posture as its token and
		// userinfo calls (see upstream.OAuth2Config.AllowPrivateIPs).
		AllowPrivateIPs: rc.AllowPrivateIPs,
	}, nil
}

// consumeResolution returns a copy of rc with the resolved credentials and
// endpoints from res copied in and DCRConfig consumed (set to nil),
// transitioning the run-config from "DCR-pending" (ClientID == "" &&
// DCRConfig != nil) to "DCR-resolved" (ClientID populated && DCRConfig
// == nil). The "consume" name is deliberate: a second call on the
// returned value is a no-op only because the first cleared DCRConfig —
// this is a one-shot state transition, not an idempotent default-fill.
//
// rc is taken by value and the modified copy is returned. The caller's
// original is never observably mutated; the value-in / value-out shape
// makes the no-mutation contract compile-time enforced rather than a
// prose discipline the caller is required to remember. Pointer-typed
// fields (DCRConfig) share storage with the caller's copy via the struct
// shallow-copy, but the only mutation here is to assign nil to the
// copy's DCRConfig — nil-assignment to the local field does not reach
// back through the original pointer.
//
// Why DCRConfig is cleared: OAuth2UpstreamRunConfig.Validate enforces
// ClientID xor DCRConfig — a resolved copy that left DCRConfig set would
// fail the validator that runs downstream in buildPureOAuth2Config.
//
// ClientID, the endpoints, and RedirectURI are written only when rc leaves
// them empty — explicit caller configuration always wins. The conditional
// ClientID write is defence-in-depth against future call sites that bypass
// the needsDCR precondition; an unconditional overwrite would silently
// clobber a pre-provisioned ClientID with no error.
//
// The defaulted RedirectURI write closes the loop on resolver-side defaulting:
// when the caller's run-config left RedirectURI empty, the resolver derived
// issuer + /oauth/callback and persisted it on the resolution; copying it
// back here means the downstream upstream.OAuth2Config has a non-empty
// RedirectURI, which authserver.Config validation requires.
//
// Note on ClientSecret: consumeResolution does NOT write the resolved
// secret because OAuth2UpstreamRunConfig models secrets as file-or-env
// references only. To propagate the DCR-resolved secret into the final
// upstream.OAuth2Config, callers must pair this call with
// applyResolutionToOAuth2Config once the config has been built. Keeping
// the two helpers side-by-side localises the DCR-specific application
// logic.
func consumeResolution(rc authserver.OAuth2UpstreamRunConfig, res *dcr.Resolution) authserver.OAuth2UpstreamRunConfig {
	if res == nil {
		return rc
	}
	if rc.ClientID == "" {
		rc.ClientID = res.ClientID
	}
	rc.DCRConfig = nil
	if rc.AuthorizationEndpoint == "" {
		rc.AuthorizationEndpoint = res.AuthorizationEndpoint
	}
	if rc.TokenEndpoint == "" {
		rc.TokenEndpoint = res.TokenEndpoint
	}
	if rc.RedirectURI == "" {
		rc.RedirectURI = res.RedirectURI
	}
	return rc
}

// applyResolutionToOAuth2Config returns a copy of cfg with the DCR-
// resolved ClientSecret and TokenEndpointAuthMethod overlaid onto it. This
// is the companion to consumeResolution: where that function writes fields
// representable in the file-or-env run-config model, this one writes the
// inline-only fields directly on the runtime config.
//
// cfg is taken by value and the modified copy is returned, mirroring
// consumeResolution. The no-mutation contract is compile-time enforced
// by the signature rather than a prose discipline.
//
// TokenEndpointAuthMethod carries the method the upstream negotiated during
// registration (RFC 7591); see upstream.authStyleFromMethod for why it matters.
//
// The split exists because buildPureOAuth2Config intentionally retains a
// narrow file-or-env contract (no DCR awareness) and because OAuth2's
// ClientSecret on the run-config is modelled as a reference rather than
// an inline string. Any future output path from OAuth2UpstreamRunConfig
// to upstream.OAuth2Config must call BOTH consumeResolution (run-config
// side) AND applyResolutionToOAuth2Config (built-config side) to get a
// fully-resolved DCR client. Forgetting the second call leaves both
// ClientSecret and TokenEndpointAuthMethod empty, producing silent auth
// failures at request time — a client that negotiated client_secret_basic
// reverts to POST-body credentials and is rejected. The type system does
// not enforce the pair, so the invariant lives here.
func applyResolutionToOAuth2Config(cfg upstream.OAuth2Config, res *dcr.Resolution) upstream.OAuth2Config {
	if res == nil {
		return cfg
	}
	cfg.ClientSecret = res.ClientSecret
	cfg.TokenEndpointAuthMethod = res.TokenEndpointAuthMethod
	return cfg
}

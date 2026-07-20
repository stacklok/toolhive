// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// Tests in this file pin the embedded-authserver-side adapter that bridges
// pkg/auth/dcr's profile-neutral API to authserver-specific domain types.
// They were lifted from pkg/auth/dcr's resolver_test.go when sub-issue 4b
// neutralised the resolver inputs — the helpers moved to runner/dcr_adapter.go
// so the dcr package no longer imports authserver types, and the tests
// follow.

func TestNeedsDCR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rc       *authserver.OAuth2UpstreamRunConfig
		expected bool
	}{
		{name: "nil", rc: nil, expected: false},
		{name: "empty client_id and dcr_config", rc: &authserver.OAuth2UpstreamRunConfig{
			DCRConfig: &authserver.DCRUpstreamConfig{},
		}, expected: true},
		{name: "client_id without dcr", rc: &authserver.OAuth2UpstreamRunConfig{
			ClientID: "x",
		}, expected: false},
		{name: "client_id wins over dcr_config (defensive AND semantic)", rc: &authserver.OAuth2UpstreamRunConfig{
			ClientID:  "x",
			DCRConfig: &authserver.DCRUpstreamConfig{},
		}, expected: false},
		{name: "both empty", rc: &authserver.OAuth2UpstreamRunConfig{}, expected: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, needsDCR(tc.rc))
		})
	}
}

func TestConsumeResolution_RespectsExplicitEndpoints(t *testing.T) {
	t.Parallel()

	rc := authserver.OAuth2UpstreamRunConfig{
		AuthorizationEndpoint: "https://explicit/authorize",
		TokenEndpoint:         "https://explicit/token",
	}
	res := &dcr.Resolution{
		ClientID:              "got-client",
		AuthorizationEndpoint: "https://discovered/authorize",
		TokenEndpoint:         "https://discovered/token",
	}
	rc = consumeResolution(rc, res)
	assert.Equal(t, "got-client", rc.ClientID)
	assert.Equal(t, "https://explicit/authorize", rc.AuthorizationEndpoint)
	assert.Equal(t, "https://explicit/token", rc.TokenEndpoint)
}

func TestConsumeResolution_FillsMissingEndpoints(t *testing.T) {
	t.Parallel()

	rc := authserver.OAuth2UpstreamRunConfig{}
	res := &dcr.Resolution{
		ClientID:              "got-client",
		AuthorizationEndpoint: "https://discovered/authorize",
		TokenEndpoint:         "https://discovered/token",
	}
	rc = consumeResolution(rc, res)
	assert.Equal(t, "got-client", rc.ClientID)
	assert.Equal(t, "https://discovered/authorize", rc.AuthorizationEndpoint)
	assert.Equal(t, "https://discovered/token", rc.TokenEndpoint)
}

// TestConsumeResolution_ClearsDCRConfig pins the contract that
// consumeResolution clears DCRConfig on the returned run-config copy
// after writing the resolved ClientID. Without this,
// OAuth2UpstreamRunConfig.Validate (run by buildPureOAuth2Config
// downstream) trips its ClientID-xor-DCRConfig rule on the resolved copy
// and rejects the upstream at boot.
func TestConsumeResolution_ClearsDCRConfig(t *testing.T) {
	t.Parallel()

	rc := authserver.OAuth2UpstreamRunConfig{
		DCRConfig: &authserver.DCRUpstreamConfig{
			RegistrationEndpoint: "https://idp.example.com/register",
		},
	}
	res := &dcr.Resolution{
		ClientID: "dcr-issued-client",
	}

	rc = consumeResolution(rc, res)

	assert.Equal(t, "dcr-issued-client", rc.ClientID)
	assert.Nil(t, rc.DCRConfig,
		"consumeResolution must clear DCRConfig so the resolved copy satisfies the ClientID-xor-DCRConfig invariant")
}

// TestConsumeResolution_DoesNotOverwritePreProvisionedClientID verifies
// the defence-in-depth in consumeResolution: a caller that bypasses
// needsDCR and invokes consumeResolution directly with a pre-provisioned
// ClientID does not have it silently clobbered.
func TestConsumeResolution_DoesNotOverwritePreProvisionedClientID(t *testing.T) {
	t.Parallel()

	rc := authserver.OAuth2UpstreamRunConfig{
		ClientID: "pre-provisioned",
	}
	res := &dcr.Resolution{
		ClientID: "would-be-overwrite",
	}
	rc = consumeResolution(rc, res)
	assert.Equal(t, "pre-provisioned", rc.ClientID,
		"consumeResolution must not overwrite a non-empty ClientID")
}

// TestConsumeResolution_NilResolutionIsNoop verifies that passing a nil
// resolution returns the input unchanged, mirroring the resolver-side
// convention. This avoids forcing every call site to nil-check before
// invocation when DCR was not required.
func TestConsumeResolution_NilResolutionIsNoop(t *testing.T) {
	t.Parallel()

	rc := authserver.OAuth2UpstreamRunConfig{
		ClientID: "pre-provisioned",
	}
	out := consumeResolution(rc, nil)
	assert.Equal(t, rc, out)
}

// TestConsumeResolution_DoesNotMutateCaller verifies the value-in /
// value-out contract: the caller's local variable is not observably
// mutated after the call, even when the function clears DCRConfig on the
// returned copy.
func TestConsumeResolution_DoesNotMutateCaller(t *testing.T) {
	t.Parallel()

	original := authserver.OAuth2UpstreamRunConfig{
		DCRConfig: &authserver.DCRUpstreamConfig{
			RegistrationEndpoint: "https://idp.example.com/register",
		},
	}
	res := &dcr.Resolution{ClientID: "dcr-issued"}

	out := consumeResolution(original, res)

	assert.Equal(t, "", original.ClientID, "caller's ClientID must remain empty")
	assert.NotNil(t, original.DCRConfig, "caller's DCRConfig must remain set")
	assert.Equal(t, "dcr-issued", out.ClientID)
	assert.Nil(t, out.DCRConfig)
}

// TestApplyResolutionToOAuth2Config covers the ClientSecret overlay onto
// the built upstream.OAuth2Config. The split between consumeResolution
// (run-config fields) and applyResolutionToOAuth2Config (inline-only
// ClientSecret) is documented in dcr_adapter.go; both must be paired to
// produce a fully-resolved DCR client.
func TestApplyResolutionToOAuth2Config(t *testing.T) {
	t.Parallel()

	cfg := upstream.OAuth2Config{}
	res := &dcr.Resolution{
		ClientSecret:            "dcr-issued-secret",
		TokenEndpointAuthMethod: "client_secret_basic",
	}
	out := applyResolutionToOAuth2Config(cfg, res)
	assert.Equal(t, "dcr-issued-secret", out.ClientSecret)
	// The negotiated auth method must be carried through so newBaseOAuth2Provider
	// presents credentials the way the upstream registered the client (issue #5865).
	assert.Equal(t, "client_secret_basic", out.TokenEndpointAuthMethod)
	assert.Equal(t, "", cfg.ClientSecret, "caller's cfg must not be mutated")
	assert.Equal(t, "", cfg.TokenEndpointAuthMethod, "caller's cfg must not be mutated")

	// nil resolution is a no-op rather than a crash.
	unchanged := applyResolutionToOAuth2Config(cfg, nil)
	assert.Equal(t, cfg, unchanged)
}

// TestConsumeResolution_AloneLeavesClientSecretEmpty is a tripwire
// regression test for the two-call invariant documented on
// applyResolutionToOAuth2Config. The contract is:
//
//	consumeResolution writes the run-config fields representable in the
//	file-or-env model; applyResolutionToOAuth2Config writes the
//	inline-only ClientSecret onto the built upstream.OAuth2Config.
//
// Both must be paired — calling only consumeResolution leaves
// ClientSecret empty and produces silent auth failures at request time.
// The type system does not enforce the pair, so this test pins the
// failure mode: a future refactor that accidentally folds the two helpers
// in a way that drops the ClientSecret write will fail here with a
// clear signal rather than at upstream auth time on a deployed binary.
func TestConsumeResolution_AloneLeavesClientSecretEmpty(t *testing.T) {
	t.Parallel()

	rc := authserver.OAuth2UpstreamRunConfig{
		DCRConfig: &authserver.DCRUpstreamConfig{
			RegistrationEndpoint: "https://idp.example.com/register",
		},
	}
	res := &dcr.Resolution{
		ClientID:     "dcr-issued-client",
		ClientSecret: "dcr-issued-secret",
	}

	rc = consumeResolution(rc, res)

	// run-config side fields are populated.
	assert.Equal(t, "dcr-issued-client", rc.ClientID)
	// But the ClientSecret destination — the built upstream.OAuth2Config —
	// is untouched. The default-constructed OAuth2Config below stands in
	// for "the built config that buildPureOAuth2Config produces": this
	// path does NOT include applyResolutionToOAuth2Config, so the
	// ClientSecret remains empty. A future regression that hides the
	// applyResolutionToOAuth2Config call inside consumeResolution would
	// flip this assertion.
	builtCfg := upstream.OAuth2Config{}
	assert.Equal(t, "", builtCfg.ClientSecret,
		"consumeResolution alone must not write ClientSecret; that is "+
			"applyResolutionToOAuth2Config's responsibility (see dcr_adapter.go)")
}

// TestNewDCRRequest covers the OAuth2UpstreamRunConfig → dcr.Request
// translation, including the file-based InitialAccessToken resolution that
// previously lived inside the resolver.
func TestNewDCRRequest(t *testing.T) {
	t.Parallel()

	tokenPath := filepath.Join(t.TempDir(), "iat")
	require.NoError(t, os.WriteFile(tokenPath, []byte("iat-value\n"), 0o600))

	tests := []struct {
		name                   string
		rc                     *authserver.OAuth2UpstreamRunConfig
		localIssuer            string
		wantErrSub             string
		wantIssuer             string
		wantInitialAccessToken string
		wantDiscoveryURL       string
		wantRegistration       string
		wantAllowPrivateIPs    bool
	}{
		{
			name: "discovery_url branch resolves file-based initial access token",
			rc: &authserver.OAuth2UpstreamRunConfig{
				Scopes: []string{"openid"},
				DCRConfig: &authserver.DCRUpstreamConfig{
					DiscoveryURL:           "https://idp.example.com/.well-known/oauth-authorization-server",
					InitialAccessTokenFile: tokenPath,
				},
			},
			localIssuer:            "https://thv.example.com",
			wantIssuer:             "https://thv.example.com",
			wantInitialAccessToken: "iat-value",
			wantDiscoveryURL:       "https://idp.example.com/.well-known/oauth-authorization-server",
		},
		{
			name: "registration_endpoint branch propagates without secret",
			rc: &authserver.OAuth2UpstreamRunConfig{
				Scopes: []string{"openid"},
				DCRConfig: &authserver.DCRUpstreamConfig{
					RegistrationEndpoint: "https://idp.example.com/register",
				},
			},
			localIssuer:      "https://thv.example.com",
			wantIssuer:       "https://thv.example.com",
			wantRegistration: "https://idp.example.com/register",
		},
		{
			// Pins the AllowPrivateIPs one-line copy from
			// OAuth2UpstreamRunConfig onto dcr.Request: without this test, a
			// future refactor could silently drop or invert the SSRF-guard
			// decision without any test failing.
			name: "AllowPrivateIPs propagates from run-config",
			rc: &authserver.OAuth2UpstreamRunConfig{
				Scopes: []string{"openid"},
				DCRConfig: &authserver.DCRUpstreamConfig{
					RegistrationEndpoint: "https://idp.example.com/register",
				},
				AllowPrivateIPs: true,
			},
			localIssuer:         "https://thv.example.com",
			wantIssuer:          "https://thv.example.com",
			wantRegistration:    "https://idp.example.com/register",
			wantAllowPrivateIPs: true,
		},
		{
			name:        "nil run-config rejected",
			rc:          nil,
			localIssuer: "https://thv.example.com",
			wantErrSub:  "run-config is required",
		},
		{
			name:        "missing dcr_config rejected",
			rc:          &authserver.OAuth2UpstreamRunConfig{},
			localIssuer: "https://thv.example.com",
			wantErrSub:  "no dcr_config",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, err := newDCRRequest(tc.rc, tc.localIssuer)
			if tc.wantErrSub != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSub)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, req)
			assert.Equal(t, tc.wantIssuer, req.Issuer)
			assert.Equal(t, tc.wantInitialAccessToken, req.InitialAccessToken)
			assert.Equal(t, tc.wantDiscoveryURL, req.DiscoveryURL)
			assert.Equal(t, tc.wantRegistration, req.RegistrationEndpoint)
			assert.Equal(t, tc.wantAllowPrivateIPs, req.AllowPrivateIPs)
		})
	}
}

// TestNewDCRRequest_EnvVarInitialAccessToken pins the env-var sibling of
// the file-based InitialAccessToken branch covered above. Without this,
// a refactor that swapped the argument order on resolveSecret or read
// the wrong field on DCRConfig would slip past the file-based test and
// only fail in production, where the env-var path is exercised by
// operators who pass credentials via Kubernetes Secrets / env injection.
// Kept as a standalone test (not a table case) because t.Setenv is
// incompatible with t.Parallel.
func TestNewDCRRequest_EnvVarInitialAccessToken(t *testing.T) {
	const envVar = "TEST_DCR_IAT_ENVVAR_BRANCH"
	t.Setenv(envVar, "iat-from-env")

	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL:             "https://idp.example.com/.well-known/oauth-authorization-server",
			InitialAccessTokenEnvVar: envVar,
		},
	}

	req, err := newDCRRequest(rc, "https://thv.example.com")
	require.NoError(t, err)
	require.NotNil(t, req)
	assert.Equal(t, "iat-from-env", req.InitialAccessToken,
		"env-var InitialAccessToken must flow through newDCRRequest → resolveSecret")
}

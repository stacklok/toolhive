// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const restartKeyPrefix = "test:restart:auth:"

// warnRecorder captures slog records so the test can count policy-change WARNs.
type warnRecorder struct {
	mu      sync.Mutex
	records []map[string]any
}

func (*warnRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnRecorder) WithAttrs([]slog.Attr) slog.Handler     { return w }
func (w *warnRecorder) WithGroup(string) slog.Handler          { return w }

func (w *warnRecorder) Handle(_ context.Context, r slog.Record) error {
	attrs := map[string]any{"level": r.Level, "msg": r.Message}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	w.records = append(w.records, attrs)
	return nil
}

// policyWarnsFor returns the policy-change WARNs for clientID, recognised by
// their old_scopes attribute, ignoring other WARNs the server logs for the same
// client on every start. WithAttrs drops attributes, which is fine because the
// production WARN uses the package-level slog.Warn.
func (w *warnRecorder) policyWarnsFor(clientID string) []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []map[string]any
	for _, r := range w.records {
		if r["level"] == slog.LevelWarn && r["client_id"] == clientID {
			if _, ok := r["old_scopes"]; !ok {
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

// startRestartableDelegateAuthServer starts an embedded auth server whose
// storage is the shared miniredis, with one delegate client holding the given
// scopes. The returned stop func is idempotent and also registered with
// t.Cleanup, so a test can stop the server mid-way to model a restart.
func startRestartableDelegateAuthServer(
	t *testing.T, mr *miniredis.Miniredis, delegateScopes []string,
) (serverURL string, embedded *authserverrunner.EmbeddedAuthServer, issuer string, stop func()) {
	t.Helper()

	secretFile := t.TempDir() + "/delegate-client-secret"
	require.NoError(t, os.WriteFile(secretFile, []byte(delegateClientSecret), 0o600))

	server, issuer, embedded, stop := startEmbeddedAuthServer(t, func(cfg *authserver.RunConfig, _ string) {
		cfg.ScopesSupported = []string{"scope-a", "scope-b", "scope-c"}
		cfg.AllowedAudiences = []string{delegateAudience}
		cfg.Storage = &storage.RunConfig{
			Type:        string(storage.TypeRedis),
			RedisConfig: &storage.RedisRunConfig{Addr: mr.Addr(), KeyPrefix: restartKeyPrefix},
		}
		cfg.DelegateClients = []authserver.DelegateClientRunConfig{{
			ClientID:         delegateClientID,
			ClientSecretFile: secretFile,
			Scopes:           delegateScopes,
			Audiences:        []string{delegateAudience},
		}}
	}, nil)
	return server.URL, embedded, issuer, stop
}

// exchangeForScope performs a token exchange for one scope as the delegate
// client and returns the HTTP status and decoded JSON body.
func exchangeForScope(
	t *testing.T, serverURL, issuer string, embedded *authserverrunner.EmbeddedAuthServer, scope string,
) (int, map[string]any) {
	t.Helper()

	values := url.Values{
		"grant_type":         {tokenExchangeGrantType},
		"subject_token":      {signedSubjectTokenWithScopes(t, embedded, issuer, delegateClientID, "scope-a scope-b scope-c")},
		"subject_token_type": {oauthproto.TokenTypeAccessToken},
		"scope":              {scope},
		"audience":           {delegateAudience},
		"client_id":          {delegateClientID},
		"client_secret":      {delegateClientSecret},
	}
	request, err := http.NewRequest(http.MethodPost, serverURL+"/oauth/token", strings.NewReader(values.Encode()))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	require.NoError(t, err)
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		require.NoError(t, response.Body.Close())
	}()

	var body map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	return response.StatusCode, body
}

func spiffeTrustForScope(t *testing.T, scope string) *authserver.SPIFFETrustConfig {
	t.Helper()
	trust, err := authserver.NewSPIFFETrustConfig(
		[]authserver.SPIFFETrustDomainRunConfig{{
			Name:        "production",
			TrustDomain: "example.org",
			Methods:     []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
			BundleSource: authserver.SPIFFEBundleSourceRunConfig{
				Type:        authserver.SPIFFEBundleSourceTypeWorkloadAPI,
				WorkloadAPI: &authserver.SPIFFEWorkloadAPIBundleSourceRunConfig{},
			},
		}},
		&authserver.InboundGrantsRunConfig{SPIFFEClientAuth: []authserver.SPIFFEClientAuthRunConfig{{
			TrustDomainRef:   "production",
			PrincipalPattern: "spiffe://example.org/ns/default/agent",
			ClientID:         "spiffe-client",
			Methods:          []authserver.SPIFFEAuthenticationMethod{authserver.SPIFFEAuthenticationMethodX509},
			Scopes:           []string{scope},
			Audiences:        []string{delegateAudience},
			GrantTypes:       []string{authserver.SPIFFEGrantTypeTokenExchange},
		}}},
		[]string{"scope-a", "scope-b", "scope-c"},
		[]string{delegateAudience},
	)
	require.NoError(t, err)
	return trust
}

// TestConfiguredDelegateClientPolicyChangeAcrossRestarts restarts the auth
// server against one shared Redis. A changed delegate policy must roll out on
// restart, while a changed SPIFFE placeholder policy must still be refused.
//
// The final SPIFFE step enters at authserver.PreflightSPIFFEStaticClientCollisions,
// the exported function NewEmbeddedAuthServerWithStorage runs before building
// the server. A full server start cannot be used there: RunConfig.Validate
// rejects any SPIFFE trust configuration in this build.
//
// The test captures the process-global slog default, so it cannot run in parallel.
//
//nolint:paralleltest // captures the process-global slog default
func TestConfiguredDelegateClientPolicyChangeAcrossRestarts(t *testing.T) {
	mr := miniredis.RunT(t)

	prev := slog.Default()
	logs := &warnRecorder{}
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// 1. First start: scopes [a, b]. An exchange for b succeeds.
	serverURL, embedded, issuer, stop := startRestartableDelegateAuthServer(t, mr, []string{"scope-a", "scope-b"})
	status, body := exchangeForScope(t, serverURL, issuer, embedded, "scope-b")
	require.Equal(t, http.StatusOK, status, "exchange for scope-b before the policy change: %v", body)
	assert.Empty(t, logs.policyWarnsFor(delegateClientID), "first registration is not a policy change")
	stop()

	// 2-3. Restart with scopes [a, c]. Startup succeeds, c works, b is refused.
	serverURL, embedded, issuer, stop = startRestartableDelegateAuthServer(t, mr, []string{"scope-a", "scope-c"})
	status, body = exchangeForScope(t, serverURL, issuer, embedded, "scope-c")
	require.Equal(t, http.StatusOK, status, "exchange for scope-c after the policy change: %v", body)
	assert.Equal(t, "scope-c", body["scope"], "the granted scope")
	status, body = exchangeForScope(t, serverURL, issuer, embedded, "scope-b")
	require.Equal(t, http.StatusBadRequest, status, "exchange for the dropped scope-b: %v", body)
	assert.Equal(t, "invalid_scope", body["error"])
	assert.Contains(t, body["error_description"], "not allowed to request scope 'scope-b'",
		"refused by the client's registered scopes, not by the subject token")

	warns := logs.policyWarnsFor(delegateClientID)
	require.Len(t, warns, 1, "the policy-changing restart logs exactly one WARN")
	assert.ElementsMatch(t, []string{"scope-a", "scope-b"}, warns[0]["old_scopes"])
	assert.ElementsMatch(t, []string{"scope-a", "scope-c"}, warns[0]["new_scopes"])
	assert.Equal(t, []string{delegateAudience}, warns[0]["old_audiences"])
	assert.Equal(t, []string{delegateAudience}, warns[0]["new_audiences"])
	stop()

	// 4. Restart again with unchanged config: succeeds, no further WARN.
	serverURL, embedded, issuer, stop = startRestartableDelegateAuthServer(t, mr, []string{"scope-a", "scope-c"})
	status, body = exchangeForScope(t, serverURL, issuer, embedded, "scope-c")
	require.Equal(t, http.StatusOK, status, "exchange after an unchanged restart: %v", body)
	assert.Len(t, logs.policyWarnsFor(delegateClientID), 1, "an unchanged restart logs no new WARN")
	stop()

	// 5. A SPIFFE placeholder at the same Redis: the first claim succeeds, a
	// changed association policy at the same client ID is refused.
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	base := storage.NewRedisStorageWithClient(redisClient, restartKeyPrefix)
	t.Cleanup(func() { _ = base.Close() })
	ctx := t.Context()

	require.NoError(t, authserver.PreflightSPIFFEStaticClientCollisions(ctx, base, spiffeTrustForScope(t, "scope-a")))
	require.NoError(t, authserver.PreflightSPIFFEStaticClientCollisions(ctx, base, spiffeTrustForScope(t, "scope-a")),
		"an unchanged SPIFFE association is idempotent")

	err := authserver.PreflightSPIFFEStaticClientCollisions(ctx, base, spiffeTrustForScope(t, "scope-b"))
	require.ErrorIs(t, err, storage.ErrAlreadyExists)
	assert.Contains(t, err.Error(), "different configured client")
	assert.Contains(t, err.Error(), "SPIFFE-reserved clients must match exactly")
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp_test

// This file exercises the Entra on-behalf-of (OBO) outgoing-auth path end to
// end, across the seams the unit tests cover only in isolation:
//
//	MCPExternalAuthConfig (CRD)
//	  -> converter (ConvertToStrategy / ResolveSecrets)
//	  -> outgoing-auth registry dispatch (GetStrategy "obo")
//	  -> OBOStrategy.Authenticate: real Entra exchange (mock endpoint) + inject
//	  -> backend receives Authorization: Bearer <backend token>
//
// The Entra token endpoint and the backend are httptest servers, so the test
// is hermetic. Both wiring paths are covered:
//   - discovered auth (MCPServer / MCPServerEntry backends discovered from a
//     group): DiscoverAndResolveAuth resolves the secret from a Kubernetes
//     Secret inline.
//   - inline outgoingAuth: ConvertToStrategy runs alone and the operator points
//     the strategy at a per-config env var, so two OBO backends in one vMCP pod
//     each source their own secret.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	authpkg "github.com/stacklok/toolhive/pkg/auth"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
	authfactory "github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

const (
	oboTestTenant    = "contoso.onmicrosoft.com"
	oboTestClientID  = "client-abc"
	oboTestUserToken = "user-entra-token"
	oboTestScope     = "api://backend/.default"
	oboTestNamespace = "default"
	oboSecretKey     = "client-secret"
)

// TestOBOIntegration_DiscoveredAuthPath drives the discovered-auth wiring:
// DiscoverAndResolveAuth resolves the client secret from a Kubernetes Secret,
// then the registry-dispatched OBO strategy performs a real exchange and the
// backend receives the exchanged token.
func TestOBOIntegration_DiscoveredAuthPath(t *testing.T) {
	t.Parallel()

	entra, entraCap := startMockEntra(t, "backend-token-discovered")
	backend, backendCap := startMockBackend(t)

	cr := oboTestCR("obo-config", entra.URL, "obo-secret")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "obo-secret", Namespace: oboTestNamespace},
		Data:       map[string][]byte{oboSecretKey: []byte("discovered-secret")},
	}

	strategy, err := converters.DiscoverAndResolveAuth(
		t.Context(), &mcpv1beta1.ExternalAuthConfigRef{Name: cr.Name}, oboTestNamespace, newOBOFakeClient(t, cr, secret))
	require.NoError(t, err)
	require.NotNil(t, strategy)
	require.NotNil(t, strategy.OBO)
	assert.Equal(t, authtypes.StrategyTypeOBO, strategy.Type)
	assert.Equal(t, "discovered-secret", strategy.OBO.ClientSecret, "the discovered path resolves the secret inline")
	assert.Empty(t, strategy.OBO.ClientSecretEnv)

	registry, err := authfactory.NewOutgoingAuthRegistry(t.Context(), oboMapEnv{})
	require.NoError(t, err)

	exchangeThroughBackend(t, registry, strategy, backend, backendCap)
	assertOBOExchange(t, entraCap, "discovered-secret")
	assert.Equal(t, "Bearer backend-token-discovered", backendCap.authHeader())
}

// TestOBOIntegration_InlineTwoBackends drives the inline outgoingAuth wiring
// for two OBO backends in one vMCP: each strategy points at its own
// operator-mounted env var, and each exchange uses its own client secret.
func TestOBOIntegration_InlineTwoBackends(t *testing.T) {
	t.Parallel()

	entraA, entraCapA := startMockEntra(t, "backend-token-a")
	entraB, entraCapB := startMockEntra(t, "backend-token-b")
	backendA, backendCapA := startMockBackend(t)
	backendB, backendCapB := startMockBackend(t)

	strategyFor := func(name, authority string) *authtypes.BackendAuthStrategy {
		strategy, err := converters.ConvertToStrategy(oboTestCR(name, authority, name+"-secret"))
		require.NoError(t, err)
		require.NotNil(t, strategy.OBO)
		// The operator sets this when it renders the vMCP config (see
		// convertExternalAuthConfigToStrategy) and mounts the secret under the
		// same name (getExternalAuthConfigSecretEnvVars).
		strategy.OBO.ClientSecretEnv = controllerutil.GenerateUniqueOBOEnvVarName(name)
		return strategy
	}
	strategyA := strategyFor("obo-backend-a", entraA.URL)
	strategyB := strategyFor("obo-backend-b", entraB.URL)
	require.NotEqual(t, strategyA.OBO.ClientSecretEnv, strategyB.OBO.ClientSecretEnv)

	registry, err := authfactory.NewOutgoingAuthRegistry(t.Context(), oboMapEnv{
		strategyA.OBO.ClientSecretEnv: "secret-a",
		strategyB.OBO.ClientSecretEnv: "secret-b",
	})
	require.NoError(t, err)

	exchangeThroughBackend(t, registry, strategyA, backendA, backendCapA)
	exchangeThroughBackend(t, registry, strategyB, backendB, backendCapB)

	assertOBOExchange(t, entraCapA, "secret-a")
	assertOBOExchange(t, entraCapB, "secret-b")
	assert.Equal(t, "Bearer backend-token-a", backendCapA.authHeader())
	assert.Equal(t, "Bearer backend-token-b", backendCapB.authHeader())
}

// exchangeThroughBackend resolves the OBO strategy from the registry,
// authenticates a backend-bound request as a signed-in user, sends it to the
// backend, and asserts the raw user token never reached the backend.
func exchangeThroughBackend(
	t *testing.T,
	registry vmcpauth.OutgoingAuthRegistry,
	strategy *authtypes.BackendAuthStrategy,
	backend *httptest.Server,
	backendCap *oboCapture,
) {
	t.Helper()

	exec, err := registry.GetStrategy(strategy.Type)
	require.NoError(t, err, "the OBO strategy must be registered in the outgoing-auth registry")

	ctx := authpkg.WithIdentity(t.Context(), &authpkg.Identity{
		PrincipalInfo: authpkg.PrincipalInfo{Subject: "alice"},
		Token:         oboTestUserToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, backend.URL, nil)
	require.NoError(t, err)
	require.NoError(t, exec.Authenticate(ctx, req, strategy))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.NotContains(t, backendCap.authHeader(), oboTestUserToken,
		"the raw user token must never reach the backend")
}

// assertOBOExchange asserts the mock Entra endpoint saw exactly one
// well-formed on-behalf-of exchange authenticated with clientSecret.
func assertOBOExchange(t *testing.T, entraCap *oboCapture, clientSecret string) {
	t.Helper()
	form := entraCap.form()
	assert.Equal(t, 1, entraCap.count())
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", form.Get("grant_type"))
	assert.Equal(t, "on_behalf_of", form.Get("requested_token_use"))
	assert.Equal(t, oboTestClientID, form.Get("client_id"))
	assert.Equal(t, clientSecret, form.Get("client_secret"))
	assert.Equal(t, oboTestUserToken, form.Get("assertion"), "the user token is the OBO assertion")
	assert.Equal(t, oboTestScope, form.Get("scope"))
}

// oboTestCR builds an obo-typed MCPExternalAuthConfig whose token endpoint
// resolves to authorityURL (the mock Entra server; a loopback http authority
// is permitted for tests).
func oboTestCR(name, authorityURL, secretName string) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: oboTestNamespace},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO: &mcpv1beta1.OBOConfig{
				TenantID:        oboTestTenant,
				Authority:       authorityURL,
				ClientID:        oboTestClientID,
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: secretName, Key: oboSecretKey},
				Scopes:          []string{oboTestScope},
			},
		},
	}
}

func newOBOFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// startMockEntra starts an httptest server standing in for the Entra token
// endpoint. It records each request's form and issues accessToken.
func startMockEntra(t *testing.T, accessToken string) (*httptest.Server, *oboCapture) {
	t.Helper()
	c := &oboCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		c.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":3599,"access_token":"` + accessToken + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

// startMockBackend starts an httptest server standing in for the upstream MCP
// backend. It records the Authorization header it receives.
func startMockBackend(t *testing.T) (*httptest.Server, *oboCapture) {
	t.Helper()
	c := &oboCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

// oboCapture records request details from an httptest handler goroutine for
// the test goroutine to read after the exchange completes.
type oboCapture struct {
	mu     sync.Mutex
	calls  int
	auth   string
	values url.Values
}

func (c *oboCapture) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.auth = r.Header.Get("Authorization")
	c.values = r.Form
}

func (c *oboCapture) authHeader() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.auth
}

func (c *oboCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *oboCapture) form() url.Values {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values
}

// oboMapEnv is a minimal env.Reader backed by a map, standing in for the vMCP
// pod's environment (into which the operator mounts the OBO client secrets).
type oboMapEnv map[string]string

func (m oboMapEnv) Getenv(key string) string { return m[key] }

func (m oboMapEnv) LookupEnv(key string) (string, bool) {
	v, ok := m[key]
	return v, ok
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package entraobo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/virtualmcp"
)

const (
	// testNamespace holds every resource the suite creates; deleting it in
	// AfterSuite cascades the cleanup.
	testNamespace = "entra-obo-e2e"

	timeout      = 5 * time.Minute
	pollInterval = 2 * time.Second

	// azTimeout bounds the Azure CLI call, which can stall on a
	// re-authentication prompt; discoveryTimeout bounds OIDC discovery.
	azTimeout        = 2 * time.Minute
	discoveryTimeout = 30 * time.Second
	// minUserTokenLifetime is how long the user token must stay valid for a
	// full run; a shorter one would surface as a 401 inside a later spec.
	minUserTokenLifetime = 15 * time.Minute

	// mcpPort is the in-cluster port of every recorder and proxy: the
	// container, its Service, the readiness probe, and the MCPServer and
	// MCPRemoteProxy ports must all agree on it.
	mcpPort = 8080

	// pingTool is the tool the token-recording backend exposes. vMCP prefixes
	// it with the backend name.
	pingTool = "instrumented_ping"

	clientSecretKey = "client-secret"
)

// Required env vars. The suite skips unless all of them are set.
const (
	envTenantID          = "TOOLHIVE_E2E_ENTRA_TENANT_ID"
	envProxyClientID     = "TOOLHIVE_E2E_ENTRA_PROXY_CLIENT_ID"
	envProxyClientSecret = "TOOLHIVE_E2E_ENTRA_PROXY_CLIENT_SECRET"
	envBackendScope      = "TOOLHIVE_E2E_ENTRA_BACKEND_SCOPE"
	envBackendAudience   = "TOOLHIVE_E2E_ENTRA_BACKEND_AUDIENCE"
)

// Optional env vars.
const (
	// envUserToken is a pre-acquired user access token for the middle-tier
	// app. When unset, the suite asks the Azure CLI for one.
	envUserToken = "TOOLHIVE_E2E_ENTRA_USER_TOKEN"
	// envUserScope overrides the scope requested from the Azure CLI
	// (default api://<proxy client ID>/.default).
	envUserScope = "TOOLHIVE_E2E_ENTRA_USER_SCOPE"
	// envUnconsentedScope is a scope the middle-tier app has NOT been granted;
	// its negative case is skipped when unset.
	envUnconsentedScope = "TOOLHIVE_E2E_ENTRA_UNCONSENTED_SCOPE"
	// envConditionalAccessScope is a scope whose resource app is targeted by a
	// conditional-access policy the test user cannot satisfy non-interactively;
	// its negative case is skipped when unset.
	envConditionalAccessScope = "TOOLHIVE_E2E_ENTRA_CA_SCOPE"
)

// entraConfig is the tenant configuration read from the environment.
type entraConfig struct {
	tenantID          string
	proxyClientID     string
	proxyClientSecret string
	backendScope      string
	backendAudience   string

	userToken              string
	userScope              string
	unconsentedScope       string
	conditionalAccessScope string
}

// loadEntraConfig reads the configuration and returns the names of the
// required env vars that are unset.
func loadEntraConfig(getenv func(string) string) (entraConfig, []string) {
	c := entraConfig{
		tenantID:               getenv(envTenantID),
		proxyClientID:          getenv(envProxyClientID),
		proxyClientSecret:      getenv(envProxyClientSecret),
		backendScope:           getenv(envBackendScope),
		backendAudience:        getenv(envBackendAudience),
		userToken:              getenv(envUserToken),
		userScope:              getenv(envUserScope),
		unconsentedScope:       getenv(envUnconsentedScope),
		conditionalAccessScope: getenv(envConditionalAccessScope),
	}
	if c.userScope == "" && c.proxyClientID != "" {
		c.userScope = "api://" + c.proxyClientID + "/.default"
	}

	var missing []string
	for name, v := range map[string]string{
		envTenantID:          c.tenantID,
		envProxyClientID:     c.proxyClientID,
		envProxyClientSecret: c.proxyClientSecret,
		envBackendScope:      c.backendScope,
		envBackendAudience:   c.backendAudience,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	return c, missing
}

// issuer is the tenant's v2.0 issuer. Both the incoming user token and the
// exchanged backend token must carry it, which requires
// requestedAccessTokenVersion: 2 on both app registrations.
func (c entraConfig) issuer() string {
	return "https://login.microsoftonline.com/" + c.tenantID + "/v2.0"
}

func joinNames(names []string) string {
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	return strings.Join(sorted, ", ")
}

// fingerprint identifies a token in logs and failure messages without
// revealing it. Never print a token itself.
func fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:12]
}

// fingerprints maps tokens to their fingerprints so Gomega failure messages,
// which print actual values, never carry a token.
func fingerprints(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, fingerprint(t))
	}
	return out
}

// tokenClaims are the claims the suite checks on Entra access tokens.
type tokenClaims struct {
	OID string `json:"oid"`
	TID string `json:"tid"`
	Aud string `json:"aud"`
	// AZP is the client the token was issued to. For an exchanged token it
	// must be the middle-tier app.
	AZP string `json:"azp"`

	// expiry is the verified exp claim.
	expiry time.Time
}

// userIdentity is the end user's access token for the middle-tier app.
type userIdentity struct {
	token  string
	source string
	claims tokenClaims
}

// acquireUserIdentity returns the user's token for the middle-tier app, from
// TOOLHIVE_E2E_ENTRA_USER_TOKEN or else the Azure CLI, and verifies it is a
// v2 token for the middle-tier app so tenant misconfiguration fails here with
// a clear message instead of as a 401 deep inside a scenario.
func acquireUserIdentity(ctx context.Context, c entraConfig) userIdentity {
	id := userIdentity{token: c.userToken, source: envUserToken}
	if id.token == "" {
		id.source = "az account get-access-token --scope " + c.userScope
		azCtx, azCancel := context.WithTimeout(ctx, azTimeout)
		defer azCancel()
		cmd := exec.CommandContext(azCtx, "az", "account", "get-access-token",
			"--scope", c.userScope, "--query", "accessToken", "--output", "tsv")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		// stdout holds the token and is never included in the message.
		gomega.Expect(err).NotTo(gomega.HaveOccurred(),
			"Azure CLI could not mint a user token (run `az login --tenant %s` first, or set %s): %s",
			c.tenantID, envUserToken, strings.TrimSpace(stderr.String()))
		id.token = strings.TrimSpace(string(out))
	}
	gomega.Expect(id.token).NotTo(gomega.BeEmpty(), "empty user token from %s", id.source)

	id.claims = verifyAccessToken(ctx, c, id.token, c.proxyClientID,
		"user token (is requestedAccessTokenVersion 2 set on the middle-tier app, "+
			"and was the token requested for the middle-tier app's scope?)")
	gomega.Expect(id.claims.OID).NotTo(gomega.BeEmpty(), "user token has no oid claim")
	gomega.Expect(time.Until(id.claims.expiry)).To(gomega.BeNumerically(">", minUserTokenLifetime),
		"the user token from %s expires within %s; get a fresh one", id.source, minUserTokenLifetime)
	return id
}

// verifyAccessToken checks the token's signature against the tenant JWKS and
// its issuer, audience, and expiry, then returns its claims. Failure messages
// name the problem but never include the token.
func verifyAccessToken(ctx context.Context, c entraConfig, token, audience, what string) tokenClaims {
	verifyCtx, verifyCancel := context.WithTimeout(ctx, discoveryTimeout)
	defer verifyCancel()
	provider, err := oidc.NewProvider(verifyCtx, c.issuer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"discovering %s (is %s the tenant GUID?)", c.issuer(), envTenantID)
	idToken, err := provider.Verifier(&oidc.Config{ClientID: audience}).Verify(verifyCtx, token)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "verifying %s (fingerprint %s)", what, fingerprint(token))
	var claims tokenClaims
	gomega.Expect(idToken.Claims(&claims)).To(gomega.Succeed())
	claims.expiry = idToken.Expiry
	return claims
}

// expectExchangedToken asserts the backend saw exactly one distinct token, and
// that it is an Entra-signed token for the backend audience, issued to the
// middle-tier app on behalf of the test user, and not the user's own token.
func expectExchangedToken(seen []string) {
	gomega.Expect(fingerprints(seen)).To(gomega.HaveLen(1),
		"the backend should receive exactly one distinct (exchanged) token")
	exchanged := seen[0]
	gomega.Expect(fingerprint(exchanged)).NotTo(gomega.Equal(fingerprint(user.token)),
		"the backend received the user's own token, so no exchange happened")
	claims := verifyAccessToken(ctx, cfg, exchanged, cfg.backendAudience, "exchanged backend token")
	gomega.Expect(claims.OID).To(gomega.Equal(user.claims.OID),
		"the exchanged token must be issued on behalf of the test user")
	gomega.Expect(claims.AZP).To(gomega.Equal(cfg.proxyClientID),
		"the exchanged token must be issued to the middle-tier app")
}

// recreateNamespace deletes any namespace a previous or aborted run left
// behind, waits for it to be gone, and creates a fresh one, so every fixture
// can be created under its fixed name.
func recreateNamespace() {
	deleteNamespaceAndWait()
	gomega.Expect(k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
	})).To(gomega.Succeed())
}

// deleteNamespaceAndWait deletes the suite namespace and waits until it no
// longer exists.
func deleteNamespaceAndWait() {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	if err := k8sClient.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "deleting namespace %s", testNamespace)
	}
	gomega.Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: testNamespace}, &corev1.Namespace{})
		return apierrors.IsNotFound(err)
	}, timeout, pollInterval).Should(gomega.BeTrue(), "namespace %s is still terminating", testNamespace)
}

// createClientSecret stores a confidential-client secret in a Secret.
func createClientSecret(name, value string) {
	gomega.Expect(k8sClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		StringData: map[string]string{clientSecretKey: value},
	})).To(gomega.Succeed())
}

// createOIDCConfig creates an MCPOIDCConfig trusting the tenant's v2 issuer.
func createOIDCConfig(name string) {
	gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: cfg.issuer()},
		},
	})).To(gomega.Succeed())
}

// oidcRef references the suite's MCPOIDCConfig with the middle-tier app as the
// expected audience of the incoming user token.
func oidcRef(name string) *mcpv1beta1.MCPOIDCConfigReference {
	return &mcpv1beta1.MCPOIDCConfigReference{Name: name, Audience: cfg.proxyClientID}
}

// createOBOConfig creates an obo-typed MCPExternalAuthConfig for the
// middle-tier app, reading its client secret from secretName.
func createOBOConfig(name, secretName, scope string) {
	gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO: &mcpv1beta1.OBOConfig{
				TenantID:        cfg.tenantID,
				ClientID:        cfg.proxyClientID,
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: secretName, Key: clientSecretKey},
				Scopes:          []string{scope},
			},
		},
	})).To(gomega.Succeed())
}

// recorderScript is the instrumented backend from the virtualmcp suite (an MCP
// server that records every bearer token it receives on /stats), minus its
// /stats access log line, which would print the recorded tokens to the pod log.
func recorderScript() string {
	const statsLog = `print(f"Stats request: {stats}", flush=True)`
	gomega.Expect(virtualmcp.InstrumentedMCPBackendScript).To(gomega.ContainSubstring(statsLog),
		"the instrumented backend's /stats log line changed; update recorderScript")
	return strings.Replace(virtualmcp.InstrumentedMCPBackendScript, statsLog, `print("Stats request", flush=True)`, 1)
}

// recorderPodTemplate gives an MCPServer's MCP container `sh -c` as its
// entrypoint (Spec.Args carries the script) and a writable /tmp for pip.
func recorderPodTemplate() *runtime.RawExtension {
	raw, err := json.Marshal(corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:         "mcp",
				Command:      []string{"sh", "-c"},
				VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
			}},
			Volumes: []corev1.Volume{{
				Name:         "tmp",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
		},
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return &runtime.RawExtension{Raw: raw}
}

// newRecorderMCPServer returns an MCPServer running the token recorder. Its
// recorded tokens are read from the backend Service mcp-<name>, which reaches
// the MCP container directly rather than through the proxy.
func newRecorderMCPServer(name string) *mcpv1beta1.MCPServer {
	return &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:           images.PythonImage,
			Transport:       "streamable-http",
			ProxyPort:       mcpPort,
			MCPPort:         mcpPort,
			Args:            []string{recorderScript()},
			PodTemplateSpec: recorderPodTemplate(),
		},
	}
}

// waitMCPServerReady waits for the MCPServer to reach the Ready phase. Failed
// is not terminal: the controller sets it while a referenced config is not yet
// valid and retries, so the wait keeps polling and reports the last message.
func waitMCPServerReady(name string) {
	gomega.Eventually(func() error {
		s := &mcpv1beta1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, s); err != nil {
			return err
		}
		if s.Status.Phase != mcpv1beta1.MCPServerPhaseReady {
			return fmt.Errorf("MCPServer %s phase %q: %s", name, s.Status.Phase, s.Status.Message)
		}
		return nil
	}, timeout, pollInterval).Should(gomega.Succeed())
}

// deployRecorder runs the token recorder as a plain Deployment and Service,
// for the backends ToolHive reaches by URL (MCPRemoteProxy, MCPServerEntry).
// It returns the in-cluster MCP endpoint.
func deployRecorder(name string) string {
	labels := map[string]string{"app": name}
	gomega.Expect(k8sClient.Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "recorder",
						Image:   images.PythonImage,
						Command: []string{"sh", "-c", recorderScript()},
						Ports:   []corev1.ContainerPort{{ContainerPort: mcpPort}},
						ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(mcpPort)},
						}},
					}},
				},
			},
		},
	})).To(gomega.Succeed())
	gomega.Expect(k8sClient.Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Port: mcpPort, TargetPort: intstr.FromInt32(mcpPort)}},
		},
	})).To(gomega.Succeed())

	gomega.Eventually(func() error {
		d := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, d); err != nil {
			return err
		}
		if d.Status.ReadyReplicas < 1 {
			return fmt.Errorf("recorder %s not ready", name)
		}
		return nil
	}, timeout, pollInterval).Should(gomega.Succeed())

	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/mcp", name, testNamespace, mcpPort)
}

// portForward starts `kubectl port-forward svc/<service>` and returns the
// local port. The forward is stopped when the current spec or container ends.
func portForward(service string, port int32) int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	localPort := listener.Addr().(*net.TCPAddr).Port
	gomega.Expect(listener.Close()).To(gomega.Succeed())

	cmd := exec.Command("kubectl", "--kubeconfig="+kubeconfig, "-n", testNamespace, "port-forward",
		"svc/"+service, fmt.Sprintf("%d:%d", localPort, port))
	gomega.Expect(cmd.Start()).To(gomega.Succeed())
	ginkgo.DeferCleanup(func() {
		_ = cmd.Process.Kill() // best effort; the process may already be gone
		_ = cmd.Wait()
	})

	gomega.Eventually(func() error {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), time.Second)
		if err != nil {
			return err
		}
		return conn.Close()
	}, 30*time.Second, 500*time.Millisecond).Should(gomega.Succeed(), "port-forward to svc/%s", service)
	return localPort
}

// recordedTokens returns the distinct bearer tokens the recorder behind
// service has received.
func recordedTokens(service string) []string {
	port := portForward(service, mcpPort)
	stats, err := virtualmcp.GetInstrumentedMCPBackendStatsFromURL(fmt.Sprintf("http://127.0.0.1:%d/stats", port))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return stats.SeenBearerTokens
}

// callPing initializes an MCP session at endpoint as the bearer of token and
// calls the named ping tool, failing the spec unless it answers "pong".
func callPing(endpoint, token, tool string) {
	mcpClient := virtualmcp.InitializeMCPClientWithRetries(endpoint, 2*time.Minute,
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	defer func() { _ = mcpClient.Close() }() // best effort; the spec has its answer

	callCtx, callCancel := context.WithTimeout(ctx, time.Minute)
	defer callCancel()
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	result, err := mcpClient.CallTool(callCtx, req)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "calling %s", tool)
	gomega.Expect(result.IsError).To(gomega.BeFalse(), "%s returned an error: %+v", tool, result.Content)
	gomega.Expect(result.Content).NotTo(gomega.BeEmpty())
	text, ok := result.Content[0].(mcp.TextContent)
	gomega.Expect(ok).To(gomega.BeTrue(), "%s content is not text", tool)
	gomega.Expect(text.Text).To(gomega.Equal("pong"))
}

// initializeStatus sends one MCP initialize request to endpoint with token as
// the bearer and returns the HTTP status. Negative cases use it to observe the
// status code the proxy chose. It returns transport errors rather than failing,
// so a caller inside Eventually retries them; Gomega's Expect fails on a
// non-nil trailing error for one-shot callers.
func initializeStatus(endpoint, token string) (int, error) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"entra-obo-e2e","version":"1.0.0"}}}`)
	reqCtx, reqCancel := context.WithTimeout(ctx, time.Minute)
	defer reqCancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection is reused
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

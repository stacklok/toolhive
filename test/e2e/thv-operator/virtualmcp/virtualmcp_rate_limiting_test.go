// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpclient "github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = ginkgo.Describe("VirtualMCPServer Rate Limiting", ginkgo.Ordered, func() {
	const (
		timeout      = 5 * time.Minute
		pollInterval = 2 * time.Second
		oidcAudience = "vmcp-audience"
	)

	var (
		mcpGroupName           string
		backendName            string
		vmcpName               string
		redisName              string
		oidcName               string
		telemetryName          string
		vmcpLocalPort          int
		oidcLocalPort          int
		vmcpPortForwardCleanup func()
		oidcPortForwardCleanup func()
		oidcCleanup            func()
	)

	ginkgo.BeforeAll(func() {
		ts := time.Now().UnixNano()
		mcpGroupName = fmt.Sprintf("e2e-rl-group-%d", ts)
		backendName = fmt.Sprintf("e2e-rl-backend-%d", ts)
		vmcpName = fmt.Sprintf("e2e-rl-vmcp-%d", ts)
		redisName = fmt.Sprintf("e2e-rl-redis-%d", ts)
		oidcName = fmt.Sprintf("e2e-rl-oidc-%d", ts)
		telemetryName = fmt.Sprintf("e2e-rl-telemetry-%d", ts)

		ginkgo.By("Deploying Redis")
		deployRedis(redisName)

		ginkgo.By("Deploying parameterized OIDC server")
		oidcIssuer, _, cleanup := DeployParameterizedOIDCServer(
			ctx, k8sClient, oidcName, defaultNamespace, timeout, pollInterval,
		)
		oidcCleanup = cleanup
		var err error
		oidcLocalPort, oidcPortForwardCleanup, err = startRateLimitServicePortForward(oidcName, 80)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		ginkgo.By("Creating MCPOIDCConfig")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{
					Issuer:                          oidcIssuer,
					InsecureAllowHTTP:               true,
					JWKSAllowPrivateIP:              true,
					ProtectedResourceAllowPrivateIP: true,
				},
			},
		})).To(gomega.Succeed())

		ginkgo.By("Creating MCPGroup")
		CreateMCPGroupAndWait(ctx, k8sClient, mcpGroupName, defaultNamespace,
			"E2E vMCP rate limiting group", timeout, pollInterval)

		ginkgo.By("Creating backend MCPServer")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
			Spec: mcpv1beta1.MCPServerSpec{
				GroupRef:  &mcpv1beta1.MCPGroupRef{Name: mcpGroupName},
				Image:     images.YardstickServerImage,
				Transport: "streamable-http",
				ProxyPort: 8080,
				MCPPort:   8080,
			},
		})).To(gomega.Succeed())

		ginkgo.By("Waiting for backend MCPServer to be ready")
		gomega.Eventually(func() error {
			server := &mcpv1beta1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      backendName,
				Namespace: defaultNamespace,
			}, server); err != nil {
				return err
			}
			if server.Status.Phase != mcpv1beta1.MCPServerPhaseReady {
				return fmt.Errorf("backend not ready yet, phase: %s", server.Status.Phase)
			}
			return nil
		}, timeout, pollInterval).Should(gomega.Succeed())

		ginkgo.By("Creating Prometheus telemetry configuration")
		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      telemetryName,
				Namespace: defaultNamespace,
			},
			Spec: mcpv1beta1.MCPTelemetryConfigSpec{
				Prometheus: &mcpv1beta1.PrometheusConfig{Enabled: true},
			},
		})).To(gomega.Succeed())
		gomega.Eventually(func() bool {
			telemetryConfig := &mcpv1beta1.MCPTelemetryConfig{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      telemetryName,
				Namespace: defaultNamespace,
			}, telemetryConfig)
			return err == nil && telemetryConfig.Status.ConfigHash != ""
		}, timeout, pollInterval).Should(gomega.BeTrue())

		redisAddr := fmt.Sprintf("%s.%s.svc.cluster.local:6379", redisName, defaultNamespace)
		ginkgo.By("Creating VirtualMCPServer with per-user rate limiting")
		gomega.Expect(k8sClient.Create(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace,
			v1beta1test.WithVMCPGroupRef(mcpGroupName),
			v1beta1test.WithVMCPTelemetryConfigRef(telemetryName),
			v1beta1test.WithVMCPConfig(vmcpconfig.Config{
				Group: mcpGroupName,
				RateLimiting: &mcpv1beta1.RateLimitConfig{
					PerUser: &mcpv1beta1.RateLimitBucket{
						MaxTokens:    1,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			}),
			v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
				Type: "oidc",
				OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
					Name:     oidcName,
					Audience: oidcAudience,
				},
			}),
			v1beta1test.WithVMCPSessionStorage(&mcpv1beta1.SessionStorageConfig{
				Provider: mcpv1beta1.SessionStorageProviderRedis,
				Address:  redisAddr,
			}),
		))).To(gomega.Succeed())

		ginkgo.By("Waiting for VirtualMCPServer to be ready")
		WaitForVirtualMCPServerReady(ctx, k8sClient, vmcpName, defaultNamespace, timeout, pollInterval)

		ginkgo.By("Port-forwarding VirtualMCPServer service")
		vmcpLocalPort, vmcpPortForwardCleanup, err = startRateLimitServicePortForward(VMCPServiceName(vmcpName), 4483)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	})

	ginkgo.AfterAll(func() {
		if vmcpPortForwardCleanup != nil {
			vmcpPortForwardCleanup()
		}
		if oidcPortForwardCleanup != nil {
			oidcPortForwardCleanup()
		}
		if oidcCleanup != nil {
			oidcCleanup()
		}
		_ = k8sClient.Delete(ctx, v1beta1test.NewVirtualMCPServer(vmcpName, defaultNamespace))
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: backendName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPGroup{
			ObjectMeta: metav1.ObjectMeta{Name: mcpGroupName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: oidcName, Namespace: defaultNamespace},
		})
		_ = k8sClient.Delete(ctx, &mcpv1beta1.MCPTelemetryConfig{
			ObjectMeta: metav1.ObjectMeta{Name: telemetryName, Namespace: defaultNamespace},
		})
		cleanupRedis(redisName)
	})

	ginkgo.It("rejects tools/call after the per-user limit is exceeded", func() {
		token := fetchRateLimitOIDCToken(oidcLocalPort, "alice")
		mcpClient := newRateLimitMCPClient(vmcpLocalPort, token)
		defer mcpClient.Close()

		tools, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		toolName := firstEchoToolName(tools.Tools)
		gomega.Expect(toolName).ToNot(gomega.BeEmpty())

		req := mcp.CallToolRequest{}
		req.Params.Name = toolName
		req.Params.Arguments = map[string]any{"input": "ratelimittest"}

		_, err = mcpClient.CallTool(ctx, req)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		result, err := mcpClient.CallTool(ctx, req)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(result.IsError).To(gomega.BeTrue())

		structured, ok := result.StructuredContent.(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(structured["code"]).To(gomega.BeNumerically("==", ratelimit.CodeRateLimited))
		gomega.Expect(structured["message"]).To(gomega.Equal(ratelimit.MessageRateLimited))

		data, ok := structured["data"].(map[string]any)
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Expect(data["retryAfterSeconds"]).To(gomega.BeNumerically(">", 0))

		ginkgo.By("Scraping non-zero rate limit metrics")
		gomega.Eventually(func() error {
			metricFamilies, scrapeErr := scrapeRateLimitMetrics(vmcpLocalPort)
			if scrapeErr != nil {
				return scrapeErr
			}
			if !rateLimitCounterIsNonZero(metricFamilies, "toolhive_rate_limit_decisions", map[string]string{
				"decision":       "allowed",
				"scope":          "per_user",
				"operation_type": "server",
			}) {
				return fmt.Errorf("allowed rate limit decision counter is zero")
			}
			if !rateLimitCounterIsNonZero(metricFamilies, "toolhive_rate_limit_decisions", map[string]string{
				"decision":       "rejected",
				"scope":          "per_user",
				"operation_type": "server",
			}) {
				return fmt.Errorf("rejected rate limit decision counter is zero")
			}
			if !rateLimitHistogramIsNonZero(metricFamilies, "toolhive_rate_limit_check_latency") {
				return fmt.Errorf("rate limit check latency histogram is empty")
			}
			return nil
		}, 30*time.Second, time.Second).Should(gomega.Succeed())
	})
})

func scrapeRateLimitMetrics(port int) (map[string]*dto.MetricFamily, error) {
	url := fmt.Sprintf("http://localhost:%d/metrics", port)
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics endpoint returned HTTP %d", resp.StatusCode)
	}

	parser := expfmt.NewTextParser(model.UTF8Validation)
	return parser.TextToMetricFamilies(resp.Body)
}

func rateLimitCounterIsNonZero(
	families map[string]*dto.MetricFamily,
	namePrefix string,
	wantLabels map[string]string,
) bool {
	for name, family := range families {
		if !strings.HasPrefix(name, namePrefix) {
			continue
		}
		for _, metric := range family.Metric {
			if prometheusLabelsMatch(metric, wantLabels) &&
				metric.Counter != nil &&
				metric.Counter.GetValue() > 0 {
				return true
			}
		}
	}
	return false
}

func rateLimitHistogramIsNonZero(
	families map[string]*dto.MetricFamily,
	namePrefix string,
) bool {
	for name, family := range families {
		if !strings.HasPrefix(name, namePrefix) {
			continue
		}
		for _, metric := range family.Metric {
			if metric.Histogram != nil && metric.Histogram.GetSampleCount() > 0 {
				return true
			}
		}
	}
	return false
}

func prometheusLabelsMatch(metric *dto.Metric, want map[string]string) bool {
	labels := make(map[string]string, len(metric.Label))
	for _, pair := range metric.Label {
		labels[pair.GetName()] = pair.GetValue()
	}
	for name, value := range want {
		if labels[name] != value {
			return false
		}
	}
	return true
}

func fetchRateLimitOIDCToken(oidcPort int, subject string) string {
	url := fmt.Sprintf("http://localhost:%d/token?subject=%s", oidcPort, subject)
	resp, err := http.Post(url, "application/x-www-form-urlencoded", nil) //nolint:noctx
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	defer resp.Body.Close()
	gomega.Expect(resp.StatusCode).To(gomega.Equal(http.StatusOK))

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	gomega.Expect(json.NewDecoder(resp.Body).Decode(&tokenResp)).To(gomega.Succeed())
	gomega.Expect(tokenResp.AccessToken).ToNot(gomega.BeEmpty())
	return tokenResp.AccessToken
}

func newRateLimitMCPClient(vmcpPort int, token string) *mcpclient.Client {
	httpClient := &http.Client{
		Transport: &authRoundTripper{token: token, transport: http.DefaultTransport},
		Timeout:   30 * time.Second,
	}
	serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpPort)
	return InitializeMCPClientWithRetries(serverURL, 2*time.Minute, transport.WithHTTPBasicClient(httpClient))
}

func startRateLimitServicePortForward(serviceName string, servicePort int32) (int, func(), error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, nil, fmt.Errorf("failed to find free local port: %w", err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	kubeconfigArg := fmt.Sprintf("--kubeconfig=%s", kubeconfig)
	//nolint:gosec // kubeconfig, serviceName, and ports are test-controlled values.
	cmd := exec.Command("kubectl", kubeconfigArg,
		"-n", defaultNamespace, "port-forward",
		fmt.Sprintf("svc/%s", serviceName),
		fmt.Sprintf("%d:%d", localPort, servicePort))
	if err := cmd.Start(); err != nil {
		return 0, nil, fmt.Errorf("failed to start port-forward to service %s: %w", serviceName, err)
	}

	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}

	for range 30 {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", localPort), 500*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return localPort, cleanup, nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	cleanup()
	return 0, nil, fmt.Errorf("port-forward to service %s never became ready on localhost:%d", serviceName, localPort)
}

func firstEchoToolName(tools []mcp.Tool) string {
	for _, tool := range tools {
		if tool.Name == "echo" || strings.HasSuffix(tool.Name, "_echo") {
			return tool.Name
		}
	}
	return ""
}

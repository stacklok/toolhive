// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package acceptancetests

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/test/e2e/images"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
)

const (
	operatorProxyReadTimeout  = time.Second
	operatorSlowUploadDelay   = 250 * time.Millisecond
	operatorSlowUploadBytes   = 20
	operatorSlowUploadMaxTime = 4 * time.Second
)

// operatorSlowUploadBody takes five seconds to complete, so the request only
// ends before operatorSlowUploadMaxTime when the MCPServer setting reaches the
// live proxy runner.
type operatorSlowUploadBody struct {
	emitted int
}

func (b *operatorSlowUploadBody) Read(p []byte) (int, error) {
	if b.emitted >= operatorSlowUploadBytes {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}

	time.Sleep(operatorSlowUploadDelay)
	b.emitted++
	p[0] = ' '
	return 1, nil
}

var _ = Describe("MCPServer proxy read timeout", Label("mcpserver", "read-timeout", "e2e"), Ordered, Serial, func() {
	const (
		testNamespace   = "proxy-read-timeout"
		serverName      = "proxy-read-timeout"
		timeout         = 3 * time.Minute
		pollingInterval = time.Second
	)

	var nodePort int32

	BeforeAll(func() {
		By("creating an isolated namespace")
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, namespace))).To(Succeed())

		By("creating an MCPServer with a short proxyReadTimeout")
		server := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serverName,
				Namespace: testNamespace,
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image:            images.YardstickServerImage,
				Transport:        "streamable-http",
				ProxyPort:        8080,
				MCPPort:          8080,
				ProxyReadTimeout: &metav1.Duration{Duration: operatorProxyReadTimeout},
				Env: []mcpv1beta1.EnvVar{
					{Name: "TRANSPORT", Value: "streamable-http"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("waiting for the MCPServer to become ready")
		testutil.WaitForMCPServerRunning(ctx, k8sClient, serverName, testNamespace, timeout, pollingInterval)

		By("exposing the MCPServer proxy through an auto-assigned NodePort")
		testutil.CreateNodePortService(ctx, k8sClient, serverName, testNamespace)
		nodePort = testutil.GetNodePort(
			ctx,
			k8sClient,
			serverName+"-nodeport",
			testNamespace,
			timeout,
			pollingInterval,
		)

		By("waiting for the proxy endpoint to accept connections")
		healthClient := &http.Client{Timeout: 5 * time.Second}
		Eventually(func() error {
			resp, err := healthClient.Get(fmt.Sprintf("http://localhost:%d/health", nodePort))
			if err != nil {
				return err
			}
			defer func() {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("health check returned %d", resp.StatusCode)
			}
			return nil
		}, timeout, pollingInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("deleting the isolated namespace")
		_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}})
	})

	It("terminates a slow upload using spec.proxyReadTimeout", func() {
		body := &operatorSlowUploadBody{}
		requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(
			requestCtx,
			http.MethodPost,
			fmt.Sprintf("http://localhost:%d/mcp", nodePort),
			body,
		)
		Expect(err).ToNot(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = operatorSlowUploadBytes

		httpClient := &http.Client{Timeout: 8 * time.Second}
		started := time.Now()
		resp, requestErr := httpClient.Do(req)
		elapsed := time.Since(started)
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			Expect(resp.Body.Close()).To(Succeed())
		}

		if requestErr == nil {
			Expect(resp).ToNot(BeNil())
			Expect(resp.StatusCode).ToNot(Equal(http.StatusOK),
				"a timed-out upload must not produce a successful MCP response")
		}
		Expect(elapsed).To(BeNumerically("<", operatorSlowUploadMaxTime),
			"the one-second MCPServer timeout should terminate the five-second upload")
	})
})

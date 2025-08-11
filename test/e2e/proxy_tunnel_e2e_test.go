package e2e_test

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Proxy Tunnel E2E", Serial, func() {
	var (
		config          *e2e.TestConfig
		proxyTunnelCmd  *exec.Cmd
		osvServerName   string
		proxyServerName string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()

		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available for testing")

		osvServerName = generateUniqueOIDCServerName("osv-oauth-target")
		proxyServerName = generateUniqueOIDCServerName("proxy-tunnel-test")

		By("Starting OSV MCP server as target workload")
		e2e.NewTHVCommand(config, "run",
			"--name", osvServerName,
			"--transport", "sse",
			"osv").ExpectSuccess()

		By("Waiting for OSV server to be ready")
		err = e2e.WaitForMCPServer(config, osvServerName, 60*time.Second)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		By("Cleaning up test resources")

		if proxyTunnelCmd != nil && proxyTunnelCmd.Process != nil {
			_ = proxyTunnelCmd.Process.Kill()
			_, _ = proxyTunnelCmd.Process.Wait()
		}

		if config.CleanupAfter {
			err := e2e.StopAndRemoveMCPServer(config, osvServerName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}
	})

	Context("validation & error handling (no external deps)", func() {
		It("fails when --tunnel-provider is missing", func() {
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())
			base := mustBaseURL(osvServerURL)

			_, stderr, _ := e2e.NewTHVCommand(
				config,
				"proxy", "tunnel",
				base,
				proxyServerName,
			).ExpectFailure()

			Expect(stderr).To(MatchRegexp(`flag needs an argument|required`))
		})

		It("fails on invalid provider name", func() {
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())
			base := mustBaseURL(osvServerURL)

			_, stderr, _ := e2e.NewTHVCommand(
				config,
				"proxy", "tunnel",
				base,
				proxyServerName,
				"--tunnel-provider", "not-a-provider",
			).ExpectFailure()

			Expect(stderr).To(MatchRegexp(`invalid tunnel provider`))
		})

		It("fails on invalid --provider-args JSON", func() {
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())
			base := mustBaseURL(osvServerURL)

			_, stderr, _ := e2e.NewTHVCommand(
				config,
				"proxy", "tunnel",
				base,
				proxyServerName,
				"--tunnel-provider", "ngrok",
				"--provider-args", "{not-json}",
			).ExpectFailure()

			Expect(stderr).To(MatchRegexp(`invalid --provider-args`))
		})

		It("fails when tunneling a non-existent workload", func() {
			_, stderr, _ := e2e.NewTHVCommand(
				config,
				"proxy", "tunnel",
				"definitely-not-a-workload",
				proxyServerName,
				"--tunnel-provider", "ngrok",
				`--provider-args`, `{"ngrok-auth-token":"dummy","dry-run":true}`,
			).ExpectFailure()

			// The exact text may vary a bit; cover both likely messages.
			Expect(stderr).To(MatchRegexp(`failed to get workload|workload .* has empty URL`))
		})

		It("fails when ngrok args are incorrect (missing ngrok-auth-token)", func() {
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())
			base := mustBaseURL(osvServerURL)

			_, stderr, _ := e2e.NewTHVCommand(
				config,
				"proxy", "tunnel",
				base,
				proxyServerName,
				"--tunnel-provider", "ngrok",
				`--provider-args`, `{"dry-run":true}`, // no token
			).ExpectFailure()

			// ParseConfig should surface this
			Expect(stderr).To(MatchRegexp(`invalid provider config:.*ngrok-auth-token is required`))
		})
	})

	Context("happy path with ngrok in dry-run mode", func() {
		It("starts a tunnel when target is a direct URL", func() {
			osvServerURL, err := e2e.GetMCPServerURL(config, osvServerName)
			Expect(err).ToNot(HaveOccurred())
			base := mustBaseURL(osvServerURL)

			// Use dry-run to skip real network calls
			argsJSON := `{"ngrok-auth-token":"dummy-token","dry-run":true}`

			By("Starting the proxy tunnel (URL target, dry-run ngrok)")
			proxyTunnelCmd = startProxyTunnel(config, proxyServerName, base, "ngrok", argsJSON)

			time.Sleep(2 * time.Second)
			Expect(proxyTunnelCmd.ProcessState).To(BeNil(), "process should still be running")

			By("Stopping via SIGINT (graceful shutdown)")
			_ = proxyTunnelCmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- proxyTunnelCmd.Wait() }()

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				Fail("proxy did not exit after SIGINT within 10s")
			}
		})

		It("starts a tunnel when target is a workload name", func() {
			argsJSON := `{"ngrok-auth-token":"dummy-token","dry-run":true}`

			By("Starting the proxy tunnel (workload target, dry-run ngrok)")
			proxyTunnelCmd = startProxyTunnel(config, proxyServerName, osvServerName, "ngrok", argsJSON)

			time.Sleep(2 * time.Second)
			Expect(proxyTunnelCmd.ProcessState).To(BeNil(), "process should still be running")

			By("Stopping via SIGINT")
			_ = proxyTunnelCmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- proxyTunnelCmd.Wait() }()

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				Fail("proxy did not exit after SIGINT within 10s")
			}
		})
	})
})

func mustBaseURL(full string) string {
	parsedURL, err := url.Parse(full)
	if err != nil {
		GinkgoWriter.Printf("Failed to parse server URL: %v\n", err)
	}
	return fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)
}

func startProxyTunnel(config *e2e.TestConfig, serverName string, target string, provider string, providerConfig string) *exec.Cmd {
	args := []string{
		"proxy",
		"tunnel",
		target,
		serverName,
		"--tunnel-provider", provider,
		"--provider-args", providerConfig,
	}

	GinkgoWriter.Printf("Starting proxy with args: %v\n", args)
	return e2e.StartLongRunningTHVCommand(config, args...)
}

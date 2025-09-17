package e2e_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/test/e2e"
)

const (
	osvServerName = "osv"
)

func generateUniqueProxyStdioServerName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d-%d", prefix, os.Getpid(), time.Now().UnixNano(), GinkgoRandomSeed())
}

var _ = Describe("Proxy Stdio E2E", Label("proxy", "stdio", "e2e"), Serial, func() {
	var (
		config        *e2e.TestConfig
		proxyCmd      *exec.Cmd
		mcpServerName string
		workloadName  string
		transportType types.TransportType
		proxyMode     string // e.g. "sse" or "streamable-http"
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred())
		workloadName = generateUniqueProxyStdioServerName("mcpserver-proxy-stdio-target")
	})

	JustBeforeEach(func() {
		// Build args after mcpServerName is set
		args := []string{"run", "--name", workloadName, "--transport", transportType.String()}

		if transportType == types.TransportTypeStdio {
			Expect(proxyMode).ToNot(BeEmpty())
			args = append(args, "--proxy-mode", proxyMode)
		}

		args = append(args, mcpServerName)

		By("Starting MCP server as target")
		e2e.NewTHVCommand(config, args...).ExpectSuccess()

		err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		By("Cleaning up test resources")

		// Stop proxy if running
		if proxyCmd != nil && proxyCmd.Process != nil {
			proxyCmd.Process.Kill()
			proxyCmd.Wait()
		}

		// Stop and remove server
		if config.CleanupAfter {
			err := e2e.StopAndRemoveMCPServer(config, workloadName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}
	})

	Context("testing proxy stdio with sse protocol", func() {
		BeforeEach(func() {
			transportType = types.TransportTypeSSE
			mcpServerName = osvServerName
		})
		It("should proxy MCP requests successfully", func() {
			By("Getting OSV server URL")
			osvServerURL, err := e2e.GetMCPServerURL(config, workloadName)
			Expect(err).ToNot(HaveOccurred())

			By("Extracting base URL for transparent proxy")
			// The URL from thv list is like: http://127.0.0.1:21929/sse#container-name
			// But the transparent proxy needs the base URL: http://127.0.0.1:21929
			baseURL := strings.TrimSuffix(strings.Split(osvServerURL, "#")[0], "/sse")
			GinkgoWriter.Printf("Original server URL: %s\n", osvServerURL)
			GinkgoWriter.Printf("Base URL for proxy: %s\n", baseURL)

			By("Starting the stdio proxy")
			proxyCmd, stdin, outputBuffer := startProxyStdioForMCP(
				config,
				workloadName,
			)

			// Ensure the proxy started
			Eventually(func() string {
				return outputBuffer.String()
			}, 10*time.Second, 1*time.Second).Should(ContainSubstring("Starting stdio proxy"))

			// Basic JSON-RPC message to initialize session
			message := `{"jsonrpc":"2.0","id":-1,"method":"initialize","params":{}}` + "\n"
			_, err = stdin.Write([]byte(message))
			Expect(err).ToNot(HaveOccurred())

			By("Validating response is received through stdout (proxied)")
			Eventually(func() string {
				return outputBuffer.String()
			}, 15*time.Second, 1*time.Second).Should(ContainSubstring(`"id":-1`))
			Eventually(func() string {
				return outputBuffer.String()
			}, 15*time.Second, 1*time.Second).Should(ContainSubstring(`"jsonrpc":"2.0"`))

			By("Validating that response came from the SSE server via proxy")
			Expect(outputBuffer.String()).To(ContainSubstring("result")) // Or other expected field in the response

			By("Shutting down proxy")
			proxyCmd.Process.Kill()
			proxyCmd.Wait()
		})
	})

	Context("testing proxy stdio with streamable-http protocol", func() {
		BeforeEach(func() {
			transportType = types.TransportTypeStreamableHTTP
			mcpServerName = osvServerName
		})

		It("should proxy MCP requests successfully", func() {
			By("Getting OSV server URL")
			osvServerURL, err := e2e.GetMCPServerURL(config, workloadName)
			Expect(err).ToNot(HaveOccurred())

			By("Extracting base URL for transparent proxy")
			// URL will be like: http://127.0.0.1:21929/mcp#container-name
			baseURL := strings.Split(osvServerURL, "#")[0]
			baseURL = strings.TrimSuffix(baseURL, "/mcp")
			GinkgoWriter.Printf("Original server URL: %s\n", osvServerURL)
			GinkgoWriter.Printf("Base URL for proxy: %s\n", baseURL)

			By("Starting the stdio proxy")
			proxyCmd, stdin, outputBuffer := startProxyStdioForMCP(
				config,
				workloadName,
			)

			// Ensure the proxy started
			Eventually(func() string {
				return outputBuffer.String()
			}, 10*time.Second, 1*time.Second).Should(ContainSubstring("Starting stdio proxy"))

			By("Sending JSON-RPC initialize message through the proxy stdin")
			message := `{"jsonrpc":"2.0","id":-1,"method":"initialize","params":{}}` + "\n"
			_, err = stdin.Write([]byte(message))
			Expect(err).ToNot(HaveOccurred())

			By("Validating response is received through stdout (proxied)")
			Eventually(func() string {
				return outputBuffer.String()
			}, 15*time.Second, 1*time.Second).Should(ContainSubstring(`"id":-1`))
			Eventually(func() string {
				return outputBuffer.String()
			}, 15*time.Second, 1*time.Second).Should(ContainSubstring(`"jsonrpc":"2.0"`))

			By("Validating that response came from the streamable-http server via proxy")
			Expect(outputBuffer.String()).To(ContainSubstring("result"))

			By("Shutting down proxy")
			proxyCmd.Process.Kill()
			proxyCmd.Wait()
		})
	})

	Context("testing proxy stdio with stdio protocol+sse proxy mode", func() {
		BeforeEach(func() {
			transportType = types.TransportTypeStdio
			proxyMode = "sse"
			mcpServerName = "time"
		})
		It("should proxy MCP requests successfully", func() {
			By("Getting time server URL")
			timeServerURL, err := e2e.GetMCPServerURL(config, workloadName)
			Expect(err).ToNot(HaveOccurred())

			By("Extracting base URL for transparent proxy")
			// The URL from thv list is like: http://127.0.0.1:21929/sse#container-name
			// But the transparent proxy needs the base URL: http://127.0.0.1:21929
			baseURL := strings.TrimSuffix(strings.Split(timeServerURL, "#")[0], "/sse")
			GinkgoWriter.Printf("Original server URL: %s\n", timeServerURL)
			GinkgoWriter.Printf("Base URL for proxy: %s\n", baseURL)

			By("Starting the stdio proxy")
			proxyCmd, stdin, outputBuffer := startProxyStdioForMCP(
				config,
				workloadName,
			)

			// Ensure the proxy started
			Eventually(func() string {
				return outputBuffer.String()
			}, 10*time.Second, 1*time.Second).Should(ContainSubstring("Starting stdio proxy"))

			// Basic JSON-RPC message to initialize session
			message := `{"jsonrpc":"2.0","id":-1,"method":"initialize","params":{}}` + "\n"
			_, err = stdin.Write([]byte(message))
			Expect(err).ToNot(HaveOccurred())

			By("Validating response is received through stdout (proxied)")
			Eventually(func() string {
				return outputBuffer.String()
			}, 15*time.Second, 1*time.Second).Should(ContainSubstring(`"id":-1`))
			Eventually(func() string {
				return outputBuffer.String()
			}, 15*time.Second, 1*time.Second).Should(ContainSubstring(`"jsonrpc":"2.0"`))

			By("Validating that response came from the SSE server via proxy")
			Expect(outputBuffer.String()).To(ContainSubstring("result")) // Or other expected field in the response

			By("Shutting down proxy")
			proxyCmd.Process.Kill()
			proxyCmd.Wait()
		})
	})

	Context("testing proxy stdio with stdio protocol+streamable-http proxy mode", func() {
		BeforeEach(func() {
			transportType = types.TransportTypeStdio
			proxyMode = "streamable-http"
			mcpServerName = "time"
		})
		It("should proxy MCP requests successfully", func() {
			By("Getting time server URL")
			timeServerURL, err := e2e.GetMCPServerURL(config, workloadName)
			Expect(err).ToNot(HaveOccurred())

			By("Extracting base URL for transparent proxy")
			// URL will be like: http://127.0.0.1:21929/mcp#container-name
			baseURL := strings.Split(timeServerURL, "#")[0]
			baseURL = strings.TrimSuffix(baseURL, "/mcp")
			GinkgoWriter.Printf("Original server URL: %s\n", timeServerURL)
			GinkgoWriter.Printf("Base URL for proxy: %s\n", baseURL)

			By("Starting the stdio proxy")
			proxyCmd, stdin, outputBuffer := startProxyStdioForMCP(
				config,
				workloadName,
			)

			// Ensure the proxy started
			Eventually(func() string {
				return outputBuffer.String()
			}, 10*time.Second, 1*time.Second).Should(ContainSubstring("Starting stdio proxy"))

			By("Sending JSON-RPC initialize message through the proxy stdin")
			message := `{"jsonrpc":"2.0","id":-1,"method":"initialize","params":{}}` + "\n"
			_, err = stdin.Write([]byte(message))
			Expect(err).ToNot(HaveOccurred())

			By("Validating response is received through stdout (proxied)")
			Eventually(func() string {
				return outputBuffer.String()
			}, 15*time.Second, 1*time.Second).Should(ContainSubstring(`"id":-1`))
			Eventually(func() string {
				return outputBuffer.String()
			}, 15*time.Second, 1*time.Second).Should(ContainSubstring(`"jsonrpc":"2.0"`))

			By("Validating that response came from the streamable-http server via proxy")
			Expect(outputBuffer.String()).To(ContainSubstring("result"))

			By("Shutting down proxy")
			proxyCmd.Process.Kill()
			proxyCmd.Wait()
		})
	})

})

// Helper functions
func startProxyStdioForMCP(config *e2e.TestConfig, workloadName string) (*exec.Cmd, io.WriteCloser, *bytes.Buffer) {
	args := []string{
		"proxy",
		"stdio",
		workloadName,
	}

	// Log the command for debugging
	GinkgoWriter.Printf("Starting proxy stdio for MCP with args: %v\n", args)

	// Create command
	cmd := exec.Command(config.THVBinary, args...)
	cmd.Env = os.Environ()

	// Create buffer to capture output (capture both stdout and stderr)
	var outputBuffer bytes.Buffer

	// Use MultiWriter to write to both buffer and GinkgoWriter
	multiWriter := io.MultiWriter(&outputBuffer, GinkgoWriter)
	cmd.Stdout = multiWriter
	cmd.Stderr = multiWriter // Capture stderr too since logger might write there

	// Get stdin pipe BEFORE starting
	stdin, err := cmd.StdinPipe()
	Expect(err).ToNot(HaveOccurred())

	// Start the command
	err = cmd.Start()
	Expect(err).ToNot(HaveOccurred())

	return cmd, stdin, &outputBuffer
}

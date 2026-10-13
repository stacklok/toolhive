// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

const markerHTTPServer = `
import http.server
import sys

port = int(sys.argv[1])
marker = sys.argv[2].encode()

class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(marker)))
        self.end_headers()
        self.wfile.write(marker)
    def log_message(self, format, *args):
        pass

class Server(http.server.HTTPServer):
    def get_request(self):
        request, address = super().get_request()
        print("ACCEPT " + marker.decode(), flush=True)
        return request, address

Server(("0.0.0.0", port), Handler).serve_forever()
`

type envoyProxyResponse struct {
	status int
	body   string
	err    error
}

var _ = Describe("Envoy resolved-address filtering", Label("proxy", "network", "isolation", "envoy", "envoy-dns", "e2e"), func() {
	It("blocks gateway CNAMEs and DNS rebinding to a gateway", func() {
		config := e2e.NewTestConfig()
		Expect(e2e.CheckTHVBinaryAvailable(config)).To(Succeed())

		suffix := fmt.Sprintf("%d-%d", GinkgoParallelProcess(), time.Now().UnixNano())
		networkName := "nie-dns-" + suffix
		created, output := createDockerBridgeNetwork(networkName, false)
		Expect(created).To(BeTrue(), "should create controlled DNS bridge: %s", output)
		gatewayIP := dockerNetworkGateway(networkName)

		safeMarker := "safe-target-" + suffix
		gatewayMarker := "gateway-target-" + suffix
		safeContainer := "nie-safe-" + suffix
		gatewayContainer := "nie-gateway-" + suffix
		port := startEnvoyMarkerContainer(gatewayContainer, "bridge", 8080, gatewayMarker, true)
		startEnvoyMarkerContainer(safeContainer, networkName, port, safeMarker, false)
		safeIP := dockerContainerIPv4(safeContainer, networkName)

		gatewayURL := fmt.Sprintf("http://%s:%d/", gatewayIP, port)
		Eventually(func() string {
			probe, probeErr := dockerE2EOutput(20*time.Second, "run", "--rm", "--network", networkName,
				images.NetworkFixtureImage, "python", "-c",
				"import sys,urllib.request; print(urllib.request.urlopen(sys.argv[1],timeout=3).read().decode())",
				gatewayURL)
			if probeErr != nil {
				return ""
			}
			return string(probe)
		}, 30*time.Second, time.Second).Should(ContainSubstring(gatewayMarker),
			"gateway target must be independently reachable from the test bridge")
		gatewayAcceptBaseline := dockerLogCount(gatewayContainer, "ACCEPT "+gatewayMarker)
		Expect(gatewayAcceptBaseline).To(BeNumerically(">", 0),
			"the independent reachability probe must connect to the gateway canary")

		stateDir, err := os.MkdirTemp("", "toolhive-envoy-dns-*")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(stateDir) })
		Expect(os.Chmod(stateDir, 0o711)).To(Succeed())
		modePath := filepath.Join(stateDir, "mode")
		queryLogPath := filepath.Join(stateDir, "queries")
		Expect(os.WriteFile(modePath, []byte("safe\n"), 0o644)).To(Succeed())
		Expect(os.Chmod(modePath, 0o644)).To(Succeed())
		Expect(os.WriteFile(queryLogPath, nil, 0o622)).To(Succeed())
		Expect(os.Chmod(queryLogPath, 0o622)).To(Succeed())

		dnsContainer := "nie-dns-server-" + suffix
		dnsIP := startEnvoyDNSServer(dnsContainer, networkName, stateDir, safeIP, gatewayIP)
		Eventually(func() string {
			resolved, resolveErr := dockerE2EOutput(20*time.Second, "run", "--rm", "--network", networkName,
				"--dns", dnsIP, images.NetworkFixtureImage, "python", "-c",
				"import socket; print(socket.gethostbyname('rebind.gateway.test'))")
			if resolveErr != nil {
				return ""
			}
			return strings.TrimSpace(string(resolved))
		}, 30*time.Second, time.Second).Should(Equal(safeIP), "controlled DNS server must answer before Envoy starts")

		profileDir, err := os.MkdirTemp("", "toolhive-envoy-dns-profile-*")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = os.RemoveAll(profileDir) })
		profilePath := filepath.Join(profileDir, "allow-network.json")
		Expect(os.WriteFile(profilePath, []byte(internalGatewayDenyProfile), 0o600)).To(Succeed())

		workloadName := "nie-dns-bootstrap-" + suffix
		DeferCleanup(func() {
			if config.CleanupAfter {
				_ = e2e.StopAndRemoveMCPServer(config, workloadName)
			}
		})
		e2e.NewTHVCommand(config, "run", "--name", workloadName, "--isolate-network",
			"--permission-profile", profilePath, "fetch").
			WithEnv("TOOLHIVE_NETWORK_PROXY=envoy").ExpectSuccess()
		Expect(e2e.WaitForMCPServer(config, workloadName, 180*time.Second)).To(Succeed())

		bootstrapPath, envoyImage := inspectEnvoyFixture(workloadName + "-egress")
		bootstrap, err := os.ReadFile(bootstrapPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(bootstrap)).To(ContainSubstring(gatewayIP),
			"production bootstrap must snapshot the controlled bridge gateway")
		Expect(string(bootstrap)).To(ContainSubstring("resolved_address_filter"))

		fixtureContainer := "nie-envoy-dns-" + suffix
		proxyPort := startEnvoyDNSFixture(fixtureContainer, networkName, dnsIP, bootstrapPath, envoyImage)

		rebindURL := fmt.Sprintf("http://rebind.gateway.test:%d/", port)
		Eventually(func() string {
			response := requestThroughEnvoy(proxyPort, rebindURL)
			if response.err != nil || response.status != http.StatusOK {
				return ""
			}
			return response.body
		}, 30*time.Second, time.Second).Should(ContainSubstring(safeMarker),
			"the initial CNAME chain must reach the safe container through Envoy")

		staticURL := fmt.Sprintf("http://static.gateway.test:%d/", port)
		staticResponse := requestThroughEnvoy(proxyPort, staticURL)
		Expect(staticResponse.body).ToNot(ContainSubstring(gatewayMarker),
			"a CNAME ending at the bridge gateway must never reach the gateway service")
		Expect(staticResponse.err != nil || staticResponse.status != http.StatusOK).To(BeTrue(),
			"a static gateway CNAME must be rejected by Envoy")
		Expect(dockerLogCount(gatewayContainer, "ACCEPT "+gatewayMarker)).To(Equal(gatewayAcceptBaseline),
			"the static CNAME must be filtered before Envoy opens a gateway connection")

		Expect(os.WriteFile(modePath, []byte("gateway\n"), 0o600)).To(Succeed())
		gatewayReached := false
		Eventually(func() bool {
			response := requestThroughEnvoy(proxyPort, rebindURL)
			gatewayReached = gatewayReached || strings.Contains(response.body, gatewayMarker)
			queryLog, readErr := os.ReadFile(queryLogPath)
			if readErr != nil {
				return false
			}
			gatewayQuery := fmt.Sprintf("rebind.gateway.test gateway.target.test %s", gatewayIP)
			return gatewayReached || strings.Contains(string(queryLog), gatewayQuery)
		}, 90*time.Second, time.Second).Should(BeTrue(),
			"Envoy must refresh the one-second DNS answer after the fixture switches to the gateway")
		Expect(gatewayReached).To(BeFalse(),
			"a refreshed CNAME ending at the gateway must be filtered before Envoy connects")

		Consistently(func() bool {
			return strings.Contains(requestThroughEnvoy(proxyPort, rebindURL).body, gatewayMarker)
		}, 5*time.Second, time.Second).Should(BeFalse(),
			"the gateway must remain unreachable after the rebinding answer enters the DNS cache")
		Expect(dockerLogCount(gatewayContainer, "ACCEPT "+gatewayMarker)).To(Equal(gatewayAcceptBaseline),
			"DNS rebinding must not cause Envoy to open a gateway connection")

		allowWorkload := "nie-dns-allow-" + suffix
		DeferCleanup(func() {
			if config.CleanupAfter {
				_ = e2e.StopAndRemoveMCPServer(config, allowWorkload)
			}
		})
		e2e.NewTHVCommand(config, "run", "--name", allowWorkload, "--isolate-network",
			"--allow-docker-gateway", "--permission-profile", profilePath, "fetch").
			WithEnv("TOOLHIVE_NETWORK_PROXY=envoy").ExpectSuccess()
		Expect(e2e.WaitForMCPServer(config, allowWorkload, 180*time.Second)).To(Succeed())

		allowBootstrapPath, allowEnvoyImage := inspectEnvoyFixture(allowWorkload + "-egress")
		allowBootstrap, err := os.ReadFile(allowBootstrapPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(allowBootstrap)).ToNot(ContainSubstring("resolved_address_filter"),
			"the explicit gateway opt-in must omit resolved-address filtering")
		allowFixture := "nie-envoy-dns-allow-" + suffix
		allowProxyPort := startEnvoyDNSFixture(
			allowFixture, networkName, dnsIP, allowBootstrapPath, allowEnvoyImage,
		)
		Eventually(func() string {
			response := requestThroughEnvoy(allowProxyPort, staticURL)
			if response.err != nil || response.status != http.StatusOK {
				return ""
			}
			return response.body
		}, 30*time.Second, time.Second).Should(ContainSubstring(gatewayMarker),
			"the explicit gateway opt-in must allow the same CNAME target")
		Expect(dockerLogCount(gatewayContainer, "ACCEPT "+gatewayMarker)).To(BeNumerically(">", gatewayAcceptBaseline),
			"the opt-in control must prove the gateway canary remains reachable through Envoy")
	})
})

func dockerE2EOutput(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput() //nolint:gosec // test-controlled arguments
}

func startEnvoyMarkerContainer(name, networkName string, port int, marker string, publish bool) int {
	args := []string{"run", "-d", "--rm", "--name", name, "--network", networkName}
	if publish {
		args = append(args, "-p", fmt.Sprintf("0:%d", port))
	}
	args = append(args, images.NetworkFixtureImage, "python", "-u", "-c", markerHTTPServer,
		strconv.Itoa(port), marker)
	output, err := dockerE2EOutput(90*time.Second, args...)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "start marker container %s: %s", name, output)
	DeferCleanup(func() { _, _ = dockerE2EOutput(30*time.Second, "rm", "-f", name) })
	if publish {
		return publishedDockerPort(name, strconv.Itoa(port)+"/tcp")
	}
	return 0
}

func dockerContainerIPv4(containerName, networkName string) string {
	output, err := dockerE2EOutput(20*time.Second, "inspect", containerName)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "inspect container %s: %s", containerName, output)
	var inspected []struct {
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	ExpectWithOffset(1, json.Unmarshal(output, &inspected)).To(Succeed())
	ExpectWithOffset(1, inspected).To(HaveLen(1))
	address := inspected[0].NetworkSettings.Networks[networkName].IPAddress
	ExpectWithOffset(1, address).ToNot(BeEmpty(), "container %s must have an address on %s", containerName, networkName)
	return address
}

func dockerLogCount(containerName, marker string) int {
	output, err := dockerE2EOutput(20*time.Second, "logs", containerName)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "read logs for container %s: %s", containerName, output)
	return strings.Count(string(output), marker)
}

func envoyDNSServerScript() string {
	_, filename, _, ok := runtime.Caller(0)
	ExpectWithOffset(1, ok).To(BeTrue())
	return filepath.Join(filepath.Dir(filename), "testdata", "envoy_dns_server.py")
}

func startEnvoyDNSServer(name, networkName, stateDir, safeIP, gatewayIP string) string {
	scriptPath := envoyDNSServerScript()
	output, err := dockerE2EOutput(90*time.Second, "run", "-d", "--rm", "--name", name,
		"--network", networkName, "--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE",
		"--mount", "type=bind,source="+stateDir+",target=/state",
		"--mount", "type=bind,source="+scriptPath+",target=/envoy_dns_server.py,readonly",
		images.NetworkFixtureImage, "python", "-u", "/envoy_dns_server.py", "/state", safeIP, gatewayIP)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "start controlled DNS server: %s", output)
	DeferCleanup(func() { _, _ = dockerE2EOutput(30*time.Second, "rm", "-f", name) })
	return dockerContainerIPv4(name, networkName)
}

func inspectEnvoyFixture(containerName string) (string, string) {
	output, err := dockerE2EOutput(20*time.Second, "inspect", containerName)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "inspect production Envoy container: %s", output)
	var inspected []struct {
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	ExpectWithOffset(1, json.Unmarshal(output, &inspected)).To(Succeed())
	ExpectWithOffset(1, inspected).To(HaveLen(1))
	for _, mount := range inspected[0].Mounts {
		if mount.Destination == "/etc/envoy/envoy.json" {
			return mount.Source, inspected[0].Config.Image
		}
	}
	Fail("production Envoy container has no bootstrap mount", 1)
	return "", ""
}

func startEnvoyDNSFixture(name, networkName, dnsIP, bootstrapPath, image string) int {
	output, err := dockerE2EOutput(90*time.Second, "run", "-d", "--rm", "--name", name,
		"--network", networkName, "--dns", dnsIP, "-p", "127.0.0.1::3128",
		"--mount", "type=bind,source="+bootstrapPath+",target=/etc/envoy/envoy.json,readonly",
		image, "-c", "/etc/envoy/envoy.json")
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "start controlled Envoy fixture: %s", output)
	DeferCleanup(func() { _, _ = dockerE2EOutput(30*time.Second, "rm", "-f", name) })
	return publishedDockerPort(name, "3128/tcp")
}

func publishedDockerPort(containerName, containerPort string) int {
	var published string
	EventuallyWithOffset(1, func() bool {
		portOutput, portErr := dockerE2EOutput(20*time.Second, "port", containerName, containerPort)
		if portErr != nil {
			return false
		}
		published = strings.TrimSpace(string(portOutput))
		return published != ""
	}, 20*time.Second, 250*time.Millisecond).Should(BeTrue(), "container port must be published")
	_, portText, err := net.SplitHostPort(strings.Split(published, "\n")[0])
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "parse published container port %q", published)
	port, err := strconv.Atoi(portText)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return port
}

func requestThroughEnvoy(proxyPort int, target string) envoyProxyResponse {
	proxyURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
	if err != nil {
		return envoyProxyResponse{err: err}
	}
	transport := &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get(target) //nolint:gosec // target is a controlled test hostname
	if err != nil {
		return envoyProxyResponse{err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return envoyProxyResponse{status: response.StatusCode, body: string(body), err: err}
}

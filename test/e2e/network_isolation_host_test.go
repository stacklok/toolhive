// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e/images"
)

// startPublishedHTTPService starts a test-controlled HTTP service on a Docker-
// published host port and returns that port. Publishing through Docker makes the
// service reachable through user-defined bridge gateways on both Linux Engine
// and Docker Desktop.
func startPublishedHTTPService(name string) int {
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"--name", name,
		"-p", "0:8080",
		images.NetworkFixtureImage,
		"python", "-m", "http.server", "8080", "--bind", "0.0.0.0",
	).CombinedOutput()
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "should start host test service: %s", out)
	DeferCleanup(func() { _ = exec.Command("docker", "stop", name).Run() })

	out, err = exec.Command("docker", "port", name, "8080/tcp").CombinedOutput()
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "should inspect published test port: %s", out)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	ExpectWithOffset(1, lines).ToNot(BeEmpty())
	parts := strings.Split(strings.TrimSpace(lines[0]), ":")
	ExpectWithOffset(1, parts).ToNot(BeEmpty())
	var port int
	_, err = fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "should parse published test port from %q", lines[0])

	client := &http.Client{Timeout: time.Second}
	EventuallyWithOffset(1, func() error {
		resp, requestErr := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port)) //nolint:gosec // test-controlled local service
		if requestErr != nil {
			return requestErr
		}
		return resp.Body.Close()
	}, 30*time.Second, 500*time.Millisecond).Should(Succeed(), "published test service should become ready")

	return port
}

type dockerGatewayTarget struct {
	name     string
	url      string
	optional bool
}

func dockerGatewayTargets(port int, unattachedNetwork string) []dockerGatewayTarget {
	target := func(network string) string {
		return fmt.Sprintf("http://%s:%d/", dockerNetworkGateway(network), port)
	}
	return []dockerGatewayTarget{
		{name: "default bridge", url: target("bridge")},
		{name: "attached external bridge", url: target("toolhive-external")},
		{name: "unattached bridge", url: target(unattachedNetwork), optional: true},
	}
}

func dockerNetworkGateway(networkName string) string {
	gateway, ok := dockerNetworkGatewayForFamily(networkName, false)
	ExpectWithOffset(1, ok).To(BeTrue(), "network %q should have an IPv4 gateway", networkName)
	return gateway
}

func dockerNetworkGatewayForFamily(networkName string, ipv6 bool) (string, bool) {
	//nolint:gosec // network name is a fixed test input
	out, err := exec.Command("docker", "network", "inspect", networkName,
		"-f", "{{json .IPAM.Config}}").CombinedOutput()
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "should inspect network %q: %s", networkName, out)
	var configs []struct {
		Gateway string `json:"Gateway"`
	}
	ExpectWithOffset(1, json.Unmarshal(out, &configs)).To(Succeed(), "should decode network %q IPAM config", networkName)
	for _, config := range configs {
		addr, parseErr := netip.ParseAddr(config.Gateway)
		if parseErr == nil && addr.Is6() == ipv6 {
			return addr.String(), true
		}
	}
	return "", false
}

func createDockerBridgeNetwork(networkName string, ipv6 bool) (bool, string) {
	args := []string{"network", "create"}
	if ipv6 {
		args = append(args, "--ipv6")
	}
	args = append(args, networkName)
	//nolint:gosec // fixed command and test-controlled network name
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return false, string(out)
	}
	DeferCleanup(func() { _ = exec.Command("docker", "network", "rm", networkName).Run() })
	return true, string(out)
}

func dockerNetworkIsInternal(networkName string) bool {
	//nolint:gosec // network name is a fixed test input
	out, err := exec.Command("docker", "network", "inspect", networkName, "-f", "{{.Internal}}").CombinedOutput()
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "should inspect network %q: %s", networkName, out)
	return strings.TrimSpace(string(out)) == "true"
}

const (
	internalGatewayDenyProfile  = `{"name":"internal-gateway-deny","network":{"outbound":{"insecure_allow_all":true}}}`
	internalGatewayAllowProfile = `{"name":"internal-gateway-allow","network":{"outbound":{"allow_host":["example.com"],"allow_port":[443]}}}`
)

func probeInternalGateway(workload string, port int) (networkName, target, output string, err error) {
	networkName = fmt.Sprintf("toolhive-%s-internal", workload)
	ExpectWithOffset(1, dockerNetworkIsInternal(networkName)).To(BeTrue(),
		"test must use the workload's Internal:true network")
	target = fmt.Sprintf("http://%s:%d/", dockerNetworkGateway(networkName), port)
	output, err = directHTTPRequestFromNetwork(networkName, target)
	return networkName, target, output, err
}

func directHTTPRequestFromNetwork(networkName, targetURL string) (string, error) {
	const script = `import sys, urllib.request
with urllib.request.urlopen(sys.argv[1], timeout=5) as response:
    print(response.status)`
	// The image was pulled by startPublishedHTTPService before this call. No proxy
	// environment is injected, so this exercises the bridge network directly.
	//nolint:gosec // fixed command and test-controlled arguments
	out, err := exec.Command("docker", "run", "--rm", "--network", networkName,
		images.NetworkFixtureImage, "python", "-c", script, targetURL).CombinedOutput()
	return string(out), err
}

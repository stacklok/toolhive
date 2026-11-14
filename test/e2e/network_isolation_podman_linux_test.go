// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/test/e2e"
	"github.com/stacklok/toolhive/test/e2e/images"
)

var _ = Describe("Native rootless Podman host isolation", Serial, Label("podman-native"), func() {
	BeforeEach(func() {
		if os.Getenv("TOOLHIVE_E2E_PODMAN_NATIVE") == "" {
			Skip("set TOOLHIVE_E2E_PODMAN_NATIVE=1 on a native rootless Podman host")
		}
		Expect(os.Getenv("TOOLHIVE_E2E_PODMAN_NATIVE")).To(Equal("1"))
		_, err := os.Stat("/run/.containerenv")
		Expect(os.IsNotExist(err)).To(BeTrue(), "run the test process natively, not inside toolbox")
		Expect(os.Geteuid()).ToNot(BeZero(), "rootless Podman is required")
		var info struct {
			Host struct {
				NetworkBackend     string
				RootlessNetworkCmd string
				ServiceIsRemote    bool
				Security           struct{ Rootless bool }
			}
		}
		Expect(json.Unmarshal(nativePodman("info", "--format", "json"), &info)).To(Succeed())
		Expect(info.Host.Security.Rootless).To(BeTrue())
		Expect(info.Host.ServiceIsRemote).To(BeFalse())
		Expect(info.Host.NetworkBackend).To(Equal("netavark"))
		Expect(info.Host.RootlessNetworkCmd).To(Equal("pasta"))
		socket := os.Getenv("TOOLHIVE_PODMAN_SOCKET")
		Expect(socket).ToNot(BeEmpty(), "set the native Podman socket explicitly; Docker fallback is forbidden")
		stat, err := os.Stat(socket)
		Expect(err).ToNot(HaveOccurred())
		Expect(stat.Mode() & os.ModeSocket).ToNot(BeZero())
		_, err = exec.LookPath("python3")
		Expect(err).ToNot(HaveOccurred())
		for _, image := range []string{images.YardstickServerImage, images.NetworkFixtureImage} {
			nativePodman("image", "exists", image)
		}
	})

	for _, backend := range []string{"squid", "envoy"} {
		It("rejects host aliases and gateways unless explicitly opted in with "+backend, func() {
			config := e2e.NewTestConfig()
			Expect(e2e.CheckTHVBinaryAvailable(config)).To(Succeed())
			script, err := os.ReadFile("testdata/podman_native.py")
			Expect(err).ToNot(HaveOccurred())
			dir := GinkgoT().TempDir()
			marker := e2e.GenerateUniqueServerName("native-marker")
			external := nativeNetworkInspect("toolhive-external")
			Expect(external.Internal).To(BeFalse())
			Expect(external.Driver).To(Equal("bridge"))
			Expect(external.Subnets).ToNot(BeEmpty())
			gateway := external.Subnets[0].Gateway
			Expect(net.ParseIP(gateway)).ToNot(BeNil())
			// Registered first: this check runs after every workload and fixture cleanup.
			DeferCleanup(func() {
				Expect(nativeNetworkInspect("toolhive-external").ID).To(Equal(external.ID),
					"the preexisting external network must not be replaced")
			})

			// This ordinary container pins the shared network until ALL thv rm calls finish.
			controlName := e2e.GenerateUniqueServerName("native-control")
			control := nativeFixture(controlName, "toolhive-external", script,
				"serve", "0.0.0.0", "/tmp/canary.json", marker, "detached")
			controlState := nativeContainerState(control)
			hostState := nativeHostCanary(dir, "host", "0.0.0.0", marker, script, false)
			gatewayState := nativeHostCanary(dir, "gateway", gateway, marker, script, true)
			Expect(hostState().Port).ToNot(Equal(controlState.Port))
			Expect(gatewayState().Port).ToNot(Equal(controlState.Port))

			command := func(args ...string) *e2e.THVCommand {
				return e2e.NewTHVCommand(config, args...).WithEnv(
					"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "config"),
					"XDG_DATA_HOME="+filepath.Join(dir, "data"), "XDG_STATE_HOME="+filepath.Join(dir, "state"),
					"XDG_CACHE_HOME="+filepath.Join(dir, "cache"), "TOOLHIVE_DEV=true",
					"TOOLHIVE_ENVOY_IMAGE=", "TOOLHIVE_EGRESS_IMAGE=",
					"TOOLHIVE_NETWORK_PROXY="+backend, "TOOLHIVE_PODMAN_SOCKET="+os.Getenv("TOOLHIVE_PODMAN_SOCKET"))
			}
			for _, allow := range []bool{false, true} {
				profile := internalGatewayDenyProfile
				if allow {
					profile = fmt.Sprintf(`{"name":"native-allow","network":{"outbound":{"allow_host":[%q],"allow_port":[%d]}}}`,
						controlName, controlState.Port)
				}
				name := e2e.GenerateUniqueServerName("native-" + backend)
				workload := nativeStartWorkload(command, name, dir, profile, allow)
				internal := "toolhive-" + name + "-internal"
				Expect(nativeNetworkInspect(internal).Internal).To(BeTrue())
				Expect(workload.NetworkSettings.Networks).To(HaveLen(1))
				Expect(workload.NetworkSettings.Networks).To(HaveKey(internal))
				egress := nativeContainerInspect(name + "-egress")
				Expect(egress.State.Running).To(BeTrue())
				Expect(egress.Config.Labels).To(HaveKeyWithValue("toolhive", "true"))
				image := "toolhive/egress-proxy"
				if backend == "envoy" {
					image = "envoyproxy/envoy"
				}
				Expect(egress.Config.Image).To(ContainSubstring(image))
				Expect(workload.Config.Env).To(ContainElement("HTTP_PROXY=http://" + name + "-egress:3128"))
				Expect(egress.NetworkSettings.Networks).To(HaveKey("toolhive-external"))
				proxyIP := egress.NetworkSettings.Networks[internal].IPAddress
				Expect(net.ParseIP(proxyIP)).ToNot(BeNil())
				hostIP := nativeRuntimeHost(name + "-dns")
				fixture := nativeFixture(e2e.GenerateUniqueServerName("native-probe"), internal, nil,
					"-c", "import time; time.sleep(1200)")
				Expect(nativeContainerInspect(fixture).NetworkSettings.Networks).To(HaveLen(1))
				Expect(nativeContainerInspect(fixture).NetworkSettings.Networks).To(HaveKey(internal))
				targets := []struct {
					host string
					port int
				}{
					{"host.containers.internal", hostState().Port},
					{hostIP, hostState().Port},
					{"host.docker.internal", hostState().Port},
					{gateway, gatewayState().Port},
				}
				By(fmt.Sprintf("%s allow=%t: proving direct reachability before recording accepted-connection baselines", backend, allow))
				for _, target := range targets {
					nativeExpectMarker(nativeProbe(control, script, "", "direct", target.host, target.port), marker, "direct")
				}
				hostBaseline, gatewayBaseline := hostState().Accepted, gatewayState().Accepted
				for _, mode := range []string{"http", "connect"} {
					By(fmt.Sprintf("%s allow=%t %s: ordinary container control succeeds", backend, allow, mode))
					nativeExpectMarker(nativeProbe(fixture, script, proxyIP, mode, controlName, controlState.Port), marker, mode)
					if allow {
						wrongPort := nativeProbe(fixture, script, proxyIP, mode, controlName, hostState().Port)
						Expect(wrongPort.Status).To(Equal(http.StatusForbidden), "restrictive profile must still reject ordinary non-allowlisted ports")
					}
					for _, target := range targets {
						By(fmt.Sprintf("%s allow=%t %s %s:%d", backend, allow, mode, target.host, target.port))
						result := nativeProbe(fixture, script, proxyIP, mode, target.host, target.port)
						if allow {
							nativeExpectMarker(result, marker, mode)
						} else {
							Expect(result.Status).To(Equal(http.StatusForbidden), "explicit policy denial, not DNS/connect failure: %+v", result)
							if mode == "connect" {
								Expect(result.ConnectStatus).To(Equal(http.StatusForbidden), "CONNECT itself must be rejected")
							}
						}
					}
				}
				if allow {
					Expect(hostState().Accepted).To(BeNumerically(">=", hostBaseline+6))
					Expect(gatewayState().Accepted).To(BeNumerically(">=", gatewayBaseline+2))
				} else {
					Consistently(func() []int { return []int{hostState().Accepted, gatewayState().Accepted} },
						2*time.Second, 100*time.Millisecond).Should(Equal([]int{hostBaseline, gatewayBaseline}))
				}
				GinkgoWriter.Printf("%s allow=%t accepted connections: host %d -> %d; gateway %d -> %d\n",
					backend, allow, hostBaseline, hostState().Accepted, gatewayBaseline, gatewayState().Accepted)
			}
		})
	}
})

type nativeNetwork struct {
	ID       string
	Driver   string
	Internal bool
	Subnets  []struct{ Gateway string }
}

type nativeContainer struct {
	ID     string
	State  struct{ Running bool }
	Config struct {
		Image  string
		Env    []string
		Labels map[string]string
	}
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

type nativeCanaryState struct {
	Port     int
	Accepted int
}

type nativeProbeResult struct {
	Status        int
	ConnectStatus int `json:"connect_status"`
	Body          string
}

func nativePodmanOutput(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "podman", append([]string{"--remote=false"}, args...)...).CombinedOutput()
}

func nativePodman(args ...string) []byte {
	out, err := nativePodmanOutput(args...)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "podman %v: %s", args, out)
	return out
}

func nativeNetworkInspect(name string) nativeNetwork {
	var networks []nativeNetwork
	ExpectWithOffset(1, json.Unmarshal(nativePodman("network", "inspect", name), &networks)).To(Succeed())
	ExpectWithOffset(1, networks).To(HaveLen(1))
	return networks[0]
}

func nativeContainerInspect(name string) nativeContainer {
	var containers []nativeContainer
	ExpectWithOffset(1, json.Unmarshal(nativePodman("inspect", name), &containers)).To(Succeed())
	ExpectWithOffset(1, containers).To(HaveLen(1))
	return containers[0]
}

func nativeExists(kind, name string) bool {
	out, err := nativePodmanOutput(kind, "exists", name)
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	ExpectWithOffset(1, err).To(BeAssignableToTypeOf(exit), "must check %s %s: %s", kind, name, out)
	ExpectWithOffset(1, err.(*exec.ExitError).ExitCode()).To(Equal(1), "existence check must not fail for another reason: %s", out)
	return false
}

func nativeExpectAbsent(kind, name string) {
	ExpectWithOffset(1, nativeExists(kind, name)).To(BeFalse(), "%s %s must not already exist", kind, name)
}

func nativeFixture(name, network string, script []byte, args ...string) string {
	nativeExpectAbsent("container", name)
	create := []string{"create", "--name", name, "--network", network, "--http-proxy=false",
		"--pull=never", images.NetworkFixtureImage, "python"}
	if script != nil {
		create = append(create, "-c", string(script))
	}
	id := strings.TrimSpace(string(nativePodman(append(create, args...)...)))
	Expect(id).To(MatchRegexp(`^[a-f0-9]{64}$`))
	DeferCleanup(func() { nativePodman("rm", "--force", "--ignore", id) })
	nativePodman("start", id)
	return id
}

func nativeContainerState(id string) nativeCanaryState {
	var state nativeCanaryState
	Eventually(func() error {
		out, err := nativePodmanOutput("exec", id, "python", "-c", `print(open("/tmp/canary.json").read())`)
		if err != nil {
			return fmt.Errorf("canary state: %w: %s", err, out)
		}
		return json.Unmarshal(out, &state)
	}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	Expect(state.Port).To(BeNumerically(">", 0))
	return state
}

func nativeHostCanary(dir, name, bind, marker string, script []byte, rootlessNetns bool) func() nativeCanaryState {
	statePath := filepath.Join(dir, name+".json")
	args := []string{"-c", string(script), "serve", bind, statePath, marker, "controlled"}
	cmd := exec.Command("python3", args...)
	if rootlessNetns {
		cmd = exec.Command("podman", append([]string{"--remote=false", "unshare", "--rootless-netns", "python3"}, args...)...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := cmd.StdinPipe()
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(func() { _ = input.Close() })
	log, err := os.Create(filepath.Join(dir, name+".log"))
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(func() { Expect(log.Close()).To(Succeed()) })
	cmd.Stdout, cmd.Stderr = log, log
	Expect(cmd.Start()).To(Succeed())
	DeferCleanup(func() {
		Expect(input.Close()).To(Succeed())
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			Expect(err).ToNot(HaveOccurred())
		case <-time.After(10 * time.Second):
			Expect(syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)).To(Succeed())
			Eventually(done, 5*time.Second).Should(Receive())
			Fail("native canary did not exit after closing its control pipe")
		}
	})
	read := func() (nativeCanaryState, error) {
		var state nativeCanaryState
		data, err := os.ReadFile(statePath)
		if err != nil {
			return state, err
		}
		err = json.Unmarshal(data, &state)
		return state, err
	}
	Eventually(func() error { _, err := read(); return err }, 15*time.Second, 100*time.Millisecond).Should(Succeed(),
		"native canary must start; diagnostics: %s", log.Name())
	return func() nativeCanaryState {
		state, err := read()
		Expect(err).ToNot(HaveOccurred())
		Expect(state.Port).To(BeNumerically(">", 0))
		return state
	}
}

func nativeStartWorkload(command func(...string) *e2e.THVCommand, name, dir, profile string, allow bool) nativeContainer {
	names := []string{name, name + "-dns", name + "-egress", name + "-ingress"}
	for _, container := range names {
		nativeExpectAbsent("container", container)
	}
	internal := "toolhive-" + name + "-internal"
	nativeExpectAbsent("network", internal)
	DeferCleanup(func() {
		// Capture exact IDs before thv rm, including sidecars left by partial startup.
		var ids []string
		for _, container := range names {
			if nativeExists("container", container) {
				owned := nativeContainerInspect(container)
				Expect(owned.Config.Labels).To(HaveKeyWithValue("toolhive", "true"))
				ids = append(ids, owned.ID)
			}
		}
		var networkID string
		if nativeExists("network", internal) {
			networkID = nativeNetworkInspect(internal).ID
		}
		stdout, stderr, err := command("rm", name).RunWithTimeout(90 * time.Second)
		// Always clean captured IDs, even if the CLI could not finish a partial startup.
		for _, id := range ids {
			nativePodman("rm", "--force", "--ignore", id)
		}
		if networkID != "" && nativeExists("network", networkID) {
			nativePodman("network", "rm", networkID)
		}
		Expect(err).ToNot(HaveOccurred(), "thv rm %s: %s %s", name, stdout, stderr)
	})
	path := filepath.Join(dir, name+".json")
	Expect(os.WriteFile(path, []byte(profile), 0600)).To(Succeed())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).ToNot(HaveOccurred())
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	Expect(listener.Close()).To(Succeed())
	args := []string{"run", "--name", name, "--transport", "streamable-http", "--target-port", port,
		"--isolate-network", "--permission-profile", path}
	if allow {
		args = append(args, "--allow-docker-gateway")
	}
	args = append(args, images.YardstickServerImage, "--", "-port", port, "-transport", "streamable-http")
	stdout, stderr, err := command(args...).RunWithTimeout(3 * time.Minute)
	Expect(err).ToNot(HaveOccurred(), "thv run: %s %s", stdout, stderr)
	var endpoint string
	Eventually(func() error {
		workload, err := e2e.FindWorkload(command, name)
		if err != nil {
			return err
		}
		if workload == nil {
			return fmt.Errorf("%s not listed", name)
		}
		if workload.Status != rt.WorkloadStatusRunning {
			return fmt.Errorf("%s: %s (%s)", name, workload.Status, workload.StatusContext)
		}
		endpoint = workload.URL
		return nil
	}, e2e.ServerReadyTimeout(), time.Second).Should(Succeed())
	Expect(endpoint).ToNot(BeEmpty())
	client, err := e2e.NewMCPClientForStreamableHTTP(e2e.NewTestConfig(), endpoint)
	Expect(err).ToNot(HaveOccurred())
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	Expect(client.Initialize(ctx)).To(Succeed(), "actual Yardstick MCP endpoint must initialize")
	return nativeContainerInspect(name)
}

func nativeRuntimeHost(dnsName string) string {
	dns := nativeContainerInspect(dnsName)
	Expect(dns.State.Running).To(BeTrue())
	socket := os.Getenv("TOOLHIVE_PODMAN_SOCKET")
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	response, err := client.Get("http://podman/containers/" + dns.ID + "/archive?path=/etc/hosts")
	Expect(err).ToNot(HaveOccurred())
	defer response.Body.Close()
	Expect(response.StatusCode).To(Equal(http.StatusOK))
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	Expect(err).ToNot(HaveOccurred())
	archive := tar.NewReader(bytes.NewReader(data))
	header, err := archive.Next()
	Expect(err).ToNot(HaveOccurred())
	Expect(filepath.Base(header.Name)).To(Equal("hosts"))
	hosts, err := io.ReadAll(archive)
	Expect(err).ToNot(HaveOccurred())
	for _, line := range strings.Split(string(hosts), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, alias := range fields[1:] {
			if alias == "host.containers.internal" {
				address, err := netip.ParseAddr(fields[0])
				Expect(err).ToNot(HaveOccurred())
				GinkgoWriter.Printf("running DNS sidecar %s runtime host address: %s\n", dnsName, address)
				return address.String()
			}
		}
	}
	Fail("running DNS sidecar hosts archive has no host.containers.internal")
	return ""
}

func nativeProbe(fixture string, script []byte, proxy, mode, host string, port int) nativeProbeResult {
	out := nativePodman("exec", fixture, "python", "-c", string(script), "probe", proxy, "3128", mode, host, strconv.Itoa(port))
	var result nativeProbeResult
	ExpectWithOffset(1, json.Unmarshal(out, &result)).To(Succeed(), "%s", out)
	return result
}

func nativeExpectMarker(result nativeProbeResult, marker, mode string) {
	ExpectWithOffset(1, result.Status).To(Equal(http.StatusOK), "%+v", result)
	ExpectWithOffset(1, result.Body).To(Equal(marker))
	if mode == "connect" {
		ExpectWithOffset(1, result.ConnectStatus).To(Equal(http.StatusOK))
	}
}

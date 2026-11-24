// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && apple_container_integration

package apple

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
)

const (
	defaultIntegrationImage = "docker.io/library/alpine:3.20"
	integrationTimeout      = 2 * time.Minute
)

func TestAppleContainerIntegration(t *testing.T) { //nolint:paralleltest // Ginkgo manages integration spec execution
	RegisterFailHandler(Fail)
	RunSpecs(t, "Apple Container Runtime Integration Suite")
}

func integrationTestImage() string {
	if image := strings.TrimSpace(os.Getenv("APPLE_CONTAINER_TEST_IMAGE")); image != "" {
		return image
	}
	return defaultIntegrationImage
}

func uniqueWorkloadName(suffix string) string {
	return fmt.Sprintf("thv-apple-int-%d-%d-%s", os.Getpid(), time.Now().UnixNano(), suffix)
}

func findFreeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", listener.Addr())
	}
	return addr.Port, nil
}

func startTestHTTPListener(ctx context.Context, addr string) (int, func(), error) {
	var listenCfg net.ListenConfig
	listener, err := listenCfg.Listen(ctx, "tcp", addr)
	if err != nil {
		return 0, nil, err
	}

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return 0, nil, fmt.Errorf("unexpected listener address type %T", listener.Addr())
	}

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			_ = conn.Close()
		}
	}()

	return tcpAddr.Port, func() { _ = listener.Close() }, nil
}

func integrationLabels(workloadName string) map[string]string {
	return map[string]string{
		lb.LabelToolHive:      lb.LabelToolHiveValue,
		lb.LabelName:          workloadName,
		lb.LabelBaseName:      workloadName,
		lb.LabelTransport:     "stdio",
		"toolhive-int-marker": "apple-integration",
	}
}

var _ = Describe("Apple Native Container Runtime", Ordered, func() {
	var (
		client *Client
		image  string
	)

	BeforeAll(func() {
		if !IsAvailable() {
			Skip("Apple container runtime is not available on this host; start it with `container system start`")
		}

		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()

		var err error
		client, err = NewClient(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(client.RuntimeType()).To(Equal(runtime.TypeApple))

		image = integrationTestImage()
		imgMgr := NewImageManager()
		Expect(imgMgr.PullImage(ctx, image)).To(Succeed())
	})

	It("reports running status and resolves host gateway", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		Expect(client.IsRunning(ctx)).To(Succeed())

		gateway, err := client.HostGateway(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(gateway).NotTo(BeEmpty())
	})

	It("verifies image presence via ImageManager", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		imgMgr := NewImageManager()
		exists, err := imgMgr.ImageExists(ctx, image)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue())
	})

	It("returns ErrWorkloadNotFound for missing workloads and resolves by toolhive-basename", func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()

		missingName := uniqueWorkloadName("missing")
		_, err := client.GetWorkloadInfo(ctx, missingName)
		Expect(errors.Is(err, runtime.ErrWorkloadNotFound)).To(BeTrue(), "GetWorkloadInfo error: %v", err)

		_, err = client.IsWorkloadRunning(ctx, missingName)
		Expect(errors.Is(err, runtime.ErrWorkloadNotFound)).To(BeTrue(), "IsWorkloadRunning error: %v", err)

		_, err = client.GetWorkloadLogs(ctx, missingName, false, 10)
		Expect(errors.Is(err, runtime.ErrWorkloadNotFound)).To(BeTrue(), "GetWorkloadLogs error: %v", err)

		_, _, err = client.AttachToWorkload(ctx, missingName)
		Expect(errors.Is(err, runtime.ErrWorkloadNotFound)).To(BeTrue(), "AttachToWorkload error: %v", err)

		baseName := uniqueWorkloadName("base")
		containerName := baseName + "-actual"
		DeferCleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			_ = client.StopWorkload(cleanupCtx, containerName)
			_ = client.RemoveWorkload(cleanupCtx, containerName)
		})

		labels := integrationLabels(containerName)
		labels[lb.LabelBaseName] = baseName

		opts := runtime.NewDeployWorkloadOptions()
		opts.AttachStdio = false

		_, err = client.DeployWorkload(
			ctx,
			image,
			containerName,
			[]string{"sh", "-c", "trap 'exit 0' TERM INT; echo basename-ok; sleep 60 & wait"},
			nil,
			labels,
			permissions.BuiltinNoneProfile(),
			"stdio",
			opts,
			false,
		)
		Expect(err).NotTo(HaveOccurred())

		info, err := client.GetWorkloadInfo(ctx, baseName)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Name).To(Equal(containerName))
		Expect(info.State).To(Equal(runtime.WorkloadStatusRunning))

		running, err := client.IsWorkloadRunning(ctx, baseName)
		Expect(err).NotTo(HaveOccurred())
		Expect(running).To(BeTrue())

		Eventually(func() (string, error) {
			return client.GetWorkloadLogs(ctx, baseName, false, 20)
		}, 10*time.Second, 250*time.Millisecond).Should(ContainSubstring("basename-ok"))

		Expect(client.StopWorkload(ctx, baseName)).To(Succeed())
		Expect(client.RemoveWorkload(ctx, baseName)).To(Succeed())

		_, err = client.GetWorkloadInfo(ctx, baseName)
		Expect(errors.Is(err, runtime.ErrWorkloadNotFound)).To(BeTrue())
	})

	It("manages a stdio workload lifecycle and rejects second attachment", func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()

		workloadName := uniqueWorkloadName("stdio")
		DeferCleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			_ = client.StopWorkload(cleanupCtx, workloadName)
			_ = client.RemoveWorkload(cleanupCtx, workloadName)
		})

		profile := permissions.BuiltinNoneProfile()
		opts := runtime.NewDeployWorkloadOptions()
		opts.AttachStdio = true

		exposedPort, err := client.DeployWorkload(
			ctx,
			image,
			workloadName,
			[]string{"sh", "-c", `echo "ready-log TEST_MODE=$TEST_MODE" >&2; cat`},
			map[string]string{"TEST_MODE": "stdio"},
			integrationLabels(workloadName),
			profile,
			"stdio",
			opts,
			false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(exposedPort).To(Equal(0))

		stdin, stdout, err := client.AttachToWorkload(ctx, workloadName)
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			_ = stdin.Close()
			_ = stdout.Close()
		}()

		const payload = "hello-apple-stdio\n"
		_, err = io.WriteString(stdin, payload)
		Expect(err).NotTo(HaveOccurred())

		lineCh := make(chan string, 1)
		errCh := make(chan error, 1)
		go func() {
			reader := bufio.NewReader(stdout)
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				errCh <- readErr
				return
			}
			lineCh <- line
		}()

		Eventually(func() (string, error) {
			select {
			case readErr := <-errCh:
				return "", readErr
			case line := <-lineCh:
				return line, nil
			default:
				return "", errors.New("waiting for echoed stdio output")
			}
		}, 15*time.Second, 100*time.Millisecond).Should(Equal(payload))

		_, _, reattachErr := client.AttachToWorkload(ctx, workloadName)
		Expect(reattachErr).To(MatchError(runtime.ErrReattachNotSupported))

		running, err := client.IsWorkloadRunning(ctx, workloadName)
		Expect(err).NotTo(HaveOccurred())
		Expect(running).To(BeTrue())

		info, err := client.GetWorkloadInfo(ctx, workloadName)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Name).To(Equal(workloadName))

		workloads, err := client.ListWorkloads(ctx)
		Expect(err).NotTo(HaveOccurred())
		found := false
		for _, item := range workloads {
			if item.Name == workloadName {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue())

		logs, err := client.GetWorkloadLogs(ctx, workloadName, false, 50)
		Expect(err).NotTo(HaveOccurred())
		Expect(logs).To(ContainSubstring("ready-log TEST_MODE=stdio"))

		Expect(client.StopWorkload(ctx, workloadName)).To(Succeed())
		Expect(client.RemoveWorkload(ctx, workloadName)).To(Succeed())
	})

	It("deploys an HTTP workload with port mapping and serves traffic", func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()

		workloadName := uniqueWorkloadName("http")
		DeferCleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			_ = client.StopWorkload(cleanupCtx, workloadName)
			_ = client.RemoveWorkload(cleanupCtx, workloadName)
		})

		hostPort, err := findFreeLocalPort()
		Expect(err).NotTo(HaveOccurred())

		profile := permissions.BuiltinNoneProfile()
		opts := runtime.NewDeployWorkloadOptions()
		opts.AttachStdio = false
		opts.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
		opts.PortBindings = map[string][]runtime.PortBinding{
			"8080/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(hostPort)}},
		}

		labels := integrationLabels(workloadName)
		labels[lb.LabelTransport] = "streamable-http"

		port, err := client.DeployWorkload(
			ctx,
			image,
			workloadName,
			[]string{
				"sh", "-c",
				`while true; do printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok' | nc -l -p 8080; done`,
			},
			nil,
			labels,
			profile,
			"streamable-http",
			opts,
			false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(port).To(Equal(hostPort))

		httpClient := &http.Client{Timeout: 2 * time.Second}
		targetURL := fmt.Sprintf("http://127.0.0.1:%d/", hostPort)

		Eventually(func() (string, error) {
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
			if reqErr != nil {
				return "", reqErr
			}
			resp, respErr := httpClient.Do(req)
			if respErr != nil {
				return "", respErr
			}
			defer resp.Body.Close()

			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				return "", readErr
			}
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
			}
			return strings.TrimSpace(string(body)), nil
		}, 30*time.Second, 250*time.Millisecond).Should(Equal("ok"))

		Expect(client.StopWorkload(ctx, workloadName)).To(Succeed())
		Expect(client.RemoveWorkload(ctx, workloadName)).To(Succeed())
	})

	It("enforces read-only root filesystem with writable tmpfs scratch mounts", func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()

		workloadName := uniqueWorkloadName("rorootfs")
		DeferCleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			_ = client.StopWorkload(cleanupCtx, workloadName)
			_ = client.RemoveWorkload(cleanupCtx, workloadName)
		})

		opts := runtime.NewDeployWorkloadOptions()
		opts.AttachStdio = false
		opts.ReadOnlyRootfs = true

		script := strings.Join([]string{
			`if touch /ro-root-check 2>/dev/null; then echo ROOTFS=writable; else echo ROOTFS=readonly; fi`,
			`touch /tmp/ok && echo TMP=ok`,
			`touch /var/tmp/ok && echo VARTMP=ok`,
			`touch /run/ok && echo RUN=ok`,
			`grep -E ' /(tmp|var/tmp|run) tmpfs ' /proc/mounts`,
			`trap 'exit 0' TERM INT`,
			`echo RO_CHECKS_DONE`,
			`sleep 60 & wait`,
		}, "; ")

		_, err := client.DeployWorkload(
			ctx,
			image,
			workloadName,
			[]string{"sh", "-c", script},
			nil,
			integrationLabels(workloadName),
			permissions.BuiltinNoneProfile(),
			"stdio",
			opts,
			false,
		)
		Expect(err).NotTo(HaveOccurred())

		var logs string
		Eventually(func() (string, error) {
			var logErr error
			logs, logErr = client.GetWorkloadLogs(ctx, workloadName, false, 100)
			return logs, logErr
		}, 30*time.Second, 250*time.Millisecond).Should(ContainSubstring("RO_CHECKS_DONE"))

		Expect(logs).To(ContainSubstring("ROOTFS=readonly"))
		Expect(logs).To(ContainSubstring("TMP=ok"))
		Expect(logs).To(ContainSubstring("VARTMP=ok"))
		Expect(logs).To(ContainSubstring("RUN=ok"))
		Expect(logs).To(MatchRegexp(`tmpfs /tmp tmpfs [^\n]*nosuid[^\n]*nodev[^\n]*noexec`))
		Expect(logs).To(MatchRegexp(`tmpfs /var/tmp tmpfs [^\n]*nosuid[^\n]*nodev[^\n]*noexec`))
		Expect(logs).To(MatchRegexp(`tmpfs /run tmpfs [^\n]*nosuid[^\n]*nodev[^\n]*noexec`))

		Expect(client.StopWorkload(ctx, workloadName)).To(Succeed())
		Expect(client.RemoveWorkload(ctx, workloadName)).To(Succeed())
	})

	It("supports network.mode none with loopback-only networking", func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()

		workloadName := uniqueWorkloadName("netnone")
		DeferCleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			_ = client.StopWorkload(cleanupCtx, workloadName)
			_ = client.RemoveWorkload(cleanupCtx, workloadName)
		})

		profile := &permissions.Profile{
			Network: &permissions.NetworkPermissions{
				Mode: "none",
			},
		}
		opts := runtime.NewDeployWorkloadOptions()
		opts.AttachStdio = false

		script := strings.Join([]string{
			`echo "IFACES=$(ip -o link show | awk -F': ' '{print $2}' | tr '\n' ',')"`,
			`if nc -z -w 2 1.1.1.1 80 2>/dev/null; then echo DIRECT=open; else echo DIRECT=blocked; fi`,
			`trap 'exit 0' TERM INT`,
			`echo NONE_CHECKS_DONE`,
			`sleep 60 & wait`,
		}, "; ")

		_, err := client.DeployWorkload(
			ctx,
			image,
			workloadName,
			[]string{"sh", "-c", script},
			nil,
			integrationLabels(workloadName),
			profile,
			"stdio",
			opts,
			false,
		)
		Expect(err).NotTo(HaveOccurred())

		var logs string
		Eventually(func() (string, error) {
			var logErr error
			logs, logErr = client.GetWorkloadLogs(ctx, workloadName, false, 100)
			return logs, logErr
		}, 30*time.Second, 250*time.Millisecond).Should(ContainSubstring("NONE_CHECKS_DONE"))

		Expect(logs).To(ContainSubstring("IFACES=lo,"))
		Expect(logs).NotTo(ContainSubstring("eth0"))
		Expect(logs).To(ContainSubstring("DIRECT=blocked"))

		Expect(client.StopWorkload(ctx, workloadName)).To(Succeed())
		Expect(client.RemoveWorkload(ctx, workloadName)).To(Succeed())
	})

	verifyEgressIsolation := func(workloadSuffix string) {
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()

		if err := checkMacOSVersionForIsolation(ctx, execCommandRunner{}); err != nil {
			Skip(err.Error())
		}

		defaultGW, err := client.HostGateway(ctx)
		Expect(err).NotTo(HaveOccurred())

		loopbackPort, closeLoopback, err := startTestHTTPListener(ctx, "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(closeLoopback)

		// Bind an ephemeral wildcard listener so the test can confirm that the
		// default vmnet gateway is unreachable directly from the internal network.
		// Note: vmnet hostOnly mode still exposes wildcard (0.0.0.0) host services
		// on the internal network's own gateway IP; loopback (127.0.0.1) services
		// remain unreachable on both gateways.
		anyPort, closeAny, err := startTestHTTPListener(ctx, "0.0.0.0:0")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(closeAny)

		workloadName := uniqueWorkloadName(workloadSuffix)
		DeferCleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cleanupCancel()
			if CurrentSpecReport().Failed() {
				if out, logErr := (execCommandRunner{}).Run(cleanupCtx, containerBinary, "logs", "--", egressContainerName(workloadName)); logErr == nil {
					_, _ = fmt.Fprintf(GinkgoWriter, "\n--- egress proxy logs (%s) ---\n%s\n", egressContainerName(workloadName), string(out))
				}
			}
			_ = client.StopWorkload(cleanupCtx, workloadName)
			_ = client.RemoveWorkload(cleanupCtx, workloadName)
		})

		profile := &permissions.Profile{
			Network: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"example.com"},
					AllowPort: []int{80},
				},
			},
		}
		opts := runtime.NewDeployWorkloadOptions()
		opts.AttachStdio = false

		probeScript := `
set -u
trap 'exit 0' TERM INT
proxy_addr="${HTTP_PROXY#http://}"
proxy_host="${proxy_addr%:*}"
proxy_port="${proxy_addr##*:}"

i=0
while [ "$i" -lt 30 ]; do
  if nc -z -w 1 "$proxy_host" "$proxy_port" 2>/dev/null; then
    break
  fi
  i=$((i + 1))
  sleep 1
done

internal_gw="$(ip route | awk '/^default / {print $3; exit}')"
echo "INTERNAL_GW=$internal_gw"
echo "HTTP_PROXY_VAL=$HTTP_PROXY"

http_via_proxy() {
  wget -S -T 10 -O /dev/null "$1" 2>&1 | awk '/HTTP\/1\.[01]/ {sub(/^ +/, ""); print; exit}'
}

echo "PROXY_ALLOWED=$(http_via_proxy http://example.com/)"
echo "PROXY_DENIED_HOST=$(http_via_proxy http://example.org/)"

denied_port_status="$({ printf 'CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n'; sleep 2; } | nc -w 5 "$proxy_host" "$proxy_port" | head -n 1 | tr -d '\r')"
echo "PROXY_DENIED_PORT=$denied_port_status"

echo "PROXY_DEFAULT_GW=$(http_via_proxy "http://${DEFAULT_GW}:${HOST_LOOPBACK_PORT}/")"
echo "PROXY_INTERNAL_GW=$(http_via_proxy "http://${internal_gw}:${HOST_LOOPBACK_PORT}/")"

if nc -z -w 3 1.1.1.1 80 2>/dev/null; then echo "DIRECT_EXTERNAL=open"; else echo "DIRECT_EXTERNAL=blocked"; fi
if nc -z -w 2 "$DEFAULT_GW" "$HOST_LOOPBACK_PORT" 2>/dev/null; then echo "DIRECT_DEFAULT_GW_LOOPBACK=open"; else echo "DIRECT_DEFAULT_GW_LOOPBACK=blocked"; fi
if nc -z -w 2 "$DEFAULT_GW" "$HOST_ANY_PORT" 2>/dev/null; then echo "DIRECT_DEFAULT_GW_ANY=open"; else echo "DIRECT_DEFAULT_GW_ANY=blocked"; fi
if nc -z -w 2 "$internal_gw" "$HOST_LOOPBACK_PORT" 2>/dev/null; then echo "DIRECT_INTERNAL_GW_LOOPBACK=open"; else echo "DIRECT_INTERNAL_GW_LOOPBACK=blocked"; fi
if nc -z -w 2 "$internal_gw" "$HOST_ANY_PORT" 2>/dev/null; then echo "DIRECT_INTERNAL_GW_ANY=open"; else echo "DIRECT_INTERNAL_GW_ANY=blocked"; fi

echo "ISOLATION_CHECKS_DONE"
sleep 60 &
wait
`

		_, err = client.DeployWorkload(
			ctx,
			image,
			workloadName,
			[]string{"sh", "-c", probeScript},
			map[string]string{
				"DEFAULT_GW":         defaultGW,
				"HOST_LOOPBACK_PORT": strconv.Itoa(loopbackPort),
				"HOST_ANY_PORT":      strconv.Itoa(anyPort),
			},
			integrationLabels(workloadName),
			profile,
			"stdio",
			opts,
			true,
		)
		Expect(err).NotTo(HaveOccurred())

		running, err := client.IsWorkloadRunning(ctx, workloadName)
		Expect(err).NotTo(HaveOccurred())
		Expect(running).To(BeTrue())

		var logs string
		Eventually(func() (string, error) {
			var logErr error
			logs, logErr = client.GetWorkloadLogs(ctx, workloadName, false, 200)
			return logs, logErr
		}, 60*time.Second, 500*time.Millisecond).Should(ContainSubstring("ISOLATION_CHECKS_DONE"))

		Expect(logs).To(MatchRegexp(`PROXY_ALLOWED=HTTP/1\.[01] [23][0-9][0-9]`))
		Expect(logs).To(MatchRegexp(`PROXY_DENIED_HOST=HTTP/1\.[01] 403`))
		Expect(logs).To(MatchRegexp(`PROXY_DENIED_PORT=HTTP/1\.[01] 403`))
		Expect(logs).To(MatchRegexp(`PROXY_DEFAULT_GW=HTTP/1\.[01] 403`))
		Expect(logs).To(MatchRegexp(`PROXY_INTERNAL_GW=HTTP/1\.[01] 403`))
		Expect(logs).To(ContainSubstring("DIRECT_EXTERNAL=blocked"))
		Expect(logs).To(ContainSubstring("DIRECT_DEFAULT_GW_LOOPBACK=blocked"))
		Expect(logs).To(ContainSubstring("DIRECT_DEFAULT_GW_ANY=blocked"))
		Expect(logs).To(ContainSubstring("DIRECT_INTERNAL_GW_LOOPBACK=blocked"))
		// Pin the known macOS vmnet hostOnly boundary: wildcard (0.0.0.0) host
		// services remain directly reachable on the internal network gateway IP.
		// If a future macOS / apple/container release blocks this path, this
		// assertion will flag the change so docs and warnings can be updated.
		Expect(logs).To(ContainSubstring("DIRECT_INTERNAL_GW_ANY=open"))

		Expect(client.StopWorkload(ctx, workloadName)).To(Succeed())
		Expect(client.RemoveWorkload(ctx, workloadName)).To(Succeed())
	}

	It("enforces network isolation allowlist, blocks direct egress, and blocks host loopback on macOS 26+", func() {
		verifyEgressIsolation("isolated")
	})

	It("enforces network isolation via Envoy sidecar when TOOLHIVE_NETWORK_PROXY=envoy", func() {
		GinkgoT().Setenv("TOOLHIVE_NETWORK_PROXY", "envoy")
		verifyEgressIsolation("envoy")
	})

	It("serves HTTP traffic through isolated ingress proxy on macOS 26+", func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()

		if err := checkMacOSVersionForIsolation(ctx, execCommandRunner{}); err != nil {
			Skip(err.Error())
		}

		workloadName := uniqueWorkloadName("http-iso")
		DeferCleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cleanupCancel()
			_ = client.StopWorkload(cleanupCtx, workloadName)
			_ = client.RemoveWorkload(cleanupCtx, workloadName)
		})

		profile := &permissions.Profile{
			Network: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"example.com"},
					AllowPort: []int{80},
				},
			},
		}
		opts := runtime.NewDeployWorkloadOptions()
		opts.AttachStdio = false
		opts.ExposedPorts = map[string]struct{}{"8080/tcp": {}}

		labels := integrationLabels(workloadName)
		labels[lb.LabelTransport] = "streamable-http"

		port, err := client.DeployWorkload(
			ctx,
			image,
			workloadName,
			[]string{
				"sh", "-c",
				`while true; do printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok' | nc -l -p 8080; done`,
			},
			nil,
			labels,
			profile,
			"streamable-http",
			opts,
			true,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(port).To(BeNumerically(">", 0))

		httpClient := &http.Client{Timeout: 3 * time.Second}
		targetURL := fmt.Sprintf("http://127.0.0.1:%d/", port)

		Eventually(func() (string, error) {
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
			if reqErr != nil {
				return "", reqErr
			}
			resp, respErr := httpClient.Do(req)
			if respErr != nil {
				return "", respErr
			}
			defer resp.Body.Close()

			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				return "", readErr
			}
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
			}
			return strings.TrimSpace(string(body)), nil
		}, 45*time.Second, 500*time.Millisecond).Should(Equal("ok"))

		Expect(client.StopWorkload(ctx, workloadName)).To(Succeed())
		Expect(client.RemoveWorkload(ctx, workloadName)).To(Succeed())
	})

	It("builds and tags an image via ImageManager.BuildImage", func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
		defer cancel()

		buildDir := GinkgoT().TempDir()
		dockerfile := fmt.Sprintf("FROM %s\nLABEL toolhive.test=built\n", image)
		Expect(os.WriteFile(filepath.Join(buildDir, "Dockerfile"), []byte(dockerfile), 0o600)).To(Succeed())

		tag := fmt.Sprintf("toolhive-apple-int-build:%d-%d", os.Getpid(), time.Now().UnixNano())
		DeferCleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			_, _ = (execCommandRunner{}).Run(cleanupCtx, containerBinary, "image", "rm", "--", tag)
		})

		imgMgr := NewImageManager()
		Expect(imgMgr.BuildImage(ctx, buildDir, tag)).To(Succeed())

		exists, err := imgMgr.ImageExists(ctx, tag)
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue())
	})
})

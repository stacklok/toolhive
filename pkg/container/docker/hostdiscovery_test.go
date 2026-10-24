// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func hostsArchive(t *testing.T, name, content string, kind byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	header := &tar.Header{Name: name, Typeflag: kind, Mode: 0o644}
	if kind == tar.TypeReg {
		header.Size = int64(len(content))
	}
	require.NoError(t, tw.WriteHeader(header))
	if kind == tar.TypeReg {
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func TestParseHostAliasArchive(t *testing.T) {
	t.Parallel()
	valid := "127.0.0.1 localhost\n192.0.2.9 host.containers.internal host.docker.internal\n"
	for _, tt := range []struct {
		name string
		data []byte
		want []netip.Addr
	}{
		{"runtime aliases", hostsArchive(t, "hosts", valid, tar.TypeReg), []netip.Addr{netip.MustParseAddr("192.0.2.9")}},
		{"multiple mappings and normalization", hostsArchive(t, "hosts", valid+
			"::ffff:192.0.2.9 HOST.DOCKER.INTERNAL. # duplicate\n"+
			"2001:db8::9 host.containers.internal\n198.51.100.7 gateway.docker.internal host.docker.internal\n", tar.TypeReg),
			[]netip.Addr{netip.MustParseAddr("192.0.2.9"), netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("2001:db8::9")}},
		{"missing Podman alias", hostsArchive(t, "hosts", "192.0.2.9 host.docker.internal\n", tar.TypeReg), nil},
		{"Podman alias only", hostsArchive(t, "hosts", "192.0.2.9 host.containers.internal\n", tar.TypeReg),
			[]netip.Addr{netip.MustParseAddr("192.0.2.9")}},
		{"no host aliases", hostsArchive(t, "hosts", "127.0.0.1 localhost\n", tar.TypeReg), nil},
		{"invalid IP", hostsArchive(t, "hosts", valid+"invalid host.containers.internal\n", tar.TypeReg), nil},
		{"zoned IP", hostsArchive(t, "hosts", valid+"fe80::1%eth0 host.containers.internal\n", tar.TypeReg), nil},
		{"missing hostname", hostsArchive(t, "hosts", valid+"192.0.2.3\n", tar.TypeReg), nil},
		{"wrong member", hostsArchive(t, "etc/hosts", valid, tar.TypeReg), nil},
		{"symlink", hostsArchive(t, "hosts", "", tar.TypeSymlink), nil},
		{"oversized member", hostsArchive(t, "hosts", strings.Repeat("#", (1<<20)+1), tar.TypeReg), nil},
		{"oversized archive", make([]byte, (2<<20)+1), nil},
		{"malformed tar", []byte("not a tar archive"), nil},
		{"multiple archive members", append(hostsArchive(t, "hosts", valid, tar.TypeReg)[:1024],
			hostsArchive(t, "extra", valid, tar.TypeReg)...), nil},
		{"truncated member", hostsArchive(t, "hosts", valid, tar.TypeReg)[:520], nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseHostAliasArchive(bytes.NewReader(tt.data))
			if tt.want == nil {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// Exercise the archive adapter against HTTP, including deployment ordering.
func discoveryHTTPClient(t *testing.T, handler http.HandlerFunc) *mobyclient.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := mobyclient.New(mobyclient.WithHost(server.URL), mobyclient.WithAPIVersion("1.44"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func TestDiscoverHostAliasesBoundsAndClosesArchive(t *testing.T) {
	t.Parallel()
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversized=%t", oversized), func(t *testing.T) {
			t.Parallel()
			closed := make(chan struct{})
			client := &Client{hostAPI: discoveryHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				defer close(closed)
				w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString([]byte(`{"name":"hosts"}`)))
				w.WriteHeader(http.StatusOK)
				if oversized {
					_, _ = w.Write(make([]byte, (2<<20)+1)) // The client may close once its limit is reached.
				}
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
					t.Error("archive response was not closed")
				}
			})}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			t.Cleanup(cancel)
			_, err := client.discoverHostAliases(ctx, "dns")
			if oversized {
				require.ErrorContains(t, err, "hosts archive exceeds size limit")
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("archive handler did not finish")
			}
		})
	}
}

func TestPodmanProxyPreservesImageEntrypoints(t *testing.T) {
	for _, backend := range []string{"squid", "envoy"} {
		t.Run(backend, func(t *testing.T) {
			image := "example.com/custom-" + backend + ":wrapper"
			t.Setenv("TOOLHIVE_EGRESS_IMAGE", image)
			t.Setenv("TOOLHIVE_ENVOY_IMAGE", image)
			created := false
			c := &Client{runtimeType: runtime.TypePodman,
				imageManager: &fakeImageManager{pulledImages: map[string]struct{}{image: {}}},
				api: &fakeDockerAPI{createFunc: func(_ context.Context, config *container.Config, host *container.HostConfig,
					_ *network.NetworkingConfig, _ *v1.Platform, _ string,
				) (container.CreateResponse, error) {
					created = true
					require.Equal(t, image, config.Image)
					require.Empty(t, config.Entrypoint, "preserve the image's wrapper entrypoint")
					if backend == "squid" {
						require.Empty(t, config.Cmd)
					} else {
						require.Equal(t, []string{"-c", "/etc/envoy/envoy.json"}, config.Cmd)
					}
					for _, mount := range host.Mounts {
						t.Cleanup(func() { require.NoError(t, os.Remove(mount.Source)) })
					}
					return container.CreateResponse{ID: "proxy"}, nil
				}}}
			spec := proxySpec{WorkloadName: "app", TransportType: "stdio", GatewayIPs: testGatewayIPs}
			if backend == "squid" {
				_, err := (&squidProxy{client: c}).SetupEgress(t.Context(), spec)
				require.NoError(t, err)
			} else {
				_, err := (&envoyProxy{client: c}).SetupIngress(t.Context(), spec, egressResult{})
				require.NoError(t, err)
			}
			require.True(t, created)
		})
	}
}

func TestPodmanHostPolicyRenderers(t *testing.T) {
	t.Parallel()
	addresses := []netip.Addr{netip.MustParseAddr("192.0.2.9"), netip.MustParseAddr("2001:db8::9")}
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("allow=%t", allow), func(t *testing.T) {
			t.Parallel()
			spec := proxySpec{GatewayIPs: addresses, AllowDockerGateway: allow,
				Permissions: &permissions.NetworkPermissions{Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{podmanGatewayHostname}, AllowPort: []int{443},
				}}}
			file, err := createTempEgressSquidConf(spec.Permissions, "proxy", allow, addresses)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.Remove(file)) })
			data, err := os.ReadFile(file)
			require.NoError(t, err)
			text := string(data)
			require.Contains(t, text, "acl docker_gateway_ip dst 192.0.2.9 2001:db8::9\n")
			require.Contains(t, text, "host.docker.internal gateway.docker.internal host.containers.internal\n")
			action := "deny"
			if allow {
				action = "allow"
			}
			for _, acl := range []string{"docker_gateway_hosts", "docker_gateway_ip"} {
				rule := "http_access " + action + " " + acl + "\n"
				require.Contains(t, text, rule, "gateway rule must not depend on the port ACL")
				require.Less(t, strings.Index(text, rule), strings.Index(text, "http_access allow allowed_"))
			}
			filters := buildEgressHTTPFilters(spec)
			rbac, ok := filters[0].TypedConfig.(*envoyHTTPRBAC)
			require.True(t, ok)
			require.Equal(t, strings.ToUpper(action), rbac.Rules.Action)
			patterns := collectAuthorityRegexes(rbac)
			for _, authority := range []string{
				"host.containers.internal:5432", "HOST.CONTAINERS.INTERNAL.:5432", "192.0.2.9:5432", "[2001:db8::9]:5432",
			} {
				require.True(t, authorityMatched(patterns, authority), authority)
			}
			cache := buildDFPDNSCache(spec)
			if allow {
				require.Nil(t, cache.ResolvedAddressFilter)
			} else {
				require.Equal(t, &envoyAddressMatcher{Ranges: []envoyCIDRRange{
					{AddressPrefix: "192.0.2.9", PrefixLen: 32}, {AddressPrefix: "2001:db8::9", PrefixLen: 128},
				}}, cache.ResolvedAddressFilter)
			}
		})
	}
}

// Opt-in native check: only uniquely named, test-owned bridge networks and
// discovery containers are created. No existing resources are removed or changed.
func TestPodmanHostDiscoveryNative(t *testing.T) {
	t.Parallel()
	if os.Getenv("TOOLHIVE_TEST_PODMAN_DISCOVERY") != "1" {
		t.Skip("set TOOLHIVE_TEST_PODMAN_DISCOVERY=1 to exercise native Podman discovery")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	c, err := NewClient(ctx)
	require.NoError(t, err)
	require.Equal(t, runtime.TypePodman, c.runtimeType)
	t.Cleanup(func() { require.NoError(t, c.client.Close()) })
	var internalNetwork string
	endpoints := make(map[string]*network.EndpointSettings)
	for _, internal := range []bool{false, true} {
		name := "toolhive-discovery-test-" + uuid.NewString()
		if internal {
			internalNetwork = name
		}
		created, err := c.client.NetworkCreate(ctx, name, mobyclient.NetworkCreateOptions{Driver: "bridge", Internal: internal})
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			_, err := c.client.NetworkRemove(cleanup, created.ID, mobyclient.NetworkRemoveOptions{})
			require.NoError(t, err)
		})
		endpoints[name] = &network.EndpointSettings{}
	}
	// Use the installed DNS image without pulling or moving an existing tag.
	_, err = c.client.ImageInspect(ctx, DnsImage)
	require.NoError(t, err)
	c.imageManager = &fakeImageManager{pulledImages: map[string]struct{}{DnsImage: {}}}
	dnsName := "toolhive-discovery-dns-" + uuid.NewString()
	id, _, err := c.createDnsContainer(ctx, dnsName, false, internalNetwork, endpoints)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_, err := c.client.ContainerRemove(cleanup, id, mobyclient.ContainerRemoveOptions{Force: true})
		require.NoError(t, err)
	})
	addresses, err := c.discoverHostAliases(ctx, id)
	require.NoError(t, err)
	require.NotEmpty(t, addresses)
	gateways, err := c.discoverBridgeGateways(ctx)
	require.NoError(t, err)
	t.Logf("running DNS sidecar: host aliases %v; all bridge gateways %v", addresses, gateways)
}

func TestHostArchiveReadError(t *testing.T) {
	t.Parallel()
	_, err := parseHostAliasArchive(io.MultiReader(bytes.NewReader(nil), failingHostsReader{}))
	require.ErrorContains(t, err, "read hosts archive")
}

type failingHostsReader struct{}

func (failingHostsReader) Read([]byte) (int, error) { return 0, errors.New("archive read failed") }

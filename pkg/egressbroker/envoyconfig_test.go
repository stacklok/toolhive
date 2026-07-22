// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker_test

import (
	"context"
	"net"
	"testing"
	"time"

	envoytls "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoydiscovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

func testParams() egressbroker.EnvoyConfigParams {
	return egressbroker.EnvoyConfigParams{
		ExtAuthzAddress:  "127.0.0.1",
		ExtAuthzPort:     9001,
		ProxyPort:        15001,
		AllowedHosts:     []string{"api.github.com", "*.example.com"},
		ScanFailOpen:     true,
		ScanMaxBodyBytes: egressbroker.DefaultScanMaxBodyBytes,
	}
}

func render(t *testing.T, params egressbroker.EnvoyConfigParams) map[string]any {
	t.Helper()
	data, err := egressbroker.RenderEnvoyBootstrap(params)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(data, &doc), "rendered bootstrap must be valid YAML")
	return doc
}

// dig walks nested maps/slices: dig(doc, "static_resources", "listeners").
func dig(t *testing.T, node any, path ...string) any {
	t.Helper()
	cur := node
	for _, key := range path {
		m, ok := cur.(map[string]any)
		require.True(t, ok, "path %q: expected map at %q, got %T", path, key, cur)
		cur, ok = m[key]
		require.True(t, ok, "path %q: missing key %q", path, key)
	}
	return cur
}

func TestRenderEnvoyBootstrap(t *testing.T) {
	t.Parallel()

	t.Run("validation fails loudly", func(t *testing.T) {
		t.Parallel()
		for _, params := range []egressbroker.EnvoyConfigParams{
			{ExtAuthzPort: 9001, ProxyPort: 15001, AllowedHosts: []string{"a.example.com"}},
			{ExtAuthzAddress: "127.0.0.1", ProxyPort: 15001, AllowedHosts: []string{"a.example.com"}},
			{ExtAuthzAddress: "127.0.0.1", ExtAuthzPort: 9001, AllowedHosts: []string{"a.example.com"}},
			{ExtAuthzAddress: "127.0.0.1", ExtAuthzPort: 9001, ProxyPort: 15001},
		} {
			_, err := egressbroker.RenderEnvoyBootstrap(params)
			require.Error(t, err)
		}
	})

	t.Run("bootstrap is deterministic regardless of host order", func(t *testing.T) {
		t.Parallel()
		a, err := egressbroker.RenderEnvoyBootstrap(testParams())
		require.NoError(t, err)
		p2 := testParams()
		p2.AllowedHosts = []string{"*.example.com", "api.github.com"}
		b, err := egressbroker.RenderEnvoyBootstrap(p2)
		require.NoError(t, err)
		assert.Equal(t, string(a), string(b))
	})

	t.Run("listener has tls_inspector and a loopback explicit-proxy address", func(t *testing.T) {
		t.Parallel()
		doc := render(t, testParams())

		listeners := dig(t, doc, "static_resources", "listeners").([]any)
		require.Len(t, listeners, 1)
		listener := listeners[0].(map[string]any)

		addr := dig(t, listener, "address", "socket_address").(map[string]any)
		assert.Equal(t, "127.0.0.1", addr["address"])
		assert.Equal(t, 15001, addr["port_value"])

		lfs := listener["listener_filters"].([]any)
		require.Len(t, lfs, 1)
		assert.Equal(t, "envoy.filters.listener.tls_inspector", lfs[0].(map[string]any)["name"])
	})

	t.Run("filter chain TLS-bumps via on-demand SDS from the broker cluster", func(t *testing.T) {
		t.Parallel()
		doc := render(t, testParams())

		chains := dig(t, doc, "static_resources", "listeners").([]any)[0].(map[string]any)["filter_chains"].([]any)
		require.Len(t, chains, 1)
		chain := chains[0].(map[string]any)

		// Network filters: sni_dynamic_forward_proxy then the HCM.
		filters := chain["filters"].([]any)
		require.Len(t, filters, 2)
		assert.Equal(t, "envoy.filters.network.sni_dynamic_forward_proxy", filters[0].(map[string]any)["name"])
		assert.Equal(t, "envoy.filters.network.http_connection_manager", filters[1].(map[string]any)["name"])

		// Downstream transport socket: no static certs — on-demand SDS only,
		// sourced from the broker's gRPC cluster. This is the wiring that lets
		// Envoy fetch a per-SNI bump cert for allowlisted SNI.
		ts := dig(t, chain, "transport_socket", "typed_config").(map[string]any)
		assert.Equal(t,
			"type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext", ts["@type"])
		sdsCfgs := dig(t, ts, "common_tls_context", "tls_certificate_sds_secret_configs").([]any)
		require.Len(t, sdsCfgs, 1)
		sds := dig(t, sdsCfgs[0], "sds_config", "api_config_source").(map[string]any)
		assert.Equal(t, "GRPC", sds["api_type"])
		services := sds["grpc_services"].([]any)
		require.Len(t, services, 1)
		cluster := dig(t, services[0], "envoy_grpc").(map[string]any)["cluster_name"]
		assert.Equal(t, "egress_broker", cluster)
		// Crucially: no inline tls_certificates — certs come from SDS only.
		assert.NotContains(t, dig(t, ts, "common_tls_context").(map[string]any), "tls_certificates")
	})

	t.Run("ext_authz fails closed and points at the broker cluster", func(t *testing.T) {
		t.Parallel()
		doc := render(t, testParams())
		chain := dig(t, doc, "static_resources", "listeners").([]any)[0].(map[string]any)["filter_chains"].([]any)[0]
		hcm := dig(t, chain, "filters").([]any)[1].(map[string]any)
		httpFilters := dig(t, hcm, "typed_config", "http_filters").([]any)
		require.Len(t, httpFilters, 4)

		assert.Equal(t, "envoy.filters.http.lua", httpFilters[0].(map[string]any)["name"],
			"the Lua correlation filter runs BEFORE ext_authz/ext_proc")
		extAuthz := dig(t, httpFilters[1], "typed_config").(map[string]any)
		assert.Equal(t, false, extAuthz["failure_mode_allow"], "ext_authz must deny when the broker is down")
		cluster := dig(t, extAuthz, "grpc_service", "envoy_grpc").(map[string]any)["cluster_name"]
		assert.Equal(t, "egress_broker", cluster)
		assert.Equal(t, "envoy.filters.http.ext_proc", httpFilters[2].(map[string]any)["name"],
			"ext_proc runs after ext_authz (D6c)")
		assert.Equal(t, "envoy.filters.http.router", httpFilters[3].(map[string]any)["name"])
	})

	t.Run("lua filter renders the D6c correlation metadata copy", func(t *testing.T) {
		t.Parallel()
		doc := render(t, testParams())
		chain := dig(t, doc, "static_resources", "listeners").([]any)[0].(map[string]any)["filter_chains"].([]any)[0]
		hcm := dig(t, chain, "filters").([]any)[1].(map[string]any)
		httpFilters := dig(t, hcm, "typed_config", "http_filters").([]any)
		lua := dig(t, httpFilters[0], "typed_config").(map[string]any)
		assert.Equal(t, "type.googleapis.com/envoy.extensions.filters.http.lua.v3.Lua", lua["@type"])
		code := dig(t, lua, "default_source_code").(map[string]any)["inline_string"].(string)
		assert.Contains(t, code, `metadata:set("io.toolhive.egress", "request_id"`)
		assert.Contains(t, code, `headers():get("x-request-id")`)
		assert.Contains(t, code, `metadata:set("io.toolhive.egress", "host"`)
		assert.Contains(t, code, `headers():get(":authority")`)
	})

	t.Run("ext_proc scans response headers+body only, fail-open default, 2s timeout (D6c)", func(t *testing.T) {
		t.Parallel()
		doc := render(t, testParams())
		chain := dig(t, doc, "static_resources", "listeners").([]any)[0].(map[string]any)["filter_chains"].([]any)[0]
		hcm := dig(t, chain, "filters").([]any)[1].(map[string]any)
		httpFilters := dig(t, hcm, "typed_config", "http_filters").([]any)
		extProc := dig(t, httpFilters[2], "typed_config").(map[string]any)

		assert.Equal(t, true, extProc["failure_mode_allow"],
			"the documented fail-open default passes responses when the scanner is down")
		assert.Equal(t, "2s", extProc["message_timeout"])
		cluster := dig(t, extProc, "grpc_service", "envoy_grpc").(map[string]any)["cluster_name"]
		assert.Equal(t, "egress_broker", cluster)

		mode := extProc["processing_mode"].(map[string]any)
		assert.Equal(t, "SKIP", mode["request_header_mode"], "request phase is a pass-through")
		assert.Equal(t, "NONE", mode["request_body_mode"])
		assert.Equal(t, "SKIP", mode["request_trailer_mode"])
		assert.Equal(t, "SEND", mode["response_header_mode"])
		assert.Equal(t, "BUFFERED", mode["response_body_mode"], "response body buffered for scanning")
		assert.Equal(t, "SKIP", mode["response_trailer_mode"])

		// The D6c correlation metadata namespace is forwarded to ext_proc.
		namespaces := dig(t, extProc, "metadata_options", "forwarding_namespaces", "typed").([]any)
		assert.Contains(t, namespaces, "io.toolhive.egress")
	})

	t.Run("fail-closed scan config flips ext_proc failure_mode_allow to false", func(t *testing.T) {
		t.Parallel()
		params := testParams()
		params.ScanFailOpen = false
		doc := render(t, params)
		chain := dig(t, doc, "static_resources", "listeners").([]any)[0].(map[string]any)["filter_chains"].([]any)[0]
		hcm := dig(t, chain, "filters").([]any)[1].(map[string]any)
		extProc := dig(t, hcm, "typed_config", "http_filters").([]any)[2].(map[string]any)["typed_config"].(map[string]any)
		assert.Equal(t, false, extProc["failure_mode_allow"],
			"fail-closed tenants get 502s on scanner unavailability")
		// ext_authz posture never changes.
		extAuthz := dig(t, hcm, "typed_config", "http_filters").([]any)[1].(map[string]any)["typed_config"].(map[string]any)
		assert.Equal(t, false, extAuthz["failure_mode_allow"])
	})

	t.Run("request-id extension renders so x-request-id exists at ext_authz time", func(t *testing.T) {
		t.Parallel()
		doc := render(t, testParams())
		chain := dig(t, doc, "static_resources", "listeners").([]any)[0].(map[string]any)["filter_chains"].([]any)[0]
		hcm := dig(t, chain, "filters").([]any)[1].(map[string]any)["typed_config"].(map[string]any)
		ridExt := dig(t, hcm, "request_id_extension", "typed_config").(map[string]any)
		assert.Equal(t,
			"type.googleapis.com/envoy.extensions.request_id.uuid.v3.UuidRequestIdConfig", ridExt["@type"])
	})

	t.Run("client-supplied x-request-id is never trusted (no accept_from_client; default pinned)", func(t *testing.T) {
		t.Parallel()
		// The pinned Envoy version (v1.36 / go-control-plane v1.37) has NO
		// request_id_config/accept_from_client field on the HCM — the trust
		// knob is preserve_external_request_id, whose default (false) discards
		// any downstream-supplied x-request-id and generates a fresh one. This
		// test pins BOTH that the field is absent and that the template does
		// not opt back into preserving client ids, so a future config edit or
		// Envoy upgrade that re-enables client-supplied ids fails loudly here.
		data, err := egressbroker.RenderEnvoyBootstrap(testParams())
		require.NoError(t, err)
		assert.NotContains(t, string(data), "preserve_external_request_id",
			"never opt into keeping client-supplied request ids (collision poisoning / "+
				"premature-consumption evasion against the D6c correlation map)")
		doc := render(t, testParams())
		chain := dig(t, doc, "static_resources", "listeners").([]any)[0].(map[string]any)["filter_chains"].([]any)[0]
		hcm := dig(t, chain, "filters").([]any)[1].(map[string]any)["typed_config"].(map[string]any)
		assert.NotContains(t, hcm, "preserve_external_request_id")
		assert.NotContains(t, hcm, "request_id_config",
			"no accept_from_client exists on the pinned HCM; if this fails after an Envoy bump, "+
				"set request_id_config.accept_from_client: false explicitly")
	})

	t.Run("routes carry no D6c filter_metadata (Lua sets it; route values are stored literally)", func(t *testing.T) {
		t.Parallel()
		data, err := egressbroker.RenderEnvoyBootstrap(testParams())
		require.NoError(t, err)
		assert.NotContains(t, string(data), "%REQ(",
			"route filter_metadata performs no formatter substitution — %REQ% values would be "+
				"stored literally and every scanner lookup would miss")
		doc := render(t, testParams())
		chain := dig(t, doc, "static_resources", "listeners").([]any)[0].(map[string]any)["filter_chains"].([]any)[0]
		vhosts := dig(t, chain, "filters").([]any)[1].(map[string]any)
		vl := dig(t, vhosts, "typed_config", "route_config", "virtual_hosts").([]any)
		for _, v := range vl {
			routes := v.(map[string]any)["routes"].([]any)
			require.Len(t, routes, 2)
			assert.NotContains(t, routes[1], "metadata",
				"the D6c correlation metadata comes from the Lua filter, not route filter_metadata")
		}
	})

	t.Run("routes cover exactly the allowlisted hosts; CONNECT terminates with upgrade config", func(t *testing.T) {
		t.Parallel()
		doc := render(t, testParams())
		chain := dig(t, doc, "static_resources", "listeners").([]any)[0].(map[string]any)["filter_chains"].([]any)[0]
		vhostList := dig(t, chain, "filters").([]any)[1].(map[string]any)
		vl := dig(t, vhostList, "typed_config", "route_config", "virtual_hosts").([]any)
		require.Len(t, vl, 2, "exactly one virtual host per allowlisted host pattern")
		domains := map[string]bool{}
		for _, v := range vl {
			vh := v.(map[string]any)
			for _, d := range vh["domains"].([]any) {
				domains[d.(string)] = true
			}
			// CONNECT route terminates the tunnel (TLS-bump data path) and
			// re-originates through the dynamic forward proxy.
			routes := vh["routes"].([]any)
			require.NotEmpty(t, routes)
			connect := routes[0].(map[string]any)
			upgrade := dig(t, connect, "route", "upgrade_configs").([]any)[0].(map[string]any)
			assert.Equal(t, "CONNECT", upgrade["upgrade_type"])
			assert.Equal(t, true, dig(t, upgrade, "connect_config").(map[string]any)["terminate_connect"])
			assert.Equal(t, "dynamic_forward_proxy_cluster",
				dig(t, connect, "route").(map[string]any)["cluster"])
		}
		assert.True(t, domains["api.github.com"])
		assert.True(t, domains["*.example.com"])
		assert.False(t, domains["evil.com"], "non-allowlisted hosts must have no route")
	})

	t.Run("upstream cluster re-encrypts with system-CA validation", func(t *testing.T) {
		t.Parallel()
		doc := render(t, testParams())
		clusters := dig(t, doc, "static_resources", "clusters").([]any)
		var dfp map[string]any
		for _, c := range clusters {
			if c.(map[string]any)["name"] == "dynamic_forward_proxy_cluster" {
				dfp = c.(map[string]any)
			}
		}
		require.NotNil(t, dfp)
		ts := dig(t, dfp, "transport_socket", "typed_config").(map[string]any)
		assert.Equal(t,
			"type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext", ts["@type"])
		ca := dig(t, ts, "common_tls_context", "validation_context", "trusted_ca").(map[string]any)
		assert.Equal(t, "/etc/ssl/certs/ca-certificates.crt", ca["filename"],
			"upstream certs must validate against the system bundle, never the bump CA")
	})

	t.Run("no redirect-following filter exists (D6a)", func(t *testing.T) {
		t.Parallel()
		data, err := egressbroker.RenderEnvoyBootstrap(testParams())
		require.NoError(t, err)
		assert.NotContains(t, string(data), "redirect")
		assert.NotContains(t, string(data), "internal_redirect")
	})

	t.Run("bootstrap carries no secret material", func(t *testing.T) {
		t.Parallel()
		data, err := egressbroker.RenderEnvoyBootstrap(testParams())
		require.NoError(t, err)
		assert.NotContains(t, string(data), "private_key")
		assert.NotContains(t, string(data), "BEGIN ")
	})
}

// TestSDSServesCertsForAllowlistedSNIOnly exercises the SDS server the
// bootstrap points at: Envoy's on-demand fetch ("host:<SNI>") yields a minted
// cert for allowlisted SNI and is refused for anything else (handshake then
// fails closed).
func TestSDSServesCertsForAllowlistedSNIOnly(t *testing.T) {
	t.Parallel()

	policy, err := egressbroker.ParsePolicy([]byte(`
providers:
- provider: github
  allowedHosts: ["api.github.com"]
  allowedMethods: ["GET"]
dialAllowlist: ["203.0.113.10/32"]
`))
	require.NoError(t, err)
	ca, err := egressbroker.GenerateBumpCA("test-bump-ca", time.Now())
	require.NoError(t, err)
	sds, err := egressbroker.NewSecretDiscoveryServer(ca, policy)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("allowlisted SNI yields a TLS certificate secret", func(t *testing.T) {
		t.Parallel()
		resp, err := sds.FetchSecrets(ctx, &envoydiscovery.DiscoveryRequest{
			ResourceNames: []string{"host:api.github.com"},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetResources(), 1)
		var secret envoytls.Secret
		// The resource is an envoy Secret; verify it carries cert+key.
		require.NoError(t, resp.GetResources()[0].UnmarshalTo(&secret))
		assert.Equal(t, "host:api.github.com", secret.GetName())
		assert.NotEmpty(t, secret.GetTlsCertificate().GetCertificateChain().GetInlineBytes())
		assert.NotEmpty(t, secret.GetTlsCertificate().GetPrivateKey().GetInlineBytes())
	})

	t.Run("non-allowlisted SNI is refused (no cert, handshake fails closed)", func(t *testing.T) {
		t.Parallel()
		_, err := sds.FetchSecrets(ctx, &envoydiscovery.DiscoveryRequest{
			ResourceNames: []string{"host:evil.com"},
		})
		require.Error(t, err)
	})

	t.Run("malformed resource names are refused", func(t *testing.T) {
		t.Parallel()
		_, err := sds.FetchSecrets(ctx, &envoydiscovery.DiscoveryRequest{
			ResourceNames: []string{"ca-key"},
		})
		require.Error(t, err)
		_, err = sds.FetchSecrets(ctx, &envoydiscovery.DiscoveryRequest{})
		require.Error(t, err)
	})
}

// TestBrokerClusterIsLoopback asserts the rendered broker cluster (ext_authz +
// SDS) never leaves the pod.
func TestBrokerClusterIsLoopback(t *testing.T) {
	t.Parallel()
	doc := render(t, testParams())
	clusters := dig(t, doc, "static_resources", "clusters").([]any)
	for _, c := range clusters {
		cm := c.(map[string]any)
		if cm["name"] != "egress_broker" {
			continue
		}
		endpoints := dig(t, cm, "load_assignment", "endpoints").([]any)
		for _, e := range endpoints {
			for _, lb := range e.(map[string]any)["lb_endpoints"].([]any) {
				addr := dig(t, lb, "endpoint", "address", "socket_address").(map[string]any)["address"].(string)
				require.True(t, net.ParseIP(addr).IsLoopback(), "broker cluster endpoint %s must be loopback", addr)
			}
		}
	}
}

// Example output guard: the rendered bootstrap mentions the SDS resource
// prefix the SDS server expects ("host:") nowhere — the resource NAME is
// derived from SNI at runtime — but the fetch wiring must be present.
func TestBootstrapMentionsSDSFetchWiring(t *testing.T) {
	t.Parallel()
	data, err := egressbroker.RenderEnvoyBootstrap(testParams())
	require.NoError(t, err)
	for _, want := range []string{
		"tls_certificate_sds_secret_configs",
		"tls_inspector",
		"sni_dynamic_forward_proxy",
		"terminate_connect: true",
		"failure_mode_allow: false",
	} {
		assert.Contains(t, string(data), want)
	}
}

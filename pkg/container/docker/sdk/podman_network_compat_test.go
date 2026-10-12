// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestPodmanNetworkCompatRoundTripperListDecoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantGateway string
		wantError   string
	}{
		{
			name: "gatewayless network alongside valid gateway",
			body: `[
				{"Id":"gatewayless","Driver":"bridge","IPAM":{"Config":[{"Gateway":"<nil>"}]}},
				{"Id":"routable","Driver":"bridge","IPAM":{"Config":[{"Gateway":"10.89.2.1"}]}}
			]`,
			wantGateway: "10.89.2.1",
		},
		{
			name: "malformed non-absence value remains invalid",
			body: `[
				{"Id":"malformed","Driver":"bridge","IPAM":{"Config":[{"Gateway":"not-an-ip"}]}}
			]`,
			wantError: "unable to parse IP",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("/v1.40/networks", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, tt.body); err != nil {
					t.Errorf("write network list response: %v", err)
				}
			})
			client := newPodmanCompatTestClient(t, mux)

			networks, err := client.NetworkList(t.Context(), mobyclient.NetworkListOptions{})
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}

			require.NoError(t, err)
			require.Len(t, networks.Items, 2)
			require.False(t, networks.Items[0].IPAM.Config[0].Gateway.IsValid())
			require.Equal(t, tt.wantGateway, networks.Items[1].IPAM.Config[0].Gateway.String())
		})
	}
}

func TestPodmanNetworkCompatRoundTripperInspectDecoding(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1.40/networks/gatewayless", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w,
			`{"Id":"gatewayless","Driver":"bridge","IPAM":{"Config":[{"Gateway":"<nil>"}]}}`); err != nil {
			t.Errorf("write network inspect response: %v", err)
		}
	})
	client := newPodmanCompatTestClient(t, mux)

	inspected, err := client.NetworkInspect(t.Context(), "gatewayless", mobyclient.NetworkInspectOptions{})
	require.NoError(t, err)
	require.Len(t, inspected.Network.IPAM.Config, 1)
	require.False(t, inspected.Network.IPAM.Config[0].Gateway.IsValid())
}

func TestPodmanNetworkCompatRoundTripperForwardsCloseIdleConnections(t *testing.T) {
	t.Parallel()

	var connectionCount atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, `[]`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connectionCount.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)

	client := server.Client()
	client.Transport = newPodmanNetworkCompatRoundTripper(client.Transport)
	request := func() {
		resp, err := client.Get(server.URL + "/v1.40/networks")
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}

	request()
	client.CloseIdleConnections()
	request()

	require.EqualValues(t, 2, connectionCount.Load())
}

func newPodmanCompatTestClient(t *testing.T, handler http.Handler) *mobyclient.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	httpClient := server.Client()
	httpClient.Transport = newPodmanNetworkCompatRoundTripper(httpClient.Transport)
	client, err := mobyclient.New(
		mobyclient.WithHost("tcp://"+serverURL.Host),
		mobyclient.WithHTTPClient(httpClient),
		mobyclient.WithAPIVersion("1.40"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func TestNormalizePodmanNetworkResponseIgnoresUnrelatedGatewayFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{"Gateway":"<nil>","IPAM":{"Config":[{"Gateway":"10.0.0.1"}]}}`)
	require.Equal(t, body, normalizePodmanNetworkResponse(body))
}

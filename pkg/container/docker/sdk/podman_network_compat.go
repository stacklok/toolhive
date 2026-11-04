// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/stacklok/toolhive/pkg/networking"
)

// podmanNetworkCompatRoundTripper normalizes Podman's Docker-compatible
// representation of a gatewayless network before the Moby client decodes it.
// Podman serializes a missing net.IP as the string "<nil>", which netip.Addr
// correctly rejects as malformed. Only that exact sentinel is treated as an
// absent gateway; all other values are left for Moby's strict decoder.
type podmanNetworkCompatRoundTripper struct {
	base http.RoundTripper
}

func newPodmanNetworkCompatRoundTripper(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &podmanNetworkCompatRoundTripper{base: base}
}

func (t *podmanNetworkCompatRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.Body == nil || req.Method != http.MethodGet ||
		resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices ||
		!isNetworkAPIPath(req.URL.Path) {
		return resp, err
	}

	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read Podman network response: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close Podman network response: %w", closeErr)
	}

	body = normalizePodmanNetworkResponse(body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	if resp.Header != nil {
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	return resp, nil
}

func (t *podmanNetworkCompatRoundTripper) CloseIdleConnections() {
	networking.ForwardCloseIdle(t.base)
}

var _ networking.IdleConnectionCloser = (*podmanNetworkCompatRoundTripper)(nil)

func normalizePodmanNetworkResponse(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return body
	}

	switch trimmed[0] {
	case '[':
		var networks []json.RawMessage
		if err := json.Unmarshal(body, &networks); err != nil {
			return body
		}

		changed := false
		for i, rawNetwork := range networks {
			normalized, networkChanged := normalizePodmanNetwork(rawNetwork)
			if networkChanged {
				networks[i] = normalized
				changed = true
			}
		}
		if !changed {
			return body
		}
		normalized, err := json.Marshal(networks)
		if err != nil {
			return body
		}
		return normalized
	case '{':
		normalized, changed := normalizePodmanNetwork(body)
		if changed {
			return normalized
		}
	}
	return body
}

func normalizePodmanNetwork(rawNetwork json.RawMessage) (json.RawMessage, bool) {
	var networkFields map[string]json.RawMessage
	if err := json.Unmarshal(rawNetwork, &networkFields); err != nil {
		return rawNetwork, false
	}

	rawIPAM, ok := networkFields["IPAM"]
	if !ok {
		return rawNetwork, false
	}
	normalizedIPAM, changed := normalizePodmanIPAM(rawIPAM)
	if !changed {
		return rawNetwork, false
	}

	networkFields["IPAM"] = normalizedIPAM
	normalizedNetwork, err := json.Marshal(networkFields)
	if err != nil {
		return rawNetwork, false
	}
	return normalizedNetwork, true
}

func normalizePodmanIPAM(rawIPAM json.RawMessage) (json.RawMessage, bool) {
	var ipamFields map[string]json.RawMessage
	if err := json.Unmarshal(rawIPAM, &ipamFields); err != nil {
		return rawIPAM, false
	}

	rawConfigs, ok := ipamFields["Config"]
	if !ok {
		return rawIPAM, false
	}
	var configs []json.RawMessage
	if err := json.Unmarshal(rawConfigs, &configs); err != nil {
		return rawIPAM, false
	}

	changed := false
	for i, rawConfig := range configs {
		var configFields map[string]json.RawMessage
		if err := json.Unmarshal(rawConfig, &configFields); err != nil {
			return rawIPAM, false
		}
		rawGateway, ok := configFields["Gateway"]
		if !ok {
			continue
		}
		var gateway string
		if err := json.Unmarshal(rawGateway, &gateway); err != nil || gateway != "<nil>" {
			continue
		}

		configFields["Gateway"] = json.RawMessage("null")
		normalizedConfig, err := json.Marshal(configFields)
		if err != nil {
			return rawIPAM, false
		}
		configs[i] = normalizedConfig
		changed = true
	}
	if !changed {
		return rawIPAM, false
	}

	normalizedConfigs, err := json.Marshal(configs)
	if err != nil {
		return rawIPAM, false
	}
	ipamFields["Config"] = normalizedConfigs
	normalizedIPAM, err := json.Marshal(ipamFields)
	if err != nil {
		return rawIPAM, false
	}
	return normalizedIPAM, true
}

func isNetworkAPIPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 0 && strings.HasPrefix(parts[0], "v") {
		parts = parts[1:]
	}
	return len(parts) > 0 && parts[0] == "networks"
}

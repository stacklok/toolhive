// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package llmgateway contains shared types for the LLM gateway feature,
// imported by both pkg/llm and pkg/client to avoid an import cycle.
package llmgateway

import "net/url"

// ProxyOriginOf returns rawURL with its path, query, and fragment stripped so
// only the scheme and host remain (the "origin"). Tools like Gemini CLI that
// append their own API path (e.g. /v1beta/...) need the origin rather than the
// full proxy base URL to avoid doubled path segments. Falls back to rawURL when
// parsing fails.
func ProxyOriginOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil {
		return rawURL
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// ApplyConfig holds the values needed to configure a single tool's LLM
// gateway settings. Using a struct prevents positional-argument mistakes when
// the caller has multiple similar string values in scope.
type ApplyConfig struct {
	GatewayURL         string // direct-mode: URL of the upstream LLM gateway
	ProxyBaseURL       string // proxy-mode: URL of the localhost reverse proxy
	TokenHelperCommand string // direct-mode: shell command that prints a fresh token
	TLSSkipVerify      bool   // when true, instruct the tool to skip TLS verification
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	envoycore "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
)

// Correlation plumbing between ext_authz (injection) and ext_proc (response
// scan), ADR D6c: the Envoy-generated x-request-id is the shared key. It is
// present at ext_authz time (request headers); on the response path the
// allowlisted routes set dynamic metadata (namespace metadataNamespace, key
// metadataKeyRequestID) from %REQ(X-REQUEST-ID)%, which ext_proc forwards per
// its metadata_options.forwarding_namespaces config (the response-side
// ext_proc cannot see request headers).
const (
	// requestIDHeader is the Envoy request-id header name (canonical
	// lowercase, as HTTP/2 header maps are).
	requestIDHeader = "x-request-id"

	// metadataNamespace is the dynamic-metadata namespace carrying the
	// correlation values from the route to ext_proc.
	metadataNamespace = "io.toolhive.egress"
	// metadataKeyRequestID is the metadata key holding the x-request-id copy.
	metadataKeyRequestID = "request_id"
	// metadataKeyHost is the metadata key holding the request host (for audit).
	metadataKeyHost = "host"
)

// metadataString extracts a string value from ext_proc-forwarded dynamic
// metadata: metadata.filter_metadata[metadataNamespace].fields[key].
func metadataString(meta *envoycore.Metadata, key string) string {
	if meta == nil {
		return ""
	}
	ns, ok := meta.GetFilterMetadata()[metadataNamespace]
	if !ok {
		return ""
	}
	return ns.GetFields()[key].GetStringValue()
}

// requestIDFromMetadata returns the route-forwarded x-request-id copy.
func requestIDFromMetadata(meta *envoycore.Metadata) string {
	return metadataString(meta, metadataKeyRequestID)
}

// requestHostFromMetadata returns the route-forwarded request host (audit
// context only — never a routing decision).
func requestHostFromMetadata(meta *envoycore.Metadata) string {
	return metadataString(meta, metadataKeyHost)
}

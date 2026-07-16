// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package providers

import "go.opentelemetry.io/otel/attribute"

// Emitter-ownership resource attributes (RFC metrics-standardization, D8).
//
// Each ToolHive service tags its OTel resource with a vendor-owned component
// identity and the shared product marker. The Prometheus exporter promotes
// these two attributes to per-series labels (see the Prometheus reader's
// WithResourceAsConstantLabels) so a single selector,
// stacklok_product="stacklok-enterprise", matches both our stacklok.* metrics
// and the unprefixed semconv (mcp.*, http.*) ones.
//
// These live here, not in the OSS toolhive-core shared metrics package: the
// product marker names the enterprise product and the component values are the
// internal service roster, neither of which belongs in a public shared library.
// toolhive-core exports only vendor-neutral primitives (bucket presets, canonical
// per-series label keys). Each consuming service (proxy, vMCP, registry) owns
// its own copy of these ownership attributes.
const (
	// AttrStacklokComponent is the resource-attribute key identifying the emitting
	// component. The Prometheus exporter renders it as the stacklok_component label.
	AttrStacklokComponent = "stacklok.component"

	// AttrStacklokProduct is the resource-attribute key identifying the product.
	// The Prometheus exporter renders it as the stacklok_product label.
	AttrStacklokProduct = "stacklok.product"

	// ProductStacklokEnterprise is the shared value for AttrStacklokProduct across
	// every in-scope service. It is the cross-component selector value.
	ProductStacklokEnterprise = "stacklok-enterprise"

	// ComponentToolhive is the AttrStacklokComponent value for the ToolHive proxy
	// and API server.
	ComponentToolhive = "toolhive"

	// ComponentVMCP is the AttrStacklokComponent value for the Virtual MCP server.
	ComponentVMCP = "vmcp"
)

// stacklokResourceLabelFilter admits exactly the two emitter-ownership attribute
// keys for promotion to per-series Prometheus labels (RFC D8). It deliberately
// excludes every other resource attribute (host.*, process.*, env-provided) to
// keep series cardinality bounded.
func stacklokResourceLabelFilter() attribute.Filter {
	return attribute.NewAllowKeysFilter(AttrStacklokComponent, AttrStacklokProduct)
}

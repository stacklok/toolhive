// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package usagemetrics provides internal usage metrics for tracking usage and adoption.
package usagemetrics

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// MetricRecord is the payload sent to the metrics API
type MetricRecord struct {
	Count     int64  `json:"count"`
	Timestamp string `json:"timestamp"` // ISO 8601 format in UTC (e.g., "2025-01-01T23:50:00Z")

	// CallsByClient splits Count by the MCP client that made the calls. Keys are
	// always one of the normalized client values (see NormalizeClientName), so the
	// map holds at most a fixed, small number of entries.
	CallsByClient map[string]int64 `json:"calls_by_client,omitempty"`

	// CodeModeCalls is how many of the calls in Count invoked the vMCP code
	// mode tool (execute_tool_script). It is a subset of Count, not added to it.
	CodeModeCalls int64 `json:"code_mode_calls,omitempty"`
	// CodeModeToolCalls is how many tool calls code mode scripts made on behalf
	// of a client. Those calls never pass through the HTTP middleware, so they
	// are not part of Count.
	CodeModeToolCalls int64 `json:"code_mode_tool_calls,omitempty"`

	Profile
}

// Profile describes the server a collector reports for. Every string field holds
// one of a fixed set of values (enforced by normalize), so no field can carry
// user-defined names, URLs or other free-form data. The one exception,
// CatalogServer, is restricted to entry names of the registry embedded in the
// ToolHive binary.
type Profile struct {
	// Transport is the MCP transport: stdio, sse, streamable-http or other.
	Transport string `json:"transport,omitempty"`
	// ServerKind is container or remote.
	ServerKind string `json:"server_kind,omitempty"`
	// RegistrySource is where the server definition came from: a registry
	// (builtin, custom_remote, custom_api, custom_local), a package that thv
	// built into an image (package_uvx, package_npx, package_go), or none of
	// these (a plain image or URL).
	RegistrySource string `json:"registry_source,omitempty"`
	// CatalogServer is the built-in registry entry name, or "custom".
	CatalogServer string `json:"catalog_server,omitempty"`
	// Runtime is docker, podman, colima, kubernetes, none or other.
	Runtime string `json:"runtime,omitempty"`
	// AuthMode is the incoming authentication: none, oidc, local, embedded or other.
	AuthMode string `json:"auth_mode,omitempty"`
	// Authz is on or off.
	Authz string `json:"authz,omitempty"`

	// BackendCount is the number of backends behind a vMCP server.
	BackendCount *int `json:"backend_count,omitempty"`
	// ToolCount is the number of backend tools a vMCP server aggregates, after
	// conflict resolution and per-backend filters. It excludes composite and
	// optimizer tools and is not filtered per user, so it measures the size of
	// the deployment rather than what one client sees. Aggregation results are
	// cached per caller, so it is the count from the most recent aggregation.
	ToolCount *int `json:"tool_count,omitempty"`
	// Features lists the vMCP features in use, drawn from optimizer, code_mode,
	// composite_tools and outgoing_auth.
	Features []string `json:"features,omitempty"`
}

// Allowed profile values.
const (
	ValueOther   = "other"
	ValueNone    = "none"
	ValueCustom  = "custom"
	ValueOn      = "on"
	ValueOff     = "off"
	ValueBuiltin = "builtin"

	ServerKindContainer = "container"
	ServerKindRemote    = "remote"

	RegistrySourceCustomRemote = "custom_remote"
	RegistrySourceCustomAPI    = "custom_api"
	RegistrySourceCustomLocal  = "custom_local"
	RegistrySourcePackageUVX   = "package_uvx"
	RegistrySourcePackageNPX   = "package_npx"
	RegistrySourcePackageGo    = "package_go"

	RuntimeKubernetes = "kubernetes"

	AuthModeOIDC     = "oidc"
	AuthModeLocal    = "local"
	AuthModeEmbedded = "embedded"

	FeatureOptimizer      = "optimizer"
	FeatureCodeMode       = "code_mode"
	FeatureCompositeTools = "composite_tools"
	FeatureOutgoingAuth   = "outgoing_auth"
)

var (
	allowedTransports      = []string{"stdio", "sse", "streamable-http"}
	allowedServerKinds     = []string{ServerKindContainer, ServerKindRemote}
	allowedRegistrySources = []string{
		ValueBuiltin, RegistrySourceCustomRemote, RegistrySourceCustomAPI, RegistrySourceCustomLocal,
		RegistrySourcePackageUVX, RegistrySourcePackageNPX, RegistrySourcePackageGo, ValueNone,
	}
	allowedRuntimes  = []string{"docker", "podman", "colima", RuntimeKubernetes, ValueNone}
	allowedAuthModes = []string{ValueNone, AuthModeOIDC, AuthModeLocal, AuthModeEmbedded}
	allowedAuthz     = []string{ValueOn, ValueOff}
	allowedFeatures  = []string{FeatureOptimizer, FeatureCodeMode, FeatureCompositeTools, FeatureOutgoingAuth}
)

// normalize returns a copy of p in which every field holds an allowed value.
// Empty fields stay empty (and are omitted from the payload); any other value
// outside the allowed set becomes "other". CatalogServer is only kept for the
// builtin registry source; providers are responsible for checking the name
// against the embedded catalog.
func (p Profile) normalize() Profile {
	out := Profile{
		Transport:      oneOf(p.Transport, allowedTransports),
		ServerKind:     oneOf(p.ServerKind, allowedServerKinds),
		RegistrySource: oneOf(p.RegistrySource, allowedRegistrySources),
		Runtime:        oneOf(p.Runtime, allowedRuntimes),
		AuthMode:       oneOf(p.AuthMode, allowedAuthModes),
		Authz:          oneOf(p.Authz, allowedAuthz),
		BackendCount:   p.BackendCount,
		ToolCount:      p.ToolCount,
	}
	// Only a server from a registry has a catalog entry to report.
	if isRegistry(out.RegistrySource) {
		out.CatalogServer = ValueCustom
		if out.RegistrySource == ValueBuiltin && p.CatalogServer != "" {
			out.CatalogServer = p.CatalogServer
		}
	}
	for _, f := range p.Features {
		if slices.Contains(allowedFeatures, f) && !slices.Contains(out.Features, f) {
			out.Features = append(out.Features, f)
		}
	}
	slices.Sort(out.Features)
	return out
}

// isRegistry reports whether a registry source names a registry, as opposed
// to a package build, none or empty.
func isRegistry(source string) bool {
	switch source {
	case "", ValueNone, RegistrySourcePackageUVX, RegistrySourcePackageNPX, RegistrySourcePackageGo:
		return false
	default:
		return true
	}
}

func oneOf(v string, allowed []string) string {
	if v == "" || slices.Contains(allowed, v) {
		return v
	}
	return ValueOther
}

// ProfileProvider is implemented by runner configurations that can describe the
// server they run. The usage metrics middleware checks for it at creation time.
type ProfileProvider interface {
	UsageMetricsProfile() Profile
}

// Collector manages tool call counting and reporting
type Collector struct {
	// Unique identifier for this proxy instance (UUID)
	instanceID string
	// countsMu guards counts, whose fields change together so the per-client
	// split always adds up to the count.
	countsMu sync.Mutex
	counts   counts
	// profile returns the server profile; called on every flush so values that
	// change over time (such as vMCP backend counts) stay current. May be nil.
	profile func() Profile
	// Current date in YYYY-MM-DD format (UTC)
	currentDate string
	// HTTP client
	client *Client
	// Lifecycle management
	stopCh   chan struct{}
	doneCh   chan struct{}
	flushCh  chan struct{}
	started  atomic.Bool
	shutdown atomic.Bool
}

// counts holds the per-day tallies a report carries.
type counts struct {
	// total is the number of tool calls received from clients.
	total int64
	// byClient splits total by normalized client name.
	byClient map[string]int64
	// codeModeCalls is the subset of total that invoked execute_tool_script.
	codeModeCalls int64
	// codeModeToolCalls is the number of tool calls made from inside code mode
	// scripts. It is not part of total.
	codeModeToolCalls int64
}

func newCounts() counts {
	return counts{byClient: make(map[string]int64)}
}

func getCurrentDateUTC() string {
	return time.Now().UTC().Format("2006-01-02")
}

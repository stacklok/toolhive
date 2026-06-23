// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usage

import "time"

// clickHouseTimeFormat formats event_time as a ClickHouse DateTime-friendly,
// UTC string ("2006-01-02 15:04:05").
const clickHouseTimeFormat = "2006-01-02 15:04:05"

// Snapshot is the data contract emitted per reporting tick. Its JSON tags MUST
// match the ClickHouse `usage_events` table column names exactly, because rows
// are inserted via FORMAT JSONEachRow:
//
//	installation_id             String
//	event_time                  DateTime
//	operator_version            LowCardinality(String)
//	k8s_server_version          LowCardinality(String) DEFAULT ''
//	mcpserver_count             UInt32
//	mcpexternalauthconfig_count UInt32 DEFAULT 0
//	cloud_provider              LowCardinality(String) DEFAULT ''
//	feature_gates               Map(String, UInt8)
//
// Do not rename these JSON tags without coordinating a ClickHouse schema change.
//
// Extension points (intentionally NOT implemented in this PoC). When adding any
// of these, add the matching column to the ClickHouse schema first:
//   - TODO(usage-poc): per-transport MCPServer breakdown (stdio vs sse vs
//     streamable-http) — likely a Map(String, UInt32).
//   - TODO(usage-poc): MCPRegistry / MCPGroup counts — UInt32 each, listed via
//     the same cached client used for the existing counts.
type Snapshot struct {
	// InstallationID is the anonymous, per-installation UUID.
	InstallationID string `json:"installation_id"`
	// EventTime is the UTC timestamp of the snapshot, ClickHouse DateTime format.
	EventTime string `json:"event_time"`
	// OperatorVersion is the running operator's version string.
	OperatorVersion string `json:"operator_version"`
	// K8sServerVersion is the Kubernetes API server GitVersion; may be empty.
	K8sServerVersion string `json:"k8s_server_version"`
	// MCPServerCount is the number of MCPServer resources observed this tick.
	MCPServerCount uint32 `json:"mcpserver_count"`
	// MCPExternalAuthConfigCount is the number of MCPExternalAuthConfig resources
	// observed this tick. It is a proxy for external-auth (token exchange) usage.
	MCPExternalAuthConfigCount uint32 `json:"mcpexternalauthconfig_count"`
	// CloudProvider is a stable lowercase token for the underlying cloud
	// ("aws"/"gcp"/"azure"/"kind"/"unknown"), resolved once at startup. It is
	// "unknown" when detection is unavailable (e.g. the operator lacks node-read
	// RBAC) — see DiscoverCloudProvider.
	CloudProvider string `json:"cloud_provider"`
	// FeatureGates maps stable, snake_case operator feature-gate keys to their
	// on/off state (1=on, 0=off). It serializes to a JSON object, which
	// JSONEachRow inserts into the ClickHouse Map(String, UInt8) column.
	FeatureGates map[string]uint8 `json:"feature_gates"`
}

// newSnapshot builds a Snapshot stamping event_time as the current UTC time in
// ClickHouse DateTime format. The cloudProvider and featureGates values are
// resolved once at reporter construction and reused for every tick.
func newSnapshot(
	installationID, operatorVersion, k8sServerVersion string,
	mcpServerCount, mcpExternalAuthConfigCount uint32,
	cloudProvider string,
	featureGates map[string]uint8,
) Snapshot {
	return Snapshot{
		InstallationID:             installationID,
		EventTime:                  time.Now().UTC().Format(clickHouseTimeFormat),
		OperatorVersion:            operatorVersion,
		K8sServerVersion:           k8sServerVersion,
		MCPServerCount:             mcpServerCount,
		MCPExternalAuthConfigCount: mcpExternalAuthConfigCount,
		CloudProvider:              cloudProvider,
		FeatureGates:               featureGates,
	}
}

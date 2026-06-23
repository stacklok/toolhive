-- Usage telemetry PoC — example queries against the usage_events table.
-- Run with clickhouse-client, or POST to the HTTP interface, e.g.:
--   curl 'http://localhost:8123/?default_format=PrettyCompact' --data-binary @queries.sql
-- (run one statement at a time when using the HTTP interface).
--
-- Table columns (v2 — post schema migration):
--   installation_id             String
--   event_time                  DateTime
--   operator_version            LowCardinality(String)
--   k8s_server_version          LowCardinality(String) DEFAULT ''
--   mcpserver_count             UInt32
--   mcpexternalauthconfig_count UInt32 DEFAULT 0
--   cloud_provider              LowCardinality(String) DEFAULT ''
--   feature_gates               Map(String, UInt8)

-- ── Adoption & fleet ──────────────────────────────────────────────────────────

-- Headline demo: latest snapshot per installation.
-- Answers "which installations exist, what version/cloud are they on, how many
-- MCPServers + external-auth configs, and when did we last hear from them?"
SELECT
    installation_id,
    argMax(operator_version, event_time)            AS version,
    argMax(k8s_server_version, event_time)          AS k8s,
    argMax(cloud_provider, event_time)              AS cloud,
    argMax(mcpserver_count, event_time)             AS current_mcpservers,
    argMax(mcpexternalauthconfig_count, event_time) AS ext_auth_configs,
    max(event_time)                                  AS last_seen
FROM usage_events
GROUP BY installation_id
ORDER BY last_seen DESC;

-- Active installations over a window.
-- Answers "how many installations are currently reporting?"
SELECT
    countDistinctIf(installation_id, event_time >= now() - INTERVAL 24 HOUR) AS active_24h,
    countDistinctIf(installation_id, event_time >= now() - INTERVAL 7 DAY)   AS active_7d
FROM usage_events;

-- Total fleet MCPServer count (sum of latest-per-installation values).
SELECT sum(c) AS total_mcpservers
FROM (
    SELECT installation_id, argMax(mcpserver_count, event_time) AS c
    FROM usage_events
    GROUP BY installation_id
);

-- Installations using external auth (latest ext-auth count > 0).
SELECT countIf(c > 0) AS installs_using_ext_auth
FROM (
    SELECT installation_id, argMax(mcpexternalauthconfig_count, event_time) AS c
    FROM usage_events
    GROUP BY installation_id
);

-- ── Version & upgrade ─────────────────────────────────────────────────────────

-- Operator version distribution: installations grouped by latest operator version.
SELECT
    version,
    count() AS installations
FROM (
    SELECT installation_id, argMax(operator_version, event_time) AS version
    FROM usage_events
    GROUP BY installation_id
)
GROUP BY version
ORDER BY installations DESC;

-- Fleet size distribution by MCPServer bucket.
SELECT
    multiIf(c = 0, '0', c <= 5, '1-5', c <= 20, '6-20', '20+') AS bucket,
    count() AS installations
FROM (
    SELECT installation_id, argMax(mcpserver_count, event_time) AS c
    FROM usage_events
    GROUP BY installation_id
)
GROUP BY bucket
ORDER BY installations DESC;

-- ── Environment & features ────────────────────────────────────────────────────

-- Kubernetes version distribution (latest per installation).
SELECT
    k8s_version,
    count() AS installations
FROM (
    SELECT installation_id, argMax(k8s_server_version, event_time) AS k8s_version
    FROM usage_events
    GROUP BY installation_id
)
GROUP BY k8s_version
ORDER BY installations DESC;

-- Cloud provider distribution (latest per installation).
SELECT
    cloud,
    count() AS installations
FROM (
    SELECT installation_id, argMax(cloud_provider, event_time) AS cloud
    FROM usage_events
    GROUP BY installation_id
)
GROUP BY cloud
ORDER BY installations DESC;

-- Feature-gate adoption: how many installations have each gate ENABLED.
-- Uses ARRAY JOIN to expand the Map(String, UInt8) column.
SELECT
    gate,
    countIf(enabled = 1) AS installs_enabled,
    count()               AS installs_reporting
FROM (
    SELECT installation_id, argMax(feature_gates, event_time) AS fg
    FROM usage_events
    GROUP BY installation_id
)
ARRAY JOIN mapKeys(fg) AS gate, mapValues(fg) AS enabled
GROUP BY gate
ORDER BY installs_enabled DESC;

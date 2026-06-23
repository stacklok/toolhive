-- Usage telemetry PoC — ClickHouse schema.
--
-- Plain MergeTree on purpose: we keep RAW snapshots (one row per report interval per
-- installation) so we can chart how operator versions and MCPServer counts change over
-- time. Do NOT switch to ReplacingMergeTree — that would collapse history and defeat the
-- time-series queries in queries.sql / grafana.md.
--
-- Apply with (in-cluster):
--   curl --data-binary @schema.sql http://clickhouse.toolhive-system.svc:8123
-- or locally:
--   curl --data-binary @schema.sql http://localhost:8123

CREATE TABLE IF NOT EXISTS usage_events (
    installation_id             String,
    event_time                  DateTime,
    operator_version            LowCardinality(String),
    k8s_server_version          LowCardinality(String) DEFAULT '',
    mcpserver_count             UInt32,
    mcpexternalauthconfig_count UInt32 DEFAULT 0,
    cloud_provider              LowCardinality(String) DEFAULT '',
    feature_gates               Map(String, UInt8)
) ENGINE = MergeTree()
ORDER BY (installation_id, event_time);

-- In-place migration for an already-deployed usage_events table. ClickHouse
-- ignores ADD COLUMN IF NOT EXISTS for columns that already exist, so these are
-- safe to re-run. The deployer step runs these against the live ClickHouse.
--   ALTER TABLE usage_events ADD COLUMN IF NOT EXISTS mcpexternalauthconfig_count UInt32 DEFAULT 0;
--   ALTER TABLE usage_events ADD COLUMN IF NOT EXISTS cloud_provider LowCardinality(String) DEFAULT '';
--   ALTER TABLE usage_events ADD COLUMN IF NOT EXISTS feature_gates Map(String, UInt8);

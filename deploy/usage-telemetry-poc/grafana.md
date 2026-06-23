# Grafana wiring — Usage Telemetry PoC

Visualise the `usage_events` table in Grafana. The Helm path (see `README.md`) is
recommended — it provisions the datasource and all 12 panels automatically.

## Plugin format rules (important — do not regress)

The `grafana-clickhouse-datasource` plugin's `format` field is an enum that differs
from Grafana core's own naming:

| Panel type | `format` value | Notes |
|---|---|---|
| Stat, Pie, Bar, Table | `"table"` | |
| Time series | `"timeseries"` | **NOT** `"time_series"` (underscore) — rejected by plugin |

Time-series queries **must** include `ORDER BY <time_col> ASC`. The plugin enforces
ascending sort; descending order returns `unable to process the data because it is not
sorted in ascending order by time`.

Time-range macros `$__fromTime` / `$__toTime` work in both modes. Multi-series
time-series include a string label column between the time and numeric value columns;
the plugin pivots automatically (one field per distinct label value, annotated with
`labels.<col>`).

## Datasource configuration (manual path)

Install `grafana-clickhouse-datasource`. In Grafana:
**Connections → Data sources → Add data source → ClickHouse**.

| Field | In-cluster value |
|---|---|
| Protocol | HTTP |
| Host | `clickhouse.toolhive-system.svc` |
| Port | `8123` |
| Username | `default` |
| Password | *(empty)* |
| Default database | `default` |

## Dashboard panels (12 total)

All panels query `usage_events`. uid = `toolhive-usage-poc`, 30s auto-refresh, 3h window.

### Adoption stats (row 1)

**1 — Stat: Active Installations (window)**
```sql
SELECT countDistinct(installation_id) AS active
FROM usage_events
WHERE event_time >= $__fromTime AND event_time <= $__toTime
```
format: `table`

**2 — Stat: Total MCPServers (fleet)**
```sql
SELECT sum(c) AS total
FROM (
  SELECT installation_id, argMax(mcpserver_count, event_time) AS c
  FROM usage_events GROUP BY installation_id
)
```
format: `table`

**3 — Stat: Installs Using External Auth**
```sql
SELECT countIf(c > 0) AS n
FROM (
  SELECT installation_id, argMax(mcpexternalauthconfig_count, event_time) AS c
  FROM usage_events GROUP BY installation_id
)
```
format: `table`

**4 — Timeseries: Active Installations Over Time**
```sql
SELECT
  toStartOfInterval(event_time, INTERVAL 5 MINUTE) AS time,
  countDistinct(installation_id) AS active
FROM usage_events
WHERE event_time >= $__fromTime AND event_time <= $__toTime
GROUP BY time ORDER BY time ASC
```
format: `timeseries`

### Engagement (row 2)

**5 — Timeseries: MCPServer Count per Installation Over Time**
```sql
SELECT event_time AS time, installation_id, mcpserver_count
FROM usage_events
WHERE event_time >= $__fromTime AND event_time <= $__toTime
ORDER BY event_time ASC
```
format: `timeseries` — pivots on `installation_id` automatically.

**6 — Barchart: Fleet Size Distribution**
```sql
SELECT
  multiIf(c=0,'0',c<=5,'1-5',c<=20,'6-20','20+') AS bucket,
  count() AS installations
FROM (
  SELECT installation_id, argMax(mcpserver_count, event_time) AS c
  FROM usage_events GROUP BY installation_id
)
GROUP BY bucket ORDER BY installations DESC
```
format: `table`

### Version & upgrade (row 3)

**7 — Piechart: Operator Version Distribution**
```sql
SELECT version, count() AS installations
FROM (
  SELECT installation_id, argMax(operator_version, event_time) AS version
  FROM usage_events GROUP BY installation_id
)
GROUP BY version ORDER BY installations DESC
```
format: `table`

**8 — Timeseries (stacked area): Version Adoption Over Time**
```sql
SELECT
  toStartOfInterval(event_time, INTERVAL 10 MINUTE) AS time,
  operator_version,
  countDistinct(installation_id) AS installs
FROM usage_events
WHERE event_time >= $__fromTime AND event_time <= $__toTime
GROUP BY time, operator_version ORDER BY time ASC
```
format: `timeseries`; `stacking: normal`, high `fillOpacity`.

### Environment & features (row 4)

**9 — Barchart: Kubernetes Version Distribution**
```sql
SELECT k8s_version, count() AS installations
FROM (
  SELECT installation_id, argMax(k8s_server_version, event_time) AS k8s_version
  FROM usage_events GROUP BY installation_id
)
GROUP BY k8s_version ORDER BY installations DESC
```
format: `table`

**10 — Piechart: Cloud Provider Distribution**
```sql
SELECT cloud, count() AS installations
FROM (
  SELECT installation_id, argMax(cloud_provider, event_time) AS cloud
  FROM usage_events GROUP BY installation_id
)
GROUP BY cloud ORDER BY installations DESC
```
format: `table`

**11 — Barchart: Feature-Gate Adoption**
```sql
SELECT gate, countIf(enabled = 1) AS installs_enabled
FROM (
  SELECT installation_id, argMax(feature_gates, event_time) AS fg
  FROM usage_events GROUP BY installation_id
)
ARRAY JOIN mapKeys(fg) AS gate, mapValues(fg) AS enabled
GROUP BY gate ORDER BY installs_enabled DESC
```
format: `table` — uses `ARRAY JOIN` to expand `Map(String, UInt8)`.

### Registry (row 5)

**12 — Table: All Installations**
```sql
SELECT
  installation_id,
  argMax(operator_version, event_time)            AS version,
  argMax(k8s_server_version, event_time)          AS k8s,
  argMax(cloud_provider, event_time)              AS cloud,
  argMax(mcpserver_count, event_time)             AS servers,
  argMax(mcpexternalauthconfig_count, event_time) AS ext_auth,
  max(event_time)                                  AS last_seen
FROM usage_events
GROUP BY installation_id ORDER BY last_seen DESC
```
format: `table`

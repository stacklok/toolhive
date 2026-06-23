# Usage Telemetry PoC — Scope Contract

**Read this first.** This directory is a throwaway proof-of-concept. Keep it small and
keep it deletable. If a change does not serve the demo below, it belongs in the parking
lot, not in this PoC.

## Purpose

Collect anonymous usage *snapshots* from the ToolHive operator and ship them to a local
ClickHouse instance, then visualise them in Grafana. The headline demo we are building
toward is a single sentence, backed by real data over time:

> "Installation X is running operator version Y with N MCPServer CRDs."

A snapshot is emitted periodically by the operator, stored as a raw row (full history,
no dedup), and queried/charted to show how installations, operator versions, and
MCPServer counts evolve over time.

## Data contract (FIXED)

- ClickHouse table `usage_events`, reached in-cluster at `clickhouse.<namespace>.svc:8123`
  (HTTP interface, port 8123).
- The operator performs a direct HTTP `INSERT INTO usage_events FORMAT JSONEachRow`.
- JSONEachRow field names: `installation_id`, `event_time` (UTC, `"2006-01-02 15:04:05"`),
  `operator_version`, `k8s_server_version`, `mcpserver_count`.
- Operator config via env: `USAGE_CLICKHOUSE_URL`
  (e.g. `http://clickhouse.toolhive-system.svc:8123`) and `USAGE_REPORT_INTERVAL`
  (duration, default `5m`). Reporter is **disabled when `USAGE_CLICKHOUSE_URL` is unset.**
- Schema is plain `MergeTree` keeping **raw snapshots** — we want full time-series
  history, NOT `ReplacingMergeTree` dedup. See `schema.sql`.

## IN SCOPE

- `installation_id` ConfigMap (`toolhive-usage-poc`) — random UUID, the only "identity".
- A standalone `manager.Runnable` reporter: cached client, never fatal (failures log and
  move on), emits a snapshot every `USAGE_REPORT_INTERVAL`.
- Minimal snapshot fields (the five above) with obvious extension points for more later.
- Direct HTTP JSONEachRow insert to ClickHouse.
- Local ClickHouse `Deployment` + `Service` for in-cluster (`clickhouse.yaml`), plus a
  `docker-compose.yml` / `docker run` bench alternative.
- `CREATE TABLE` DDL (`schema.sql`) — MergeTree, raw snapshots.
- Demo / active-installations / version-distribution SQL (`queries.sql`).
- Grafana wiring doc (`grafana.md`).
- The end-to-end demo loop in `README.md`.

## PARKING LOT — do NOT build

- Production ingestion gateway. Direct insert from the operator is **PoC-only**.
- The OTLP-Collector-with-ClickHouse-exporter variant. This is the production-shaped
  follow-on — mention it, do not implement it.
- Opt-out / opt-in flows and consent UX.
- Anonymisation hardening beyond the random UUID.
- Multi-cluster fan-in, authentication, TLS to ClickHouse, retries/buffering.
- Any change to reconcile logic, CRD types, or the existing OpenTelemetry pipeline.

## Layout

- Go code (the operator-side reporter) lives in `cmd/thv-operator/pkg/usage/` and is
  built by a separate effort. **This directory holds infra + docs only.**
- Deleting both `deploy/usage-telemetry-poc/` and `cmd/thv-operator/pkg/usage/` removes
  the PoC entirely. Nothing else in the tree should depend on either.

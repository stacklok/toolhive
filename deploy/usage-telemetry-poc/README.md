# ToolHive Operator Usage Telemetry — Proof of Concept

> **⚠️ This is a PoC.** The operator inserts snapshots **directly** into ClickHouse over
> HTTP. That is fine for a local demo and nothing else. In production you would put an
> **ingestion gateway** in front of ClickHouse, and the production-shaped path is an
> **OpenTelemetry Collector with a ClickHouse exporter** (mentioned here, deliberately
> not implemented). See `CLAUDE.md` for the full in-scope / parking-lot split.

This PoC collects anonymous usage *snapshots* from the ToolHive operator and stores them
as raw rows in a local ClickHouse, so you can answer — over time —:

> "Installation X is running operator version Y with N MCPServer CRDs."

- Infra + docs: this directory (`deploy/usage-telemetry-poc/`).
- Operator-side reporter (Go): `cmd/thv-operator/pkg/usage/`.
- Data contract (table schema, field names, env vars): see `CLAUDE.md` and `schema.sql`.

## Demo loop

### 1. Pick a namespace

Defaults to `toolhive-system` (the operator's namespace). If you change it, update the
`namespace:` fields in `clickhouse.yaml` and the `USAGE_CLICKHOUSE_URL` host below.

```bash
export NS=toolhive-system
```

### 2. Deploy ClickHouse

In-cluster:

```bash
kubectl apply -f clickhouse.yaml
kubectl -n "$NS" rollout status deploy/clickhouse
```

Or, for a local bench (no Kubernetes):

```bash
docker compose -f docker-compose.yml up -d
```

### 3. Apply the schema

In-cluster (port-forward, then POST the DDL over HTTP):

```bash
kubectl -n "$NS" port-forward svc/clickhouse 8123:8123 &
curl --data-binary @schema.sql http://localhost:8123
```

Or exec directly into the pod:

```bash
kubectl -n "$NS" exec -i deploy/clickhouse -- \
  clickhouse-client --multiquery < schema.sql
```

Local compose variant:

```bash
curl --data-binary @schema.sql http://localhost:8123
```

### 4. Run the operator pointed at ClickHouse

Set these env vars on the operator Deployment (or in your local run environment). The
reporter is **disabled when `USAGE_CLICKHOUSE_URL` is unset**, so it is opt-in by config.

```bash
USAGE_CLICKHOUSE_URL=http://clickhouse.toolhive-system.svc:8123
USAGE_REPORT_INTERVAL=1m   # optional; default 5m. Use 1m to see rows quickly.
```

On a Deployment:

```bash
kubectl -n "$NS" set env deploy/toolhive-operator \
  USAGE_CLICKHOUSE_URL=http://clickhouse."$NS".svc:8123 \
  USAGE_REPORT_INTERVAL=1m
```

On first start the reporter creates/reads the `toolhive-usage-poc` ConfigMap, which holds
the random `installation_id`.

### 5. Create a couple of MCPServer CRs

So `mcpserver_count` is non-zero and changes over time. Minimal example:

```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPServer
metadata:
  name: fetch
  namespace: toolhive-system
spec:
  image: ghcr.io/stackloklabs/gofetch/server
  transport: streamable-http
  proxyPort: 8080
  mcpPort: 8080
```

```bash
kubectl apply -f - <<'EOF'
# (paste the manifest above)
EOF
```

More examples live in `../../examples/operator/mcp-servers/`. Add or delete a few and
watch `mcpserver_count` track the change on the next report interval.

### 6. Watch rows land

```bash
curl 'http://localhost:8123/?default_format=PrettyCompact' \
  --data-binary 'SELECT * FROM usage_events ORDER BY event_time DESC LIMIT 10'
```

(or `clickhouse-client --query "SELECT * FROM usage_events ORDER BY event_time DESC LIMIT 10"`)

### 7. Run the headline demo query

Latest snapshot per installation (see `queries.sql` for this and other queries):

```bash
curl 'http://localhost:8123/?default_format=PrettyCompact' --data-binary '
SELECT
    installation_id,
    argMax(operator_version, event_time) AS version,
    argMax(mcpserver_count, event_time)  AS current_mcpservers,
    max(event_time)                       AS last_seen
FROM usage_events
GROUP BY installation_id;'
```

### 8. View in Grafana

**Quick path — Helm (recommended):** Grafana with the ClickHouse datasource and the three
panels below auto-provisioned. No click-ops required.

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update

helm upgrade --install grafana grafana/grafana \
  --namespace toolhive-system \
  --kubeconfig kconfig.yaml \
  -f deploy/usage-telemetry-poc/grafana-values.yaml
```

Then access it:

```bash
kubectl port-forward svc/grafana 3000:80 -n toolhive-system --kubeconfig kconfig.yaml
# open http://localhost:3000  — login: admin / admin
```

Navigate to **Dashboards → ToolHive → ToolHive Usage Telemetry**. The dashboard
auto-refreshes every 30s and defaults to a 3-hour time window (use the picker for more
history).

**Tear down Grafana only:**

```bash
helm uninstall grafana -n toolhive-system --kubeconfig kconfig.yaml
```

**Manual path** (no Helm, click-ops required): see `grafana.md` for datasource
configuration and panel SQL.

## Simulating multiple installations

The "across installations" queries (active count, version distribution) need more than
one `installation_id`. Options:

- Delete the `toolhive-usage-poc` ConfigMap (`kubectl -n "$NS" delete configmap
  toolhive-usage-poc`) and let the reporter mint a fresh UUID — the next snapshots arrive
  under a new installation.
- Run the operator/reporter in multiple namespaces or clusters, each with its own
  ConfigMap and `USAGE_CLICKHOUSE_URL` pointed at the same ClickHouse.
- For pure SQL/Grafana exploration, hand-insert rows with a different `installation_id`:

  ```bash
  curl --data-binary @- http://localhost:8123 <<'EOF'
  INSERT INTO usage_events FORMAT JSONEachRow
  {"installation_id":"demo-2","event_time":"2026-06-23 12:00:00","operator_version":"v0.9.0","k8s_server_version":"v1.30.0","mcpserver_count":3}
  EOF
  ```

## Tear down / delete the PoC

```bash
# Grafana (if installed via Helm)
helm uninstall grafana -n toolhive-system --kubeconfig kconfig.yaml

# ClickHouse
kubectl delete -f clickhouse.yaml          # in-cluster
# or
docker compose -f docker-compose.yml down -v   # local bench
```

Then remove the operator env vars (`USAGE_CLICKHOUSE_URL`, `USAGE_REPORT_INTERVAL`) and,
optionally, the `toolhive-usage-poc` ConfigMap.

To remove the PoC **entirely** from the codebase, delete both:

```
deploy/usage-telemetry-poc/      # infra + docs (this directory)
cmd/thv-operator/pkg/usage/      # the operator-side reporter
```

Nothing else in the tree depends on either.

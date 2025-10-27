# Refactor MCPRegistry controller
## Requirement Overview

The MCPRegistry operator uses the `MCPRegistry` CRD to deploy Registry API servers pre-populated with curated MCP server definitions. The controller fetches registry data from multiple source types (`ConfigMap`, Git repositories, or external Registry APIs), applies filtering rules, and stores the result in a `ConfigMap` mounted by the Registry API server.

Currently, these data source management capabilities exist only in the Kubernetes operator. However, ToolHive users typically start with the CLI before deploying to Kubernetes. To provide a consistent experience, we need to move data source management into `thv-registry-api` itself, making these features available in both CLI and Kubernetes environments.

**Affected features:**
- Data source configuration (ConfigMap, Git, Registry API)
- Periodic and on-demand synchronization
- Tag and name filtering

This proposal describes moving functionality from the `thv-operator` module ([toolhive](https://github.com/stacklok/toolhive)) to the `thv-registry-api` module ([toolhive-registry-server](https://github.com/stacklok/toolhive-registry-server)).

### Design Constraints

This refactoring maintains feature parity without introducing new capabilities:

- **API stability**: The `MCPRegistry` CRD spec remains unchanged
- **Status evolution**: Status field changes will be documented if required
- **CLI extension**: New runtime flags may be added to `thv-registry-api`
- **Storage**: File-based storage will be implemented; database storage is out of scope

## Current status
### Data source configuration
`MCPRegistry` resources define data sources and filtering rules in the `spec.source` and `spec.filter` fields:
```yaml
spec:
  source:
    type: configmap
    configmap:
      name: minimal-registry-data
  syncPolicy:
    interval: "30m"
  filter:
    tags:
      include: ["database", "production"]
      exclude: ["experimental", "deprecated", "beta"]
```
### Current Implementation
The `MCPRegistry` controller implements data source management in these operator packages:
| Package| Purpose |
|--------|---------|
|`cmd/thv-operator/pkg/sources`|`SourceHandler` interface and implementations for `ConfigMap`, Git, and Registry API sources; StorageManager implementation using `ConfigMap` |
|`cmd/thv-operator/pkg/httpclient`|HTTP client for Registry API sources|
|`cmd/thv-operator/pkg/git`|Git repository operations|
|`cmd/thv-operator/pkg/sync`|`Manager` interface for periodic and on-demand synchronization|
|`cmd/thv-operator/pkg/filtering`|`FilterService` interface and implementation for tag and name filtering|

These packages run exclusively within the operator controller with no external dependencies.

### Controller Responsibilities
The controller currently handles two distinct concerns:
1. Deployment management: Creates and maintains Registry API server Deployments and Services
1. Data synchronization: Fetches, filters, and stores registry data in `ConfigMap`s

After deployment, the controller does not communicate with the Registry API server—it only manages the `ConfigMap` containing registry data.

### Status Reporting
The MCPRegistry status tracks deployment, synchronization and storage state information.

#### API deployment status
```yaml
apiStatus:
  endpoint: http://thv-git-api.toolhive-system:8080
  phase: Ready
  readySince: "2025-10-22T08:30:38Z"
```
#### Synchronization status  
```yaml
syncStatus:
  phase: Complete
  lastSyncTime: "2025-10-22T08:30:27Z"
  serverCount: 88
  lastSyncHash: c013b6b36286ab...
```
#### Storage reference
```yaml
storageRef:
  type: configmap
  configMapRef:
    name: thv-git-registry-storage
```
#### Standard conditions
```yaml
conditions:
  - type: APIReady
    status: "True"
    lastTransitionTime: "2025-10-22T08:30:38Z"
```

## Refactoring Proposal

### Component Responsibilities

**Controller:**
- Deploy and manage Registry API (`Deployment`, `Service`)
- Create and update configuration `ConfigMap` from `MCPRegistry` spec
- Monitor deployment health and update `apiStatus`
- Trigger manual syncs via Registry API endpoint
- Update `syncStatus`, see [Sync Status Updates](#sync-status-updates) options

**Registry API:**
- Watch configuration file for changes
- Execute periodic and manual synchronization
- Fetch, filter, and store registry data
- Expose sync status via HTTP endpoint

### Configuration Management

**Flow:**
1. Controller creates `ConfigMap` `<registry-name>-config` containing `MCPRegistry` spec
2. `ConfigMap` mounted at `/config/spec.yaml` in Registry API pod
3. Registry API launched with `--config /config/spec.yaml` flag
4. Registry API watches file and triggers sync on changes

### Storage

**File-based (Phase 1):**
- Registry API stores data at path defined by `--storage-path` flag
- Controller sets `--storage-path /data` using mounted `emptyDir` volume
- Data format: ToolHive registry JSON schema (unchanged from current)
- Persistence: Data survives pod restarts, lost on deployment deletion

**Database-backed (Future):** Deferred to separate proposal for multi-replica support.

### Sync Status Updates

**Options:**

**Option 1 - HTTP Polling (Recommended):**
- Registry API exposes `GET /registry/status` returning sync history, stored in the configured storage path
- Controller polls every 60 seconds and updates `syncStatus`
- Pros: Simple, no new CRDs, works for CLI and Kubernetes
- Cons: 60-second status latency

**Option 2 - Event CRD:**
- New `SyncCompletionEvent` CRD created by Registry API on sync completion
- Controller watches events and updates `MCPRegistry.status.syncStatus`
- Pros: Real-time updates
- Cons: Requires RBAC, adds CRD complexity, Kubernetes-only

**Option 3 - Remove syncStatus:**
- Remove `syncStatus` from `MCPRegistry` entirely
- Users query Registry API `GET /registry/status` directly for sync information
- Pros: Simplest implementation
- Cons: Breaking change, loses kubectl visibility

**Recommendation:** Option 1 provides best balance of simplicity and functionality.

**Status endpoint response:**
```json
{
  "lastSync": {
    "type": "automatic",
    "startTime": "2025-10-22T08:30:38Z",
    "completionTime": "2025-10-22T08:32:38Z",
    "hash": "c013b6b36286ab...",
    "serverCount": 88,
    "result": "completed",
    "attemptCount": 0,
    "errorMessage": null
  }
}
```

### Automatic Sync

- Registry API uses `time.Ticker` for periodic sync based on `syncPolicy.interval`
- On each interval: check source hash, sync only if changed
- Record result in sync history

### Manual Sync

- Registry API exposes `POST /registry/sync` endpoint
- Controller calls endpoint when `toolhive.stacklok.dev/sync-trigger` annotation changes
- Endpoint returns 202 (accepted) or 409 (sync in progress)
- Sync result recorded in history

### Sync Failure Handling

**Current behavior:**
- Fixed 5-minute retry interval
- Unlimited retry attempts
- `syncStatus.attemptCount` increments on failure, resets on success

**Proposed (Phase 1):**
Match current behavior - Registry API retries every 5 minutes indefinitely.

**Future enhancement:**
Add configurable retry policy to `syncPolicy.retryPolicy` (tracked separately).

### Data Filtering

- Registry API applies name and tag filters during sync processing
- Filter changes trigger immediate sync (hash-based detection)
- Move `cmd/thv-operator/pkg/filtering` package to `thv-registry-api`

### API Status Updates

Controller continues managing `apiStatus` with no changes:
- Monitors Deployment and Service health
- Updates phase: NotStarted, Deploying, Ready, Unhealthy, Error
- Existing `apiStatus` structure preserved

## Implementation Plan

### Phase 1: Move Data Source Management to Registry API

**Goal:** Full-functioning solution with sync logic in `thv-registry-api`, maintaining backward compatibility with existing MCPRegistry resources.

**Task Order Rationale:** Registry API must be enhanced first before operator changes, to avoid breaking existing deployments.

#### Registry API Development

1. **Add configuration file support**
   - Implement `--config` flag in `thv-registry-api serve` command
   - Add YAML configuration parsing for `source`, `syncPolicy`, and `filter` fields
   - Implement file watcher for configuration changes (using `fsnotify` or similar)
   - Graceful reload on configuration changes

2. **Add file-based storage**
   - Implement `--storage-path` flag for registry data location
   - Add file-based storage backend matching current ConfigMap JSON format
   - Atomic file writes (temp file + rename pattern)
   - Load existing data on startup if present

3. **Port source handler packages**
   - Copy `cmd/thv-operator/pkg/sources` to `thv-registry-api`
   - Copy `cmd/thv-operator/pkg/httpclient` to `thv-registry-api`
   - Copy `cmd/thv-operator/pkg/git` to `thv-registry-api`
   - Adapt packages to work without Kubernetes client dependencies
   - Update import paths

4. **Port sync manager**
   - Copy `cmd/thv-operator/pkg/sync` to `thv-registry-api`
   - Adapt sync logic to use file-based storage instead of ConfigMap
   - Implement retry logic (5-minute interval, unlimited attempts)
   - Remove Kubernetes-specific dependencies

5. **Port filtering logic**
   - Copy `cmd/thv-operator/pkg/filtering` to `thv-registry-api`
   - Keep filtering interface and implementation unchanged
   - Update import paths

6. **Implement automatic sync**
   - Add `time.Ticker` based periodic sync using `syncPolicy.interval`
   - Implement hash-based change detection before syncing
   - Store sync history in file at storage path

7. **Add sync status endpoint**
   - Implement `GET /registry/status` returning sync history
   - Return format matching proposed JSON schema
   - Read from sync history file

8. **Add manual sync endpoint**
   - Implement `POST /registry/sync` for manual sync triggering
   - Return 202 (accepted) or 409 (sync in progress)
   - Prevent concurrent syncs

9. **Add integration tests**
   - Test configuration file watching and reload
   - Test all source types (ConfigMap, Git, API)
   - Test filtering logic
   - Test automatic and manual sync
   - Test retry behavior on failures

#### Operator Updates

10. **Update operator to use new Registry API**
    - Modify MCPRegistry controller to create configuration ConfigMap
    - Update Deployment spec to mount config ConfigMap at `/config/spec.yaml`
    - Update Deployment spec to mount emptyDir volume at `/data`
    - Add `--config /config/spec.yaml --storage-path /data` flags to registry-api container
    - Add polling logic to call `GET /registry/status` every 60 seconds (depends on [selected update option](#sync-status-updates))
    - Update `syncStatus` from polling results

11. **Add manual sync trigger via API**
    - Detect `toolhive.stacklok.dev/sync-trigger` annotation changes
    - Call `POST /registry/sync` on Registry API endpoint
    - Update `lastManualSyncTrigger` after successful API call

12. **Remove sync logic from operator**
    - Remove sync execution code from MCPRegistry controller (keep status update only)
    - Mark `cmd/thv-operator/pkg/sources` as deprecated (keep for reference)
    - Mark `cmd/thv-operator/pkg/sync` as deprecated (keep for reference)
    - Mark `cmd/thv-operator/pkg/filtering` as deprecated (keep for reference)

13. **Update operator integration tests**
    - Update tests to verify controller creates config ConfigMap
    - Test controller polls Registry API status endpoint
    - Test manual sync via annotation
    - Test backward compatibility with existing MCPRegistry resources

14. **Update documentation**
    - Document new Registry API flags and endpoints
    - Update MCPRegistry CRD examples
    - Add migration guide for users (if any operator upgrade steps needed)

### Phase 2: Future Enhancements

**Goal:** Improvements deferred from Phase 1 to keep initial scope manageable.

1. **Configurable retry policy**
   - Add `spec.syncPolicy.retryPolicy` field to MCPRegistry CRD
   - Support `maxAttempts`, `retryInterval`, `backoff` configuration
   - Implement in Registry API sync logic
   - Update operator to pass retry config via ConfigMap

2. **Real-time status updates (if needed)**
   - Evaluate if 60-second polling latency is acceptable
   - If not: Implement Option 2 (Event CRD) from proposal
   - Add `SyncCompletionEvent` CRD
   - Update Registry API to create events
   - Update controller to watch events

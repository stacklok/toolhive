# Refactor MCPRegistry controller
## Goal
The main goal is to expose all the data source management functions in the `thv-registry-api`
module, so that both the CLI and the Kubernetes users can benefit the same features.

The impacted features, today implemented in the MCPRegistry controller, are the following:
* Data source configuration
* Periodical and on-demand sync
* Data filtering

## Current status
### Data source configuration
The operator allows to define the data source in the `spec.source` and `spec.filter` sections of
the `MCPRegistry`:
```yaml
spec:
  source:
    type: configmap
    configmap:
      name: minimal-registry-data
  syncPolicy:
    interval: "30m"  # Automatically sync every 30 minutes
  filter:
    tags:
      include:
        - "database"    # Only database servers
        - "production"  # Only production-ready servers
      exclude:
        - "experimental"  # Exclude experimental servers
        - "deprecated"    # Exclude deprecated servers
        - "beta"          # Exclude beta versions
```
### Implementation
The implementation is defined in the following packages:
- `cmd/thv-operator/pkg/sources`: data source management functions
  - Includes the implementations of the `SourceHandler` interface for the managed types 
    (`api`, `configmap` and `git`).
  - Includes the implementation of the `StorageManager` interface using a `ConfigMap`
- `cmd/thv-operator/pkg/httpclient`: utility classes for the `api` type.
- `cmd/thv-operator/pkg/git`: utility classes for the `git` type.
- `cmd/thv-operator/pkg/sync`: interface and default implementation of the `Manager` interface to 
  handle the automatic or on-demand sync flow.
- `cmd/thv-operator/pkg/filtering`: the `FilterService` interface and a default implementation to
  apply name and tag filtering to the original data.

The above packages are executed in the context the the operator controller, with no interactions 
with other components.

### Interaction with Registry API server
The `MCPRegistry` controller manages the deployments of the registry API servers but does not
interact with them after they are deployed.

### MCPRegistry status
The `MCPRegistry` status keeps track of the deployment status of the registry API and also the sync
status, both with explicit fields and the standard condition fields.
```yaml
apiStatus:
  endpoint: http://thv-git-api.toolhive-system:8080
  message: Registry API is ready and serving requests
  phase: Ready
  readySince: "2025-10-22T08:30:38Z"
conditions:
  - lastTransitionTime: "2025-10-22T08:30:38Z"
    message: Registry API is ready and serving requests
    reason: APIReady
    status: "True"
    type: APIReady
lastAppliedFilterHash: 74234e98afe7498fb5daf1f36ac2d78acc339464f950703b8c019892f982b90b
lastManualSyncTrigger: "2025-10-22T08:38:16.286Z"
message: Registry is ready and API is serving requests
phase: Ready
storageRef:
  configMapRef:
    name: thv-git-registry-storage
  type: configmap
syncStatus:
  lastAttempt: "2025-10-22T08:30:27Z"
  lastSyncHash: c013b6b36286ab438f3c08306474f2e163bee6554c8886c2c8155164479cdf53
  lastSyncTime: "2025-10-22T08:30:27Z"
  message: Registry data synchronized successfully
  phase: Complete
  serverCount: 88
```
## Refactory Proposal
### High level requirements
- Both the Kubernetes and the CLI registries provide the same data source functionalities.
- The Kubernetes controller takes care deploying the registry API server as today and ensure
  that its configuration matches the `MCPRegistry` specification.
- The registry API reacts to configuration changes to apply them.
- The Kubernetes controller


### [NEXT] New CRDs for data source specification
Move the data source management functions from the `thv-operator` module to the `thv-registry-api` 
module
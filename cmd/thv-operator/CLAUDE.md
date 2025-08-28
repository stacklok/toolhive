- CRDs are in cmd/thv-operator/api/v1alpha1/mcpserver_types.go
- After modifying the CRDs, the following changes need to be done:
    - `task operator-generate`, `task operator-manifests` and `task crdref-gen`.
    - it is important to run `task crdref-gen` inside cmd/thv-operator as the current directory
- When committing a change that changes CRDs, it is important to bump the chart version in deploy/charts/operator-crds/Chart.yaml and deploy/charts/operator-crds/README.md

## Keycloak Development Setup

```bash
task keycloak:install-operator    # Install Keycloak operator
task keycloak:deploy-dev         # Deploy Keycloak and setup ToolHive realm  
task keycloak:get-admin-creds    # Get admin credentials
task keycloak:port-forward       # Access admin UI at http://localhost:8080
```

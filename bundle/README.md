# ToolHive Operator OLM Bundle

This directory contains the Operator Lifecycle Manager (OLM) bundle for the ToolHive Operator, enabling installation via OLM-compatible catalogs such as the OKDerators Catalog.

## Bundle Structure

```
bundle/
├── manifests/                          # OLM bundle manifests
│   ├── toolhive-operator.clusterserviceversion.yaml  # Operator metadata and deployment spec
│   ├── toolhive.stacklok.dev_mcpservers.yaml         # MCPServer CRD
│   ├── toolhive.stacklok.dev_mcpregistries.yaml      # MCPRegistry CRD
│   └── toolhive.stacklok.dev_mcptoolconfigs.yaml     # MCPToolConfig CRD
├── metadata/                           # Bundle metadata
│   └── annotations.yaml                # Bundle annotations
└── README.md                           # This file
```

## Building the Bundle

### Prerequisites

- `operator-sdk` (for validation) - [Installation Guide](https://sdk.operatorframework.io/docs/installation/)
- `podman` or `docker` (for building bundle image)
- `opm` (optional, for catalog operations) - [Installation Guide](https://github.com/operator-framework/operator-registry/releases)

### Build Steps

1. **Prepare the bundle** (copies CRDs to bundle/manifests):
   ```bash
   cd cmd/thv-operator
   task bundle:prepare
   ```

2. **Validate the bundle**:
   ```bash
   task bundle:validate
   ```

3. **Build the bundle image**:
   ```bash
   task bundle:build BUNDLE_IMAGE=ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5
   ```

4. **Push the bundle image** (requires authentication):
   ```bash
   task bundle:push BUNDLE_IMAGE=ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5
   ```

Or run all steps at once:
```bash
task bundle:all BUNDLE_IMAGE=ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5
```

## Manual Build

If you prefer to build manually:

```bash
# From the repository root
docker build -f bundle.Dockerfile -t ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5 .
docker push ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5
```

## Bundle Contents

### ClusterServiceVersion (CSV)

The CSV (`toolhive-operator.clusterserviceversion.yaml`) defines:
- Operator metadata (name, version, description)
- Installation modes (AllNamespaces, SingleNamespace, etc.)
- Deployment specification
- RBAC permissions (ClusterRole and ClusterRoleBinding)
- CRD ownership and descriptions
- Related images

### Custom Resource Definitions (CRDs)

The bundle includes three CRDs:
1. **MCPServer** - Primary resource for managing MCP server instances
2. **MCPRegistry** - Experimental feature for managing MCP server registries
3. **MCPToolConfig** - Configuration for tool filtering and overrides

## Updating the Bundle

When updating the operator:

1. Update the CSV version in `bundle/manifests/toolhive-operator.clusterserviceversion.yaml`
2. Update the bundle image tag in build commands
3. Regenerate CRDs if API changes were made:
   ```bash
   cd cmd/thv-operator
   task operator-manifests
   ```
4. Copy updated CRDs to bundle:
   ```bash
   task bundle:prepare
   ```
5. Rebuild and push the bundle

## OKD Compatibility

The bundle is configured for OKD compatibility:
- Security contexts comply with restricted Security Context Constraints (SCCs)
- `runAsNonRoot: true`
- `readOnlyRootFilesystem: true`
- All capabilities dropped
- Non-root user (UID 1000)

## Integration with OKDerators Catalog

To add this operator to the OKDerators Catalog:

1. Fork the [okderators-catalog-index](https://github.com/okd-project/okderators-catalog-index) repository
2. Add a package manifest referencing this bundle image
3. Update the catalog index using `opm`
4. Submit a pull request

See the main project documentation for detailed onboarding instructions.

## Resources

- [OLM Documentation](https://olm.operatorframework.io/)
- [Operator SDK Documentation](https://sdk.operatorframework.io/)
- [OKDerators Catalog](https://github.com/okd-project/okderators-catalog-index)


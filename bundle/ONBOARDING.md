# ToolHive Operator OKDerators Catalog Onboarding Guide

This document provides a comprehensive guide for onboarding the ToolHive Operator to the OKDerators Catalog Index.

## Status

✅ **Phase 1: OLM Bundle Creation** - COMPLETED
- Bundle directory structure created
- ClusterServiceVersion (CSV) generated
- CRDs packaged
- Bundle metadata created
- Bundle Dockerfile created
- Build automation added

⏳ **Phase 2: OKD Compatibility Verification** - READY FOR TESTING
- Security contexts verified in CSV
- Requires testing on OKD cluster

⏳ **Phase 3: Catalog Integration** - READY FOR IMPLEMENTATION
- Requires forking okderators-catalog-index repository

⏳ **Phase 4: Testing and Validation** - READY FOR TESTING
- Requires OKD test cluster

⏳ **Phase 5: Submission and Review** - READY FOR SUBMISSION
- Requires completed testing

## Phase 1: OLM Bundle Creation (✅ COMPLETED)

### What Was Created

1. **Bundle Structure**
   - `bundle/manifests/` - Contains CSV and CRD manifests
   - `bundle/metadata/` - Contains bundle annotations
   - `bundle.Dockerfile` - Bundle image build file

2. **ClusterServiceVersion (CSV)**
   - Location: `bundle/manifests/toolhive-operator.clusterserviceversion.yaml`
   - Includes:
     - Operator metadata and description
     - All install modes (AllNamespaces, SingleNamespace, etc.)
     - Deployment specification with security contexts
     - Cluster-scoped RBAC permissions
     - CRD ownership and descriptions
     - Example ALM resources

3. **CRDs**
   - `toolhive.stacklok.dev_mcpservers.yaml`
   - `toolhive.stacklok.dev_mcpregistries.yaml`
   - `toolhive.stacklok.dev_mcptoolconfigs.yaml`

4. **Build Automation**
   - Taskfile targets added:
     - `bundle:prepare` - Copy CRDs to bundle
     - `bundle:validate` - Validate bundle with operator-sdk
     - `bundle:build` - Build bundle image
     - `bundle:push` - Push bundle image
     - `bundle:all` - Run all bundle tasks

### Building the Bundle

```bash
# From cmd/thv-operator directory
cd cmd/thv-operator

# Prepare bundle (copy CRDs)
task bundle:prepare

# Validate bundle
task bundle:validate

# Build bundle image
task bundle:build BUNDLE_IMAGE=ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5

# Push bundle image (requires authentication)
task bundle:push BUNDLE_IMAGE=ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5

# Or run all steps
task bundle:all BUNDLE_IMAGE=ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5
```

## Phase 2: OKD Compatibility Verification

### Security Context Verification

The bundle has been configured with OKD-compatible security contexts:

✅ **Pod Security Context**
- `runAsNonRoot: true`

✅ **Container Security Context**
- `allowPrivilegeEscalation: false`
- `readOnlyRootFilesystem: true`
- `runAsNonRoot: true`
- `runAsUser: 1000`
- `capabilities.drop: ["ALL"]`

### Testing on OKD

1. **Deploy to OKD Test Cluster**
   ```bash
   # Build and push bundle image first
   task bundle:all BUNDLE_IMAGE=ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5
   
   # Create CatalogSource in OKD cluster
   cat <<EOF | oc apply -f -
   apiVersion: operators.coreos.com/v1alpha1
   kind: CatalogSource
   metadata:
     name: toolhive-operator-catalog
     namespace: openshift-marketplace
   spec:
     sourceType: grpc
     image: ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5
     displayName: ToolHive Operator
     publisher: Stacklok
   EOF
   ```

2. **Verify SCC Compliance**
   ```bash
   # Check if operator pod runs with restricted SCC
   oc get pod -n toolhive-system -l name=toolhive-operator -o jsonpath='{.items[0].metadata.annotations.openshift\.io/scc}'
   # Should output: restricted
   ```

3. **Test Operator Functionality**
   ```bash
   # Create test MCPServer
   cat <<EOF | oc apply -f -
   apiVersion: toolhive.stacklok.dev/v1alpha1
   kind: MCPServer
   metadata:
     name: test-server
   spec:
     image: docker.io/mcp/fetch
     transport: stdio
     port: 8080
     permissionProfile:
       type: builtin
       name: network
   EOF
   
   # Verify MCPServer is created and reconciled
   oc get mcpserver test-server
   ```

### Checklist

- [ ] Bundle image builds successfully
- [ ] Operator installs on OKD cluster
- [ ] Operator pod runs with `restricted` SCC
- [ ] All CRDs are created
- [ ] MCPServer resources can be created
- [ ] Operator reconciles MCPServer resources correctly
- [ ] No security context violations

## Phase 3: Catalog Integration

### Fork and Clone Repository

```bash
# Fork okderators-catalog-index on GitHub
# Then clone your fork
git clone https://github.com/YOUR_USERNAME/okderators-catalog-index.git
cd okderators-catalog-index
git checkout -b add-toolhive-operator
```

### Add Catalog Entry

1. **Create Package Directory**
   ```bash
   mkdir -p catalog/toolhive-operator
   ```

2. **Create Package Manifest**
   
   Create `catalog/toolhive-operator/package.yaml`:
   ```yaml
   packageName: toolhive-operator
   channels:
     - name: stable
       entries:
         - name: toolhive-operator.v0.3.5
   defaultChannel: stable
   ```

3. **Update Catalog Index**
   
   The catalog index is typically built using `opm`. Check the okderators-catalog-index repository for their specific build process. Generally:
   ```bash
   # Install opm if not already installed
   # https://github.com/operator-framework/operator-registry/releases
   
   # Add bundle to index (example - check repository for exact process)
   opm index add \
     --bundles ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5 \
     --tag ghcr.io/YOUR_USERNAME/okderators-catalog-index:latest \
     --container-tool docker
   ```

4. **Follow Repository Structure**
   
   Review existing operators in the catalog to understand the exact structure and process used by okderators-catalog-index.

### Documentation

Add operator documentation to the catalog entry:
- Operator description
- Usage examples
- Links to documentation
- Support information

## Phase 4: Testing and Validation

### Local Catalog Testing

1. **Build Catalog Index**
   ```bash
   # Follow okderators-catalog-index build instructions
   ```

2. **Deploy Catalog to Test Cluster**
   ```bash
   # Create CatalogSource pointing to your catalog index
   ```

3. **Install Operator via OLM**
   ```bash
   # Create OperatorGroup
   cat <<EOF | oc apply -f -
   apiVersion: operators.coreos.com/v1
   kind: OperatorGroup
   metadata:
     name: toolhive-operator-group
     namespace: toolhive-system
   spec:
     targetNamespaces:
     - toolhive-system
   EOF
   
   # Create Subscription
   cat <<EOF | oc apply -f -
   apiVersion: operators.coreos.com/v1alpha1
   kind: Subscription
   metadata:
     name: toolhive-operator
     namespace: toolhive-system
   spec:
     channel: stable
     name: toolhive-operator
     source: okderators-catalog
     sourceNamespace: openshift-marketplace
   EOF
   ```

4. **Verify Installation**
   ```bash
   # Check operator installation
   oc get csv -n toolhive-system
   oc get pods -n toolhive-system
   
   # Verify CRDs are installed
   oc get crd | grep toolhive
   ```

### E2E Testing

1. **Create Test Resources**
   - Create MCPServer resources
   - Create MCPToolConfig resources
   - Test MCPRegistry (if experimental features enabled)

2. **Verify Reconciliation**
   - Check that Deployments are created
   - Check that Services are created
   - Verify operator logs for errors

3. **Test Upgrade Path**
   - If upgrading from previous version, test upgrade process

4. **Test Uninstallation**
   - Verify operator can be cleanly uninstalled
   - Verify CRDs are removed (if applicable)

### Validation Checklist

- [ ] Catalog index builds successfully
- [ ] Operator installs via OLM
- [ ] All CRDs are created
- [ ] Operator pod runs successfully
- [ ] MCPServer resources can be created
- [ ] Operator reconciles resources correctly
- [ ] Operator upgrade works (if applicable)
- [ ] Operator uninstallation works cleanly

## Phase 5: Submission and Review

### Prepare Pull Request

1. **Commit Changes**
   ```bash
   git add catalog/toolhive-operator/
   git commit -m "Add ToolHive Operator to catalog"
   git push origin add-toolhive-operator
   ```

2. **Create Pull Request**
   - Go to https://github.com/okd-project/okderators-catalog-index
   - Create PR from your fork
   - Include:
     - Clear description of the operator
     - Testing results
     - Links to operator documentation
     - Any special considerations

3. **PR Description Template**
   ```markdown
   ## Add ToolHive Operator
   
   This PR adds the ToolHive Operator to the OKDerators Catalog.
   
   ### Operator Details
   - **Name**: ToolHive Operator
   - **Version**: v0.3.5
   - **Description**: Manages MCP (Model Context Protocol) servers in Kubernetes
   - **Repository**: https://github.com/stacklok/toolhive
   - **Documentation**: https://docs.stacklok.com/toolhive/
   
   ### Testing
   - [x] Tested on OKD cluster
   - [x] Operator installs successfully
   - [x] All CRDs functional
   - [x] SCC compliance verified
   
   ### Bundle Image
   - `ghcr.io/stacklok/toolhive-operator-bundle:v0.3.5`
   ```

### Review Process

1. **Address Review Comments**
   - Respond to reviewer feedback
   - Make requested changes
   - Update documentation as needed

2. **CI Checks**
   - Ensure all CI checks pass
   - Fix any validation errors

3. **Community Coordination**
   - Engage with OKD community maintainers
   - Address any concerns or questions

## Resources

- [OKDerators Catalog Index](https://github.com/okd-project/okderators-catalog-index)
- [OLM Documentation](https://olm.operatorframework.io/)
- [Operator SDK Documentation](https://sdk.operatorframework.io/)
- [OKD Documentation](https://docs.okd.io/)
- [ToolHive Operator Documentation](https://docs.stacklok.com/toolhive/)

## Support

For questions or issues:
- ToolHive Operator: https://github.com/stacklok/toolhive/issues
- OKDerators Catalog: https://github.com/okd-project/okderators-catalog-index/issues
- OKD Community: Join #okd-dev on Slack


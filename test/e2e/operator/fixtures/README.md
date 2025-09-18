# Test Fixtures

This directory contains YAML manifests for testing the MCPRegistry controller.

## Files

- **mcpregistry-git-basic.yaml**: Basic MCPRegistry with Git source and automatic sync
- **mcpregistry-git-auth.yaml**: MCPRegistry with Git authentication using secrets
- **mcpregistry-manual-sync.yaml**: MCPRegistry with manual sync only
- **git-credentials-secret.yaml**: Secret containing Git authentication credentials
- **test-registry-data.yaml**: Sample registry data in ConfigMap format

## Usage

These fixtures are used by the operator e2e tests to create consistent test scenarios. They can be loaded using the test helpers or applied directly with kubectl for manual testing.

## Customization

When using these fixtures in tests:
1. Update the namespace field to match your test namespace
2. Modify resource names to avoid conflicts
3. Adjust Git URLs to point to test repositories as needed
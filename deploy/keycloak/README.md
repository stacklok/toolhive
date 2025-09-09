# Keycloak Development Setup

This directory contains configuration for setting up Keycloak authentication with ToolHive MCP servers in development environments.

## Quick Start

1. **Deploy Keycloak and setup realm** (from `cmd/thv-operator/` directory):
   ```bash
   task kind-setup
   task operator-install-crds
   task operator-deploy-local
   task keycloak:deploy-dev
   ```

2. **Access Keycloak admin UI**:
   ```bash
   task keycloak:port-forward
   ```
   Open http://localhost:8080 and login with operator-generated credentials:
   ```bash
   task keycloak:get-admin-creds
   ```

3. **Deploy authenticated MCP server**:
   ```bash
   kubectl apply -f deploy/keycloak/mcpserver-with-auth.yaml --kubeconfig kconfig.yaml
   ```

## Testing Authentication

1. **Get access token**:
   ```bash
   curl -d "client_id=mcp-test-client" \
        -d "username=toolhive-user" \
        -d "password=user123" \
        -d "grant_type=password" \
        "http://localhost:8080/realms/toolhive/protocol/openid-connect/token"
   ```

2. **Use token with MCP server**:
   ```bash
   curl -H "Authorization: Bearer YOUR_TOKEN" \
        http://your-mcp-server-url/
   ```
   An easy to test example is to forward the port to your MCP server:
   ```
   kubectl port-forward svc/mcp-fetch-server-keycloak-proxy 9090:9090 -ntoolhive-system
   ```
   then launch the MCP inspector connect to `localhost:9090/mcp` and use the token from earlier as a bearer token.

#!/bin/bash
set -e

KEYCLOAK_URL="http://localhost:8080"
# Get admin credentials from the operator-created secret
ADMIN_USER=$(kubectl get secret keycloak-dev-initial-admin -n keycloak -o jsonpath='{.data.username}' --kubeconfig kconfig.yaml | base64 --decode)
ADMIN_PASS=$(kubectl get secret keycloak-dev-initial-admin -n keycloak -o jsonpath='{.data.password}' --kubeconfig kconfig.yaml | base64 --decode)

echo "Using operator-generated admin credentials..."

echo "Getting admin token..."
TOKEN=$(curl -s -d "client_id=admin-cli" \
  -d "username=$ADMIN_USER" \
  -d "password=$ADMIN_PASS" \
  -d "grant_type=password" \
  "$KEYCLOAK_URL/realms/master/protocol/openid-connect/token" | jq -r '.access_token')

if [ "$TOKEN" = "null" ] || [ -z "$TOKEN" ]; then
    echo "Failed to get admin token"
    exit 1
fi

echo "Setting up ToolHive realm..."

# First create the realm
echo "Creating toolhive realm..."
curl -s -X POST "$KEYCLOAK_URL/admin/realms" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "realm": "toolhive",
    "displayName": "ToolHive Realm",
    "enabled": true,
    "accessTokenLifespan": 3600,
    "accessTokenLifespanForImplicitFlow": 1800,
    "ssoSessionIdleTimeout": 3600,
    "ssoSessionMaxLifespan": 72000,
    "offlineSessionIdleTimeout": 2592000
  }' || echo "Realm may already exist"

# Create clients
echo "Creating mcp-test-client..."
curl -s -X POST "$KEYCLOAK_URL/admin/realms/toolhive/clients" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "clientId": "mcp-test-client",
    "enabled": true,
    "publicClient": false,
    "secret": "mcp-test-client-secret",
    "serviceAccountsEnabled": true,
    "standardFlowEnabled": true,
    "directAccessGrantsEnabled": true,
    "redirectUris": ["http://localhost:*", "http://127.0.0.1:*"],
    "webOrigins": ["http://localhost:*", "http://127.0.0.1:*"],
    "description": "Confidential client for MCP testing"
  }' || echo "Client may already exist"

echo "Creating mcp-server..."
curl -s -X POST "$KEYCLOAK_URL/admin/realms/toolhive/clients" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "clientId": "mcp-server",
    "enabled": true,
    "publicClient": false,
    "secret": "PLOs4j6ti521kb5ZVVwi5GWi9eDYTwq",
    "serviceAccountsEnabled": true,
    "standardFlowEnabled": false,
    "directAccessGrantsEnabled": false,
    "attributes": {
      "standard.token.exchange.enabled": "true"
    },
    "description": "Confidential client for MCP server"
  }' || echo "Client may already exist"

# Create client scope for backend access
echo "Creating backend-access client scope..."
curl -s -X POST "$KEYCLOAK_URL/admin/realms/toolhive/client-scopes" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "backend-access",
    "description": "Adds backend to token audience for backend service access",
    "protocol": "openid-connect",
    "attributes": {
      "include.in.token.scope": "true",
      "display.on.consent.screen": "false"
    }
  }' || echo "Client scope may already exist"

# Get the backend-access client scope ID
BACKEND_SCOPE_ID=$(curl -s -H "Authorization: Bearer $TOKEN" \
  "$KEYCLOAK_URL/admin/realms/toolhive/client-scopes" | \
  jq -r '.[] | select(.name=="backend-access") | .id')

if [ "$BACKEND_SCOPE_ID" != "null" ] && [ -n "$BACKEND_SCOPE_ID" ]; then
  echo "Adding backend audience mapper to client scope..."
  curl -s -X POST "$KEYCLOAK_URL/admin/realms/toolhive/client-scopes/$BACKEND_SCOPE_ID/protocol-mappers/models" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d '{
      "name": "backend-audience-mapper",
      "protocol": "openid-connect",
      "protocolMapper": "oidc-audience-mapper",
      "config": {
        "included.custom.audience": "backend",
        "id.token.claim": "false",
        "access.token.claim": "true"
      }
    }' || echo "Backend audience mapper may already exist"

  # Assign the backend-access scope as optional to mcp-server
  MCP_SERVER_CLIENT_ID=$(curl -s -H "Authorization: Bearer $TOKEN" \
    "$KEYCLOAK_URL/admin/realms/toolhive/clients" | \
    jq -r '.[] | select(.clientId=="mcp-server") | .id')

  if [ "$MCP_SERVER_CLIENT_ID" != "null" ] && [ -n "$MCP_SERVER_CLIENT_ID" ]; then
    echo "Assigning backend-access scope to mcp-server as optional..."
    curl -s -X PUT "$KEYCLOAK_URL/admin/realms/toolhive/clients/$MCP_SERVER_CLIENT_ID/optional-client-scopes/$BACKEND_SCOPE_ID" \
      -H "Authorization: Bearer $TOKEN" || echo "Scope assignment may already exist"
  fi
fi

# Create users
echo "Creating toolhive-admin..."
curl -s -X POST "$KEYCLOAK_URL/admin/realms/toolhive/users" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "toolhive-admin",
    "enabled": true,
    "email": "admin@toolhive.example.com",
    "emailVerified": true,
    "firstName": "ToolHive",
    "lastName": "Admin",
    "credentials": [{
      "type": "password",
      "value": "admin123",
      "temporary": false
    }]
  }' || echo "User may already exist"

echo "Creating toolhive-user..."
curl -s -X POST "$KEYCLOAK_URL/admin/realms/toolhive/users" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "toolhive-user",
    "enabled": true,
    "email": "user@toolhive.example.com",
    "emailVerified": true,
    "firstName": "ToolHive", 
    "lastName": "User",
    "credentials": [{
      "type": "password",
      "value": "user123",
      "temporary": false
    }]
  }' || echo "User may already exist"

echo "Creating toolhive-readonly..."
curl -s -X POST "$KEYCLOAK_URL/admin/realms/toolhive/users" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "toolhive-readonly",
    "enabled": true,
    "email": "readonly@toolhive.example.com",
    "emailVerified": true,
    "firstName": "ToolHive",
    "lastName": "ReadOnly",
    "credentials": [{
      "type": "password",
      "value": "readonly123",
      "temporary": false
    }]
  }' || echo "User may already exist"

# Create client scope for audience mapping
echo "Creating mcp-server-audience client scope..."
curl -s -X POST "$KEYCLOAK_URL/admin/realms/toolhive/client-scopes" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "mcp-server-audience",
    "description": "Adds mcp-server to token audience",
    "protocol": "openid-connect",
    "attributes": {
      "include.in.token.scope": "true",
      "display.on.consent.screen": "false"
    }
  }' || echo "Client scope may already exist"

# Get the client scope ID
SCOPE_ID=$(curl -s -H "Authorization: Bearer $TOKEN" \
  "$KEYCLOAK_URL/admin/realms/toolhive/client-scopes" | \
  jq -r '.[] | select(.name=="mcp-server-audience") | .id')

if [ "$SCOPE_ID" != "null" ] && [ -n "$SCOPE_ID" ]; then
  echo "Adding audience mapper to client scope..."
  curl -s -X POST "$KEYCLOAK_URL/admin/realms/toolhive/client-scopes/$SCOPE_ID/protocol-mappers/models" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d '{
      "name": "mcp-server-audience-mapper",
      "protocol": "openid-connect",
      "protocolMapper": "oidc-audience-mapper",
      "config": {
        "included.client.audience": "mcp-server",
        "id.token.claim": "false",
        "access.token.claim": "true"
      }
    }' || echo "Audience mapper may already exist"

  # Assign the client scope as default to mcp-test-client
  CLIENT_ID=$(curl -s -H "Authorization: Bearer $TOKEN" \
    "$KEYCLOAK_URL/admin/realms/toolhive/clients" | \
    jq -r '.[] | select(.clientId=="mcp-test-client") | .id')

  if [ "$CLIENT_ID" != "null" ] && [ -n "$CLIENT_ID" ]; then
    echo "Assigning audience scope to mcp-test-client..."
    curl -s -X PUT "$KEYCLOAK_URL/admin/realms/toolhive/clients/$CLIENT_ID/default-client-scopes/$SCOPE_ID" \
      -H "Authorization: Bearer $TOKEN" || echo "Scope assignment may already exist"
  fi
fi

echo "ToolHive realm setup complete!"
echo ""
echo "Access your realm at: $KEYCLOAK_URL/admin/master/console/#/toolhive"
echo "Users created:"
echo "   - toolhive-admin (admin123)"
echo "   - toolhive-user (user123)" 
echo "   - toolhive-readonly (readonly123)"
echo "Clients created:"
echo "   - mcp-test-client (confidential, secret: mcp-test-client-secret, for user authentication)"
echo "   - mcp-server (confidential, secret: PLOs4j6ti521kb5ZVVwi5GWi9eDYTwq, token exchange enabled)"
echo ""
echo "Client scopes created:"
echo "   - backend-access (adds 'backend' to token audience, assigned to mcp-server as optional)"
echo ""
echo "Token exchange test commands:"
echo "   # Get user token:"
echo "   TOKEN=\$(curl -s -d \"client_id=mcp-test-client\" -d \"client_secret=mcp-test-client-secret\" -d \"username=toolhive-user\" -d \"password=user123\" -d \"grant_type=password\" \"http://localhost:8080/realms/toolhive/protocol/openid-connect/token\" | jq -r '.access_token')"
echo ""
echo "   # mcp-server exchanges user token for backend audience (using scope):"
echo "   curl -s -d \"grant_type=urn:ietf:params:oauth:grant-type:token-exchange\" \\"
echo "        -d \"client_id=mcp-server\" \\"
echo "        -d \"client_secret=PLOs4j6ti521kb5ZVVwi5GWi9eDYTwq\" \\"
echo "        -d \"subject_token=\$TOKEN\" \\"
echo "        -d \"subject_token_type=urn:ietf:params:oauth:token-type:access_token\" \\"
echo "        -d \"scope=backend-access\" \\"
echo "        \"http://localhost:8080/realms/toolhive/protocol/openid-connect/token\""

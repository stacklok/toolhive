# OAuth CRD Integration Test

E2E test for Phase 10b: OAuth type in MCPExternalAuthConfig CRD.

## Architecture Overview

This setup uses **two ngrok domains** to work around ngrok's 15-rule policy limit:

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                              Multi-Domain Architecture                               │
├─────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                     │
│  Domain 1: <YOUR-MCP-DOMAIN>                                                        │
│  ├── /oauth-test/mcp                          → MCP endpoint (auth enforced)        │
│  └── /.well-known/oauth-protected-resource/*  → RFC 9728 protected resource metadata│
│                                                                                     │
│  Domain 2: <YOUR-OAUTH-DOMAIN>                                                      │
│  ├── /oauth-test/.well-known/openid-configuration  → OIDC discovery                 │
│  ├── /oauth-test/.well-known/jwks.json             → JWKS endpoint                  │
│  └── /oauth-test/oauth/*                           → OAuth flow (authorize/callback/token)
│                                                                                     │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

### Why Two Domains?

ngrok's free tier has a **15-rule policy limit** per domain. Each HTTPRoute rule consumes:
- 3 fixed rules (base policy overhead)
- 1 rule per plain route (no URL rewrite)
- 2 rules per route with URL rewrite

With 7 endpoints needing URL rewrites, we exceeded the limit on a single domain.

## OAuth Flow Overview

There are **two separate OAuth flows** happening:

```
┌──────────┐              ┌─────────────────────────────────┐              ┌─────────────────┐
│  VSCode  │ ────A────►   │   Proxy Auth Server             │ ────B────►   │     Google      │
│ (Client) │              │  (toolhive.ngrok.app)           │              │ (Upstream IDP)  │
└──────────┘              └─────────────────────────────────┘              └─────────────────┘

Flow A: VSCode authenticates to Proxy (Proxy is IDP to VSCode)
Flow B: Proxy authenticates to Google (Google is IDP to Proxy)
```

### Redirect URIs - CRITICAL

| Where to Configure | Redirect URI | Purpose |
|--------------------|--------------|---------|
| **Google Cloud Console** | `https://<YOUR-OAUTH-DOMAIN>/oauth-test/oauth/callback` | Google → Proxy callback (Flow B) |
| **MCPExternalAuthConfig clients** | `http://127.0.0.1:33418/`, `http://127.0.0.1:57879/` | Proxy → VSCode callback (Flow A) |

### URL Mapping via Gateway

**Domain 1 (MCP + Protected Resource):**

| External URL (ngrok) | Internal URL (pod) | Rewrite Type |
|---------------------|-------------------|--------------|
| `/oauth-test/mcp` | `/mcp` | ReplaceFullPath |
| `/.well-known/oauth-protected-resource/*` | (no rewrite) | None |

**Domain 2 (OAuth Endpoints):**

| External URL (ngrok) | Internal URL (pod) | Rewrite Type |
|---------------------|-------------------|--------------|
| `/oauth-test/.well-known/openid-configuration` | `/.well-known/openid-configuration` | ReplaceFullPath |
| `/oauth-test/.well-known/jwks.json` | `/.well-known/jwks.json` | ReplaceFullPath |
| `/oauth-test/oauth/*` | `/oauth/*` | ReplacePrefixMatch |

**Important:** OAuth endpoints (`/oauth/*`) use `ReplacePrefixMatch` instead of `ReplaceFullPath` because `ReplaceFullPath` strips query parameters, which are required for OAuth flows (client_id, state, code_challenge, etc.).

## Prerequisites

- K8s cluster with ToolHive operator deployed
- Updated CRDs with OAuth support installed
- Gateway API CRDs installed
- ngrok operator installed with credentials
- Two ngrok domains configured (replace placeholders with your actual domains):
  - `<YOUR-MCP-DOMAIN>` - for MCP endpoint
  - `<YOUR-OAUTH-DOMAIN>` - for OAuth endpoints
- Google OAuth credentials from Google Cloud Console

## Files

| File | Purpose |
|------|---------|
| `00-namespace.yaml` | Creates `oauth-test` namespace |
| `01-secrets.yaml` | RSA signing key and Google OAuth client secret |
| `02-mcpexternalauthconfig.yaml` | OAuth configuration (auth server + upstream IDP) |
| `03-mcpserver.yaml` | MCPServer with OAuth and OIDC validation |
| `04-gateway.yaml` | Two Gateways and HTTPRoutes for multi-domain setup |

## Secret Generation

Before deploying, you need to generate secrets and configure OAuth credentials:

### 1. Generate RSA Private Key

Generate a 2048-bit RSA private key for signing JWT tokens:

```bash
openssl genrsa -out private.pem 2048
```

This creates a file `private.pem` with content like:
```
-----BEGIN PRIVATE KEY-----
MIIEvAIBADANBgkqhkiG9w0BAQEFAASCBKYwggSiAgEAAoIBAQC...
...
-----END PRIVATE KEY-----
```

### 2. Update `01-secrets.yaml`

Replace the placeholder in `01-secrets.yaml`:

```yaml
stringData:
  private.pem: |
    <YOUR-RSA-PRIVATE-KEY-HERE>
```

With your generated key (copy the entire output from `private.pem`, including the BEGIN/END lines).

### 3. Get Google OAuth Credentials

1. Go to [Google Cloud Console](https://console.cloud.google.com)
2. Create a new project or select an existing one
3. Navigate to **APIs & Services > Credentials**
4. Click **Create Credentials > OAuth 2.0 Client ID**
5. Choose application type: **Web application**
6. Add authorized redirect URI: `https://<YOUR-OAUTH-DOMAIN>/oauth-test/oauth/callback`
   - Replace `<YOUR-OAUTH-DOMAIN>` with your actual ngrok domain for OAuth endpoints
7. Click **Create** - you'll receive:
   - **Client ID**: (e.g., `123456789-abc123.apps.googleusercontent.com`)
   - **Client Secret**: (e.g., `GOCSPX-abc123xyz`)

### 4. Update Configuration Files

Replace these placeholders in the manifest files:

**In `01-secrets.yaml`:**
- `<YOUR-RSA-PRIVATE-KEY-HERE>` - your generated RSA private key
- `<YOUR-GOOGLE-CLIENT-SECRET>` - the client secret from Google Cloud Console

**In `02-mcpexternalauthconfig.yaml`:**
- `<YOUR-OAUTH-DOMAIN>` - your ngrok domain for OAuth endpoints (e.g., `example.ngrok.app`)
- `<YOUR-GOOGLE-CLIENT-ID>` - the client ID from Google Cloud Console

**In `04-gateway.yaml`:**
- `<YOUR-MCP-DOMAIN>` - your ngrok domain for MCP endpoint (e.g., `mcp.ngrok.app`)
- `<YOUR-OAUTH-DOMAIN>` - your ngrok domain for OAuth endpoints (e.g., `auth.ngrok.app`)

Note: You need two separate ngrok domains due to ngrok's 15-rule policy limit.

## Setup

### 0. Create namespace

```bash
kubectl apply -f 00-namespace.yaml
```

### 1. Install Gateway API CRDs (if not already installed)

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml
```

### 2. Install ngrok operator (if not already installed)

```bash
export NGROK_API_KEY="your-api-key"
export NGROK_AUTHTOKEN="your-authtoken"

# Add helm repo
helm repo add ngrok https://ngrok.github.io/kubernetes-ingress-controller
helm repo update ngrok

# Install operator
helm upgrade --install ngrok-operator ngrok/ngrok-operator \
    --namespace ngrok-operator \
    --create-namespace \
    --set credentials.apiKey="${NGROK_API_KEY}" \
    --set credentials.authtoken="${NGROK_AUTHTOKEN}"

# Wait for operator to be ready
kubectl wait --for=condition=available --timeout=120s deployment/ngrok-operator-manager -n ngrok-operator

# Apply GatewayClass
kubectl apply -f ../google-passthrough-test/ngrok/02-gatewayclass.yaml
```

### 3. Create secrets

```bash
kubectl apply -f 01-secrets.yaml
```

### 4. Create OAuth config and MCPServer

```bash
kubectl apply -f 02-mcpexternalauthconfig.yaml
kubectl apply -f 03-mcpserver.yaml
```

### 5. Deploy Gateway and HTTPRoutes

```bash
kubectl apply -f 04-gateway.yaml
```

## Verification

### Check resources created

```bash
kubectl get mcpexternalauthconfig -n oauth-test google-oauth
kubectl get mcpserver -n oauth-test oauth-test-server
kubectl get pods -n oauth-test
kubectl get gateway,httproute -n oauth-test
```

### Verify pod configuration

```bash
# Check volumes (should have auth-server-signing-key)
kubectl get pod -n oauth-test -l app.kubernetes.io/name=oauth-test-server -o jsonpath='{.items[0].spec.volumes[*].name}' | tr ' ' '\n'

# Check volume mounts (should mount at /etc/authserver)
kubectl get pod -n oauth-test -l app.kubernetes.io/name=oauth-test-server -o jsonpath='{.items[0].spec.containers[0].volumeMounts}' | jq .

# Check env vars (should have TOOLHIVE_OAUTH_UPSTREAM_CLIENT_SECRET)
kubectl get pod -n oauth-test -l app.kubernetes.io/name=oauth-test-server -o jsonpath='{.items[0].spec.containers[0].env}' | jq .
```

### Test auth server endpoints (via ngrok)

Replace `<YOUR-MCP-DOMAIN>` and `<YOUR-OAUTH-DOMAIN>` with your actual domains:

```bash
# Domain 1: MCP endpoint (should return 401 - auth enforced)
curl -s -o /dev/null -w "HTTP: %{http_code}\n" -X POST \
  https://<YOUR-MCP-DOMAIN>/oauth-test/mcp

# Domain 1: Protected Resource Metadata (RFC 9728)
curl -s https://<YOUR-MCP-DOMAIN>/.well-known/oauth-protected-resource/oauth-test/mcp | jq .

# Domain 2: OIDC Discovery
curl -s https://<YOUR-OAUTH-DOMAIN>/oauth-test/.well-known/openid-configuration | jq .

# Domain 2: JWKS
curl -s https://<YOUR-OAUTH-DOMAIN>/oauth-test/.well-known/jwks.json | jq .

# Domain 2: Authorize (should return 302 redirect to Google)
curl -sI "https://<YOUR-OAUTH-DOMAIN>/oauth-test/oauth/authorize?client_id=vscode&redirect_uri=http://127.0.0.1:33418/&response_type=code&scope=openid&state=test&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256" | grep -i location
```

### Test full OAuth flow (manual browser test)

Replace `<YOUR-OAUTH-DOMAIN>` with your actual OAuth domain:

```bash
# Open in browser - will redirect to Google login
open "https://<YOUR-OAUTH-DOMAIN>/oauth-test/oauth/authorize?client_id=vscode&redirect_uri=http://127.0.0.1:33418/&response_type=code&scope=openid&state=xyz&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256"
```

**Expected flow:**
1. Browser redirects to Google login
2. After Google auth, Google redirects to `https://<YOUR-OAUTH-DOMAIN>/oauth-test/oauth/callback`
3. Proxy processes callback and redirects to `http://127.0.0.1:33418/?code=PROXY_CODE&state=xyz`
4. Browser shows "connection refused" (expected - VSCode isn't listening)
5. **SUCCESS** if you see the `code=` parameter in the URL - the flow completed!

## Test Results (Example from Original Setup - 2025-12-17)

These results are from the original test setup. Your results should be similar when using your own domains and credentials.

| Check | Status | Notes |
|-------|--------|-------|
| MCPExternalAuthConfig created | ✅ | `type: oauth` with nested authServer/upstream |
| MCPServer pod running | ✅ | Both pods healthy |
| Signing key volume mounted | ✅ | At `/etc/authserver/signing-key.pem` |
| Upstream secret env var present | ✅ | `TOOLHIVE_OAUTH_UPSTREAM_CLIENT_SECRET` |
| Gateway/HTTPRoutes created | ✅ | Multi-domain setup working |
| MCP endpoint (Domain 1) | ✅ | Returns 401 (auth enforced) |
| Protected Resource Metadata (Domain 1) | ✅ | Returns RFC 9728 JSON |
| OIDC Discovery (Domain 2) | ✅ | Returns valid OIDC config |
| JWKS (Domain 2) | ✅ | Returns RSA public key |
| OAuth authorize (Domain 2) | ✅ | 302 redirects to Google with query params |
| Full OAuth flow | ✅ | Google → callback → client redirect |

### Debug Logging

The proxy includes debug middleware that logs all incoming requests:
```
DEBUG REQUEST: GET /oauth/authorize?client_id=vscode&redirect_uri=...
```

This was added to troubleshoot ngrok URL rewrite issues (ReplaceFullPath strips query params).

### Via Port-Forward (verified working)

```bash
kubectl port-forward -n oauth-test svc/mcp-oauth-test-server-proxy 8080:8080 &
curl http://localhost:8080/.well-known/openid-configuration
curl http://localhost:8080/.well-known/jwks.json
curl http://localhost:8080/.well-known/oauth-protected-resource/oauth-test/mcp
```

## Troubleshooting

### ngrok rule limit exceeded

If you see policy errors about rule limits, split endpoints across multiple domains as shown in `04-gateway.yaml`.

### Query parameters stripped

If OAuth endpoints return `400 Bad Request: client_id is required`, the URL rewrite is stripping query params. Use `ReplacePrefixMatch` instead of `ReplaceFullPath` for OAuth routes.

### Pod not starting
```bash
kubectl describe pod -n oauth-test -l app.kubernetes.io/name=oauth-test-server
kubectl logs -n oauth-test -l app.kubernetes.io/name=oauth-test-server
```

### Gateway not routing
```bash
kubectl describe gateway -n oauth-test
kubectl describe httproute -n oauth-test
```

### OAuth errors
```bash
# Check proxy logs for auth server errors (includes DEBUG REQUEST lines)
kubectl logs -n oauth-test -l app.kubernetes.io/name=oauth-test-server -f
```

## Cleanup

```bash
kubectl delete -f 04-gateway.yaml
kubectl delete -f 03-mcpserver.yaml
kubectl delete -f 02-mcpexternalauthconfig.yaml
kubectl delete -f 01-secrets.yaml
kubectl delete -f 00-namespace.yaml
```

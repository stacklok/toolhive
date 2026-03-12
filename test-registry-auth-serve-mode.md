# Testing Registry Auth Serve Mode (PR 2A)

## Prerequisites

- Go installed
- ToolHive repo checked out on `feat/registry-auth-serve-mode` branch

## Build

```bash
go build -o ./bin/thv ./cmd/thv/
```

## Scenario A: Public registry (no auth required)

This uses the default embedded registry. No auth configuration needed.

### Start the server

```bash
./bin/thv config set-registry ""
./bin/thv serve --port 18080
```

In a separate terminal, run the curl commands below.

### 1. List registries

```bash
curl -s http://localhost:18080/api/v1beta/registry | python3 -m json.tool
```

Expected: `auth_status` is `"none"` and `auth_type` is `""`.

```json
{
    "registries": [
        {
            "name": "default",
            "version": "1.0.0",
            "last_updated": "...",
            "server_count": 79,
            "type": "default",
            "source": "",
            "auth_status": "none",
            "auth_type": ""
        }
    ]
}
```

### 2. Get default registry details

```bash
curl -s http://localhost:18080/api/v1beta/registry/default | python3 -m json.tool | head -15
```

Expected: Same `auth_status`/`auth_type` fields, plus full registry contents.

### 3. List servers

```bash
curl -s http://localhost:18080/api/v1beta/registry/default/servers | python3 -m json.tool | head -40
```

Expected: Array of server objects with name, description, tools, image, permissions, etc.

Stop the server with Ctrl+C.

## Scenario B: Stacklok internal registry (auth required)

This uses the Stacklok registry which requires authentication. Without credentials configured, all registry endpoints return a structured 503 error.

### Configure the registry

```bash
./bin/thv config set-registry https://toolhive-registry.stacklok.dev/registry/toolhive
```

### Start the server

```bash
./bin/thv serve --port 18080
```

### 1. List registries — expect 503

```bash
curl -s -w "\nHTTP_CODE: %{http_code}" http://localhost:18080/api/v1beta/registry
```

Expected:

```
{"code":"registry_auth_required","message":"Registry authentication required. Run 'thv registry login' to authenticate."}

HTTP_CODE: 503
```

### 2. Get default registry — expect 503

```bash
curl -s -w "\nHTTP_CODE: %{http_code}" http://localhost:18080/api/v1beta/registry/default
```

Expected: Same 503 response.

### 3. List servers — expect 503

```bash
curl -s -w "\nHTTP_CODE: %{http_code}" http://localhost:18080/api/v1beta/registry/default/servers
```

Expected: Same 503 response.

### What Studio sees

Studio receives the JSON body with `code: "registry_auth_required"`. Currently it will display the error message. Once PR 2B lands with the login endpoint, Studio can detect this code and prompt the user to authenticate.

### 4. Configure OAuth (sets auth_status to "configured")

Stop the server, then:

```bash
./bin/thv config set-registry-auth --issuer https://auth.example.com --client-id my-client
./bin/thv serve --port 18080
```

```bash
curl -s http://localhost:18080/api/v1beta/registry | python3 -m json.tool
```

Expected: `auth_status` is `"configured"` and `auth_type` is `"oauth"`. The registry still returns a 503 because no token has been obtained yet.

After a successful `thv registry login` (PR 2B, not yet implemented), `auth_status` would become `"authenticated"` and the registry endpoints would return data.

## Cleanup

```bash
# Reset to default embedded registry
./bin/thv config set-registry ""
```

Stop the server with Ctrl+C.

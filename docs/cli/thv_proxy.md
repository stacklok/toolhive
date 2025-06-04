## thv proxy

Spawn a transparent proxy for an MCP server

### Synopsis

Spawn a transparent proxy that will redirect to an MCP server endpoint.
This command creates a standalone proxy without starting a container.

```
thv proxy [flags] SERVER_NAME
```

### Options

```
  -h, --help                               help for proxy
      --host string                        Host for the HTTP proxy to listen on (IP or hostname) (default "127.0.0.1")
      --oidc-audience string               Expected audience for the token
      --oidc-client-id string              OIDC client ID
      --oidc-issuer string                 OIDC issuer URL (e.g., https://accounts.google.com)
      --oidc-jwks-url string               URL to fetch the JWKS from
      --port int                           Port for the HTTP proxy to listen on (host port)
      --remote-auth                        Enable OAuth authentication to remote MCP server
      --remote-auth-client-id string       OAuth client ID for remote server authentication
      --remote-auth-client-secret string   OAuth client secret for remote server authentication (optional for PKCE)
      --remote-auth-issuer string          OAuth/OIDC issuer URL for remote server authentication (e.g., https://accounts.google.com)
      --remote-auth-scopes strings         OAuth scopes to request for remote server authentication (default [openid,profile,email])
      --remote-auth-skip-browser           Skip opening browser for remote server OAuth flow
      --target-uri string                  URI for the target MCP server (e.g., http://localhost:8080) (required)
```

### Options inherited from parent commands

```
      --debug   Enable debug mode
```

### SEE ALSO

* [thv](thv.md)	 - ToolHive (thv) is a lightweight, secure, and fast manager for MCP servers


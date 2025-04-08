## thv run

Run an MCP server

### Synopsis

Run an MCP server in a container with the specified server name or image and arguments.
If a server name is provided, it will first try to find it in the registry.
If found, it will use the registry defaults for transport, permissions, etc.
If not found, it will treat the argument as a Docker image and run it directly.
The container will be started with minimal permissions and the specified transport mode.

```
thv run [flags] SERVER_OR_IMAGE [-- ARGS...]
```

### Options

```
      --authz-config string         Path to the authorization configuration file
  -e, --env stringArray             Environment variables to pass to the MCP server (format: KEY=VALUE)
  -f, --foreground                  Run in foreground mode (block until container exits)
  -h, --help                        help for run
      --name string                 Name of the MCP server (auto-generated from image if not provided)
      --oidc-audience string        Expected audience for the token
      --oidc-client-id string       OIDC client ID
      --oidc-issuer string          OIDC issuer URL (e.g., https://accounts.google.com)
      --oidc-jwks-url string        URL to fetch the JWKS from
      --permission-profile string   Permission profile to use (stdio, network, or path to JSON file) (default "stdio")
      --port int                    Port for the HTTP proxy to listen on (host port)
      --secret stringArray          Specify a secret to be fetched from the secrets manager and set as an environment variable (format: NAME,target=TARGET)
      --target-host string          Host to forward traffic to (only applicable to SSE transport) (default "localhost")
      --target-port int             Port for the container to expose (only applicable to SSE transport)
      --transport string            Transport mode (sse or stdio) (default "stdio")
  -v, --volume stringArray          Mount a volume into the container (format: host-path:container-path[:ro])
```

### Options inherited from parent commands

```
      --debug   Enable debug mode
```

### SEE ALSO

* [thv](thv.md)	 - ToolHive (thv) is a lightweight, secure, and fast manager for MCP servers


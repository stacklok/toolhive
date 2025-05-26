## thv run

Run an MCP server

### Synopsis

Run an MCP server with the specified name, image, or protocol scheme.

ToolHive supports three ways to run an MCP server:

1. From the registry:
   $ thv run server-name [-- args...]
   Looks up the server in the registry and uses its predefined settings
   (transport, permissions, environment variables, etc.)

2. From a container image:
   $ thv run ghcr.io/example/mcp-server:latest [-- args...]
   Runs the specified container image directly with the provided arguments

3. Using a protocol scheme:
   $ thv run uvx://package-name [-- args...]
   $ thv run npx://package-name [-- args...]
   $ thv run go://package-name [-- args...]
   Automatically generates a container that runs the specified package
   using either uvx (Python with uv package manager), npx (Node.js),
   or go (Golang)

The container will be started with the specified transport mode and
permission profile. Additional configuration can be provided via flags.

```
thv run [flags] SERVER_OR_IMAGE_OR_PROTOCOL [-- ARGS...]
```

### Options

```
      --authz-config string         Path to the authorization configuration file
      --ca-cert string              Path to a custom CA certificate file to use for container builds
  -e, --env stringArray             Environment variables to pass to the MCP server (format: KEY=VALUE)
  -f, --foreground                  Run in foreground mode (block until container exits)
  -h, --help                        help for run
      --host string                 Host for the HTTP proxy to listen on (IP or hostname) (default "127.0.0.1")
      --image-verification string   Set image verification mode (warn, enabled, disabled) (default "warn")
      --name string                 Name of the MCP server (auto-generated from image if not provided)
      --oidc-audience string        Expected audience for the token
      --oidc-client-id string       OIDC client ID
      --oidc-issuer string          OIDC issuer URL (e.g., https://accounts.google.com)
      --oidc-jwks-url string        URL to fetch the JWKS from
      --permission-profile string   Permission profile to use (none, network, or path to JSON file) (default "network")
      --port int                    Port for the HTTP proxy to listen on (host port)
      --secret stringArray          Specify a secret to be fetched from the secrets manager and set as an environment variable (format: NAME,target=TARGET)
      --target-host string          Host to forward traffic to (only applicable to SSE transport) (default "127.0.0.1")
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


# Vibe Tool (vt)

Vibe Tool (vt) is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers. It is written in Rust and has extensive test coverage—including input validation—to ensure reliability and security.

Under the hood, Vibe Tool acts as a very thin client for the Docker/Podman Unix socket API. This design choice allows it to remain both efficient and lightweight while still providing powerful, container-based isolation for running MCP servers.

## Why Vibe Tool?

Existing ways to start MCP servers are viewed as insecure, often granting containers more privileges than necessary. vibe tool aims to solve this by starting containers in a locked-down environment, granting only the minimal permissions required to run. This significantly reduces the attack surface and enforces best practices for container security.

## Commands

The vt command-line interface provides the following subcommands:

* `vt run` Runs an MCP server using the default STDIO transport.

* `vt run --transport=sse` Runs an SSE MCP server.

* `vt list` Lists running MCP servers.

* `vt stop` Stops an MCP server.

* `vt rm` Removes an MCP server.

* `vt help` Displays help information.

* `vt version` Shows the current version of Vibe Tool.

* `vt (no subcommand)` Starts an MCP server that itself is used to manage Vibe Tool servers.

## Usage

### Running an MCP Server

To run an MCP server, use the following command:

```bash
vt run --transport sse --name my-mcp-server --port 8080 my-mcp-server-image:latest -- my-mcp-server-args
```

This command closely resembles `docker run` but focuses on security and simplicity. When invoked:

* Vibe Tool creates a container from the specified image (`my-mcp-server-image:latest`).

* It configures the container to listen on the chosen port (8080).

* Labels the container so it can be tracked by Vibe Tool:

    ```yaml
    vibetool: true
    vibetool-name: my-mcp-server
    ```

* Sets up the specified transport (e.g., SSE, stdio), potentially using a reverse proxy, depending on user choice.

### Transport Modes

* **SSE**:

    If the transport is `sse`, Vibe Tool creates a reverse proxy a random that forwards requests to the container. This means the container itself does not directly expose any ports.

* **STDIO**:

    If the transport is `stdio`, Vibe Tool redirects SSE traffic to the container's standard input and output.
    This acts as a secure proxy, ensuring that the container does not have direct access to the network nor
    the host machine.

## Permissions

Containers launched by Vibe Tool come with a minimal set of permissions, strictly limited to what is required. Permissions can be further customized via a JSON-based permission profile provided with the `--permission-profile` flag.

An example permission profile file could be:

```json
{
  "read": [
    "/var/run/mcp.sock"
  ],
  "write": [
    "/var/run/mcp.sock"
  ],
  "network": {
    "outbound": {
      "insecure_allow_all": false,
      "allow_transport": [
        "tcp",
        "udp"
      ],
      "allow_host": [
        "localhost",
        "google.com"
      ],
      "allow_port": [
        80,
        443
      ]
    }
  }
}
```

This profile lets the container read and write to the `/var/run/mcp.sock` Unix socket and also make outbound network requests to `localhost` and `google.com` on ports `80` and `443`.

Two built-in profiles are included for convenience:

* `stdio`: Grants minimal permissions with no network access.
* `network`: Permits outbound network connections to any host on any port (not recommended for production use).

## Listing Running MCP Servers

Use:

```bash
vt list
```

This lists all active MCP servers managed by Vibe Tool, along with their current status.

## License

This project is licensed under the Apache 2.0 License. See the LICENSE file for details.

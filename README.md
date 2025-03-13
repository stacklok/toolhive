# mcp-lok

mcp-lok is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers. It is written in Rust and has extensive test coverage—including input validation—to ensure reliability and security.

## Overview

Under the hood, mcp-lok acts as a very thin client for the Docker/Podman Unix socket API. This design choice allows it to remain both efficient and lightweight while still providing powerful, container-based isolation for running MCP servers.

## Why mcp-lok?

Existing ways to start MCP servers are viewed as insecure, often granting containers more privileges than necessary. mcp-lok aims to solve this by starting containers in a locked-down environment, granting only the minimal permissions required to run. This significantly reduces the attack surface and enforces best practices for container security.

## Installation

### From Source

```bash
git clone https://github.com/stacklok/mcp-lok.git
cd mcp-lok
cargo build --release
```

The binary will be available at `target/release/mcp-lok`.

## Commands

The mcp-lok command-line interface provides the following subcommands:

* `mcp-lok run` - Runs an MCP server.
* `mcp-lok list` - Lists running MCP servers.
* `mcp-lok start` - Starts an MCP server and sends it to the background.
* `mcp-lok stop` - Stops an MCP server.
* `mcp-lok rm` - Removes an MCP server.
* `mcp-lok help` - Displays help information.
* `mcp-lok version` - Shows the current version of mcp-lok.
* `mcp-lok (no subcommand)` - Starts an MCP server that itself is used to manage mcp-lok servers.

## Usage

### Running an MCP Server

To run an MCP server, use the following command:

```bash
mcp-lok run --transport sse --name my-mcp-server --port 8080 my-mcp-server-image:latest -- my-mcp-server-args
```

This command closely resembles `docker run` but focuses on security and simplicity. When invoked:

* mcp-lok creates a container from the specified image (`my-mcp-server-image:latest`).
* It configures the container to listen on the chosen port (8080).
* Labels the container so it can be tracked by mcp-lok:
    ```yaml
    mcp-lok: true
    mcp-lok-name: my-mcp-server
    ```
* Sets up the specified transport (e.g., SSE, stdio), potentially using a reverse proxy or Unix socket, depending on user choice.

### Transport Modes

* **SSE**:
    If the transport is `sse`, mcp-lok creates a reverse proxy on port `8080` that forwards requests to the container. This means the container itself does not directly expose any ports.

* **STDIO**:
    If the transport is `stdio`, mcp-lok creates a Unix socket (`/var/run/mcp.sock`) that the container uses for I/O. This approach is highly secure and does not expose external ports.

### Permissions

Containers launched by mcp-lok come with a minimal set of permissions, strictly limited to what is required. By default, containers have access only to the `/var/run/mcp.sock` Unix socket. Permissions can be further customized via a JSON-based permission profile provided with the `--permission-profile` flag.

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

* `stdio`: Grants only read/write access to `/var/run/mcp.sock`.
* `network`: Permits outbound network connections to any host on any port (not recommended for production use).

### Listing Running MCP Servers

```bash
mcp-lok list
```

This lists all active MCP servers managed by mcp-lok, along with their current status.

### Stopping an MCP Server

```bash
mcp-lok stop my-mcp-server
```

### Removing an MCP Server

```bash
mcp-lok rm my-mcp-server
```

## License

This project is licensed under the Apache 2.0 License. See the LICENSE file for details.
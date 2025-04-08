# ToolHive (thv)

<img src="docs/images/toolhive.png" alt="ToolHive Logo" width="300" />

ToolHive (thv) is a lightweight, secure, and fast manager for MCP (Model Context
Protocol) servers. It is written in Golang and has extensive test
coverage—including input validation—to ensure reliability and security.

Under the hood, ToolHive acts as a very thin client for the Docker/Podman Unix
socket API. This design choice allows it to remain both efficient and
lightweight while still providing powerful, container-based isolation for
running MCP servers.

<img src="./docs/images/thv-readme-demo.svg" alt="Terminal Recording">

## Why ToolHive?

Deploying MCP servers requires complex multi-step processes with a lot of
friction: involving running random potentially harmful commands (e.g. using `uv`
or `npx`), manually managing security credentials (e.g. putting an api token
into a text file), and wrestling with inconsistent packaging methods. ToolHive
aims to solve this by starting containers in a locked-down environment, granting
only the minimal permissions required to run. This significantly reduces the
attack surface, improves usability, and enforces best practices for container
security.

ToolHive radically simplifies MCP deployment by:

- Ease of use: Instantly deploy MCP servers through Docker containers. Users can
  start their MCP servers with a single, straightforward command. No need to
  install and fight with different versions of python / node / etc.

- Enhanced security: Secure by default: the tool securely manages secrets and
  configurations, greatly reducing leaks & risks. No more plaintext secrets in
  configuration files

- Standardized packaging: Leveraging OCI container standards, the project
  provides a repeatable, standardized packaging method for MCP servers, ensuring
  compatibility and reliability.

### Key benefits

- Curated MCP registry: Includes a registry of curated MCPs with verified
  configurations — users can effortlessly discover and deploy MCP servers
  without any manual setup. Just select one from the list and safely run it with
  just one command.

- Enterprise-ready authorization: Offers robust authorization controls tailored
  for enterprise environments, securely managing tool access and integrating
  seamlessly with existing infrastructures (e.g., Kubernetes).

- Seamless integration: Compatible with popular development tools such as GitHub
  Copilot, Cursor, Roo Code, and more, streamlining your workflow.

## Getting started

TODO: Add simple installation instructions

## Usage

### Running an MCP server

First, find the MCP server you want to run. You can search for available MCP
servers in the registry using:

```bash
thv search <search-term>
```

This command will return a list of available MCP servers that match the search
term.

Once you find the MCP server you want to run, you can start it using the
`thv run` command. For example, to run a specific MCP server:

```bash
thv run <name-of-mcp-server>
```

The registry already contains all the parameters needed to run the server, so
you don't need to specify any additional arguments. ToolHive will automatically
pull the image and start the server.

### Listing running MCP servers

Use:

```bash
thv list
```

This lists all active MCP servers managed by ToolHive, along with their current
status.

### Browsing the registry

You can also browse the registry to see all available MCP servers. Use the
following command:

```bash
thv registry list
```

This will display a list of all available MCP servers in the registry, along
with their descriptions and other relevant information.

To view detailed information about a specific MCP server, use:

```bash
thv registry info <name-of-mcp-server>
```

This command will provide you with detailed information about the MCP server,
including its configuration, available options, and any other relevant details.

We're open to adding more MCP servers to the registry. If you have a specific
server in mind, please submit a pull request or open an issue on our GitHub
repository. We're tracking the the list in
[registry.json](pkg/registry/data/registry.json).

### Running a custom MCP server

If you want to run a custom MCP server that is not in the registry, you can do
so by specifying the image name and any additional arguments. For example:

```bash
thv run --transport sse --name my-mcp-server --port 8080 my-mcp-server-image:latest -- my-mcp-server-args
```

This command closely resembles `docker run` but focuses on security and
simplicity. When invoked:

- ToolHive creates a container from the specified image
  (`my-mcp-server-image:latest`).

- It configures the container to listen on the chosen port (8080).

- Labels the container so it can be tracked by ToolHive:

  ```yaml
  toolhive: true
  toolhive-name: my-mcp-server
  ```

- Sets up the specified transport (e.g., SSE, stdio), potentially using a
  reverse proxy, depending on user choice.

### Transport modes

- **SSE**:

  If the transport is `sse`, ToolHive creates a reverse proxy on a random port
  that forwards requests to the container. This means the container itself does
  not directly expose any ports.

- **STDIO**:

  If the transport is `stdio`, ToolHive redirects SSE traffic to the container's
  standard input and output. This acts as a secure proxy, ensuring that the
  container does not have direct access to the network nor the host machine.

## Permissions

Containers launched by ToolHive come with a minimal set of permissions, strictly
limited to what is required. Permissions can be further customized via a
JSON-based permission profile provided with the `--permission-profile` flag.

An example permission profile file could be:

```json
{
  "read": ["/var/run/mcp.sock"],
  "write": ["/var/run/mcp.sock"],
  "network": {
    "outbound": {
      "insecure_allow_all": false,
      "allow_transport": ["tcp", "udp"],
      "allow_host": ["localhost", "google.com"],
      "allow_port": [80, 443]
    }
  }
}
```

This profile lets the container read and write to the `/var/run/mcp.sock` Unix
socket and also make outbound network requests to `localhost` and `google.com`
on ports `80` and `443`.

Two built-in profiles are included for convenience:

- `stdio`: Grants minimal permissions with no network access.
- `network`: Permits outbound network connections to any host on any port (not
  recommended for production use).

## Client compatibility

| Client            | Supported | Notes                                  |
| ----------------- | --------- | -------------------------------------- |
| Copilot (VS Code) | ✅        | v1.99.0+ or Insiders version           |
| Cursor            | ✅        |                                        |
| Roo Code          | ✅        |                                        |
| PydanticAI        | ✅        |                                        |
| Continue          | ❌        | Continue doesn't yet support SSE       |
| Claude Desktop    | ❌        | Claude Desktop doesn't yet support SSE |

## Running ToolHive in a local kind cluster

To run ToolHive in a local lind cluster, follow the
[# Running ToolHive Inside a Local Kubernetes Kind Cluster With Ingress](./docs/running-toolhive-in-kind-cluster.md)
doc.

## License

This project is licensed under the Apache 2.0 License. See the
[LICENSE](./LICENSE) file for details.

<!-- markdownlint-disable-file MD033 -->

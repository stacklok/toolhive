# ToolHive (thv)

<img src="docs/images/toolhive.png" alt="ToolHive Logo" width="300" />

ToolHive (thv) is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers. It is written in Golang and has extensive test coverage—including input validation—to ensure reliability and security.

Under the hood, ToolHive acts as a very thin client for the Docker/Podman Unix socket API. This design choice allows it to remain both efficient and lightweight while still providing powerful, container-based isolation for running MCP servers.

## Why ToolHive?

Deploying MCP servers requires complex multi-step processes with a lot of friction: involving running random potentially harmful commands (e.g. using `uv` or `npx`), manually managing security credentials (e.g. putting an api token into a text file), and wrestling with inconsistent packaging methods. ToolHive aims to solve this by starting containers in a locked-down environment, granting only the minimal permissions required to run. This significantly reduces the attack surface, imporves usability, and enforces best practices for container security.

ToolHive radically simplifies MCP deployment by:
 - Ease of Use: Instantly deploy MCP servers through Docker containers. Users can start their MCP servers with a single, straightforward command. No need to install and fight with different versions of python / node / etc.

- Enhanced Security: Secure by default: the tool securely manages secrets and configurations, greatly reducing leaks & risks. No more plaintext secrets in configuration files

- Standardized Packaging: Leveraging OCI container standards, the project provides a repeatable, standardized packaging method for MCP servers, ensuring compatibility and reliability.


## Key Benefits
- Curated MCP Registry: Includes a registry of curated MCPs with verified configurations — users can effortlessly discover and deploy MCP servers without any manual setup. Just select one from the list and safely run it with just one command.

- Enterprise-Ready Authorization: Offers robust authorization controls tailored for enterprise environments, securely managing tool access and integrating seamlessly with existing infrastructures (e.g., Kubernetes).

- Seamless Integration: Compatible with popular development tools such as Copilot, Continue, Claude Desktop, Stitch, and more, streamlining your workflow.


## Commands

The thv command-line interface provides the following subcommands:

* `thv run` Runs an MCP server using the default STDIO transport.

* `thv run --transport=sse` Runs an SSE MCP server.

* `thv list` Lists running MCP servers.

* `thv stop` Stops an MCP server.

* `thv rm` Removes an MCP server.

* `thv help` Displays help information.

* `thv version` Shows the current version of ToolHive.

* `thv (no subcommand)` Starts an MCP server that itself is used to manage ToolHive servers.

## Usage

### Running an MCP Server

To run an MCP server, use the following command:

```bash
thv run --transport sse --name my-mcp-server --port 8080 my-mcp-server-image:latest -- my-mcp-server-args
```

This command closely resembles `docker run` but focuses on security and simplicity. When invoked:

* ToolHive creates a container from the specified image (`my-mcp-server-image:latest`).

* It configures the container to listen on the chosen port (8080).

* Labels the container so it can be tracked by ToolHive:

    ```yaml
    toolhive: true
    toolhive-name: my-mcp-server
    ```

* Sets up the specified transport (e.g., SSE, stdio), potentially using a reverse proxy, depending on user choice.

### Transport Modes

* **SSE**:

    If the transport is `sse`, ToolHive creates a reverse proxy a random that forwards requests to the container. This means the container itself does not directly expose any ports.

* **STDIO**:

    If the transport is `stdio`, ToolHive redirects SSE traffic to the container's standard input and output.
    This acts as a secure proxy, ensuring that the container does not have direct access to the network nor
    the host machine.

## Permissions

Containers launched by ToolHive come with a minimal set of permissions, strictly limited to what is required. Permissions can be further customized via a JSON-based permission profile provided with the `--permission-profile` flag.

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
thv list
```

This lists all active MCP servers managed by ToolHive, along with their current status.

## Running Against Local Kind Cluster

In order to run this against a local Kind Cluster, run:
- `task build-image` to build the image into the local registry, it should spit out the image URL
- `kind load docker-image $IMAGE_URL  --name $KIND_CLUSTER_NAME` to load it into the Kind cluster
- Create a `pod.yaml` spec for the pod, using the URL above as the image URL and `args:` field with the args you want to run. kind should create and run the pod.

## License

This project is licensed under the Apache 2.0 License. See the LICENSE file for details.

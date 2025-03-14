# mcp-lok

mcp-lok is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers. It is written in Rust and has extensive test coverage—including input validation—to ensure reliability and security.

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

* `mcp-lok run` Runs an MCP server.

* `mcp-lok list` Lists running MCP servers.

* `mcp-lok start` Starts an MCP server and sends it to the background.

* `mcp-lok stop` Stops an MCP server.

* `mcp-lok rm` Removes an MCP server.

* `mcp-lok help` Displays help information.

* `mcp-lok version` Shows the current version of mcp-lok.

* `mcp-lok (no subcommand)` Starts an MCP server that itself is used to manage mcp-lok servers.

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

* Sets up the specified transport (e.g., SSE, stdio), potentially using a reverse proxy, depending on user choice.

### Transport Modes

* **SSE**:

    If the transport is `sse`, mcp-lok creates a reverse proxy on port `8080` that forwards requests to the container. This means the container itself does not directly expose any ports.

* **STDIO**:

    If the transport is `stdio`, mcp-lok redirects SEE traffic to the container's standard input and output.
    This acts as a secure proxy, ensuring that the container does not have direct access to the network nor
    the host machine.

## Permissions

Containers launched by mcp-lok come with a minimal set of permissions, strictly limited to what is required. Permissions can be further customized via a JSON-based permission profile provided with the `--permission-profile` flag.

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
mcp-lok list
```

This lists all active MCP servers managed by mcp-lok, along with their current status.

## Development

### Running Tests

```bash
# Run all tests
cargo test

# Run unit tests only
cargo test --lib

# Run end-to-end tests
cargo test --test e2e
```

### Running BDD-style End-to-End Tests

The project includes comprehensive BDD-style end-to-end tests using cucumber-rs. These tests verify the functionality of the entire system from a user's perspective.

You can run these tests using the provided Makefile:

```bash
# Run all tests (unit and e2e)
make test

# Run only e2e tests
make test-e2e

# Run a specific feature or tag
make test-e2e-feature FEATURE=server_lifecycle
make test-e2e-feature FEATURE=@transport

# Run e2e tests with JUnit reports (for CI integration)
make test-e2e-junit

# Run e2e tests with verbose output
make test-e2e-verbose

# Show all available make targets
make help
```

The BDD tests are organized into five main feature areas:
1. Server lifecycle management (starting, stopping, removing servers)
2. CLI command functionality
3. Transport mechanisms (SSE and stdio)
4. Permission profiles and security constraints
5. MCP protocol implementation

### Building Documentation

```bash
cargo doc --open
```

### Code Coverage

This project uses [grcov](https://github.com/mozilla/grcov) to generate code coverage reports. The coverage setup is configured in the `coverage.sh` script and can be run using the `make coverage` command.

#### Running Code Coverage

To generate a code coverage report:

```bash
make coverage
```

This will:
1. Run the unit tests with coverage instrumentation
2. Generate an HTML coverage report in `target/coverage/html/`
3. Generate a Markdown summary report in `target/coverage/summary.md`

#### Current Coverage Status

The current code coverage is around 14%. The permissions module has good coverage (91.71%), but other modules like CLI commands and container implementations need more tests.

#### Areas for Improvement

Based on the coverage report, the following areas need more tests:

1. CLI Commands (0% coverage):
   - src/cli/commands/list.rs
   - src/cli/commands/rm.rs
   - src/cli/commands/run.rs
   - src/cli/commands/start.rs
   - src/cli/commands/stop.rs

2. Container Implementations (0% coverage):
   - src/container/docker.rs
   - src/container/podman.rs

3. Transport Implementations (partial coverage):
   - src/transport/sse.rs (26.06%)
   - src/transport/stdio.rs (9.89%)

## License

This project is licensed under the Apache 2.0 License. See the LICENSE file for details.
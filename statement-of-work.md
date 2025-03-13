The project is called `mcp-lok` and it's a lightweight, secure and fast manager for MCP (Model Context Protocol) Servers.

It's implemented in rust and has a lot of test coverage which also take input validation into account.

The command structure is as follows:

* `mcp-lok run`: Runs an MCP server
* `mcp-lok list`: Lists running MCP servers
* `mcp-lok start`: Starts an MCP server and sends it to the background
* `mcp-lok stop`: Stops an MCP server
* `mcp-lok rm`: Removes an MCP server
* `mcp-lok help`: Shows the help message
* `mcp-lok version`: Shows the version of the program
* `mcp-lok`: Starts an MCP server that's used to manage `mcp-lok` servers

The project is implemented as a very thin client for the Docker/Podman unix socket API. This allows it to be very lightweight and fast.

The reason for this project is that the current ways of starting MCP servers are deemed insecure, so this aims to provide a more secure way of starting them
by having locked down containers that only have the permissions they need.

In the future (not now), it will also have a CRI (Container Runtime Interface) implementation that will allow it to run on Kubernetes.

The project is licensed under the Apache 2.0 license.

# Running an MCP server

To run an MCP server, you can use the following command:

```bash
mcp-lok run --transport sse --name my-mcp-server --port 8080 my-mcp-server-image:latest -- my-mcp-server-args
```

The command is very similar to the `docker run` command, but it's a bit more lightweight and secure.

Under the hood, it creates a container that runs the `my-mcp-server-image:latest` image and listens on port `8080`. It also labels the container
in such a way that it can be identified by `mcp-lok`. The labels look like this:

```yaml
mcp-lok: true
mcp-lok-name: my-mcp-server
```

If the transport is `sse`, mcp-lok will also create a reverse proxy that listens on port `8080` and forwards the requests to the container.
This way, the container doesn't need to expose any ports to the outside world. This is the default transport.

If the transport is `stdio`, mcp-lok will create a unix socket that the container can use to communicate with the outside world. This will
be mounted into the container as `/var/run/mcp.sock`. mcp-lok has all the machinery to make this work and it's very secure. Developers only need
to package their server in a container and run it with `mcp-lok`.

# Permissions

The containers that are run by `mcp-lok` are locked down and only have the permissions they need. A custom permission profile is created for each
container that's run by `mcp-lok`. By default, the containers only have access to the `/var/run/mcp.sock` unix socket and nothing else. This is
changed by using the `--permission-profile` flag which takes a path to a permission profile file. The permission profile file is a JSON file that
looks like this:

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
    },
  }
}
```

This permission profile allows the container to read and write to the `/var/run/mcp.sock` unix socket and also allows it to make outbound network
connections to `localhost` and `google.com` on ports `80` and `443`. This is a very secure way of running containers.

There are also two built-in permission profiles that can be used:

* `--permission-profile=stdio`: This allows the container to read and write to the `/var/run/mcp.sock` unix socket and nothing else.
* `--permission-profile=network`: This allows the container to make outbound network connections to any host on any port. This is merely for
  convenience and should not be used in production.

mcp-lok implements these natively which is a main feature of the project.

# Listing running MCP servers

To list running MCP servers, you can use the following command:

```bash
mcp-lok list
```

This will list all the running MCP servers and their status.

# Future work

In the future, we plan to implement a CRI (Container Runtime Interface) implementation that will allow `mcp-lok` to run on Kubernetes. This will
be a big step forward for the project and will make it much more useful.

We also plan to add more subcommands to the `mcp-lok` command to make it more useful. e.g.

* `mcp-lok logs`: Shows the logs of an MCP server
* `mcp-lok exec`: Executes a command in an MCP server
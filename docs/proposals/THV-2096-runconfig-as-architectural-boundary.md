# `RunConfig` as Architectural Boundary

## Problem Statement

As of the time of this writing, ToolHive has a few interfaces and as released as part of three user facing
systems, namely

* **ToolHive CLI** `thv`
* **ToolHive Operator**, which is used in both Kubernetes and OpenShift
* **ToolHive UI**, which internally uses APIs exposed by `thv serve`

These interfaces differ in their execution environment and "quality of life" features that one might implement. For example, one obvious difference is the way configuration is accessed: CLI must both accept CLI options and read files on file system, the Kubernetes Operator requires all parameters to be fully specified in the main CRD or into "linked" ones (e.g. `MCPToolConfig`), and finally HTTP API that is currently used to back ToolHive Studio also requires all parameters to be fully specified in the body of the request.</b>
Another one is in config reload semantics, which must implemented in an ad-hoc fashion for CLI, while Kubernetes handles it as part of the life cycle of resources. Yet another example is the location of runtime configuration and state. The Kubernetes Operator relies on both being stored "inside the cluster", while the CLI and UI must both rely on file system, yet the configuration might be semantically equivalent.</b>
A final useful use case is exporting `RunConfig` so that the same workload can be "moved" to a different place or shipped as configuration. A similar use case is that implemented by the `thv restart` command, which fetches the serialized version of a run config to restart the workload when necessary.

These differences warrant the introduction of an architectural boundary where workloads are executed, which is currently specified via what we call a RunConfig.

## Goals

* make RunConfig the entrypoint for workloads execution
* stabilize RunConfig so that new user or application interfaces can be built upon it
* clearly specify responsibilities of code above and below this new boundary

## Responsibilities of a RunConfig

A `RunConfig` struct contains either information necessary for a OCI-compatible runner to run a workload, or alternatively the remote URL at which an already running MCP is reachable. In both scenarios, ToolHive aims to "wrap" the workload in a proxy to handle auth and gather telemetry.

Finally, `RunConfig` are currently serialized as JSON and used by `thv restart` command.

## Current Interface

Conceptually, a `RunConfig` contains three things
* details on how to run or reach the Workload
* configuration for the Proxy itself
* Metadata like name pertaining both components that allow ToolHive to refer to them as one

**Metadata details** amount to
* name
* group
* schema version
* debug settings, common to both proxy and workload

**Workload details** for local Workloads amount to
* OCI-compatible container config (image, its command arguments, container name, etc...)
* desired workload name
* host and port to expose
* environment variables to set (literal or file-based)
* secrets to set
* volumes to mount
* container labels
* Kubernetes pod template patch (Kubernetes specific)
* network isolation flag

While when the Workload is remote, details are
* remote URL
* auth configuration

**Proxy details** amount to
* host and port to expose
* workload transport type
* permission profile (literal or file-based)
* OIDC configuration parameters
* authorization config (literal or file-based)
* audit config (literal or file-based)
* proxy headers trust flag
* proxy mode (i.e. transport to expose)
* CA bundle
* JWKS token file
* tools config
* IgnoreConfig (?)
* middleware configuration settings

## Current `RunConfig` users

This is the list of users of `RunConfig` users outside of `pkg/runner` package as of commit [6e18a3c](https://github.com/stacklok/toolhive/tree/6e18a3c967d257e8ff715775beea948fe18230f2)

* [cmd/thv-operator/controllers/mcpserver_runconfig.go](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/cmd/thv-operator/controllers/mcpserver_runconfig.go) which translates the CRD into the equivalent run config and accesses several fields for validation
* [cmd/thv-proxyrunner/app/execution.go](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/cmd/thv-proxyrunner/app/execution.go) which is the binary used to run proxy Pods on Kubernetes
* [cmd/thv-proxyrunner/app/run.go](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/cmd/thv-proxyrunner/app/run.go) which loads the JSON representation of a RunConfig from one of three possible standard paths of a Kubernetes Pod
* [cmd/thv/app/run.go](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/cmd/thv/app/run.go) which implements the `thv run --foreground` and uses [cmd/thv/app/run_flags.go](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/cmd/thv/app/run_flags.go) to map CLI flags to run config options
* [pkg/api/v1/workload_service.go](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/pkg/api/v1/workload_service.go) that is used by the HTTP API layer to map a workload creation request into the respective run config
* [pkg/api/v1/workload_types.go](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/pkg/api/v1/workload_types.go) which does the opposite, mapping a run config to a workload creation request returned by the `GET /api/v1beta/workloads/{name}` endpoint
* [pkg/mcp/server/run_server.go](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/pkg/mcp/server/run_server.go) that implements the `thv mcp` command

It's worth mentioning that the RunConfig struct is referenced in [pkg/workloads/manager.go](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/pkg/workloads/manager.go) as well, but that package implements the default workload manager and might be considered part of the runner.

## Responsibility Split

We propose the following split in responsibilities

**RunConfig** data structure and routines will be responsible for holding configuration parameters, basic validation, and serialization, but not storage. Simply put, the package should accept bytes and readers, and return bytes, similarly to how `encoding/json` works.

**CLI** and **Operator** will be responsible for mapping their respective representation of configuration parameters to the representation allowed by `RunConfig`. Specifically, no file-based representation of configuration parameters is allowed in the `RunConfig` struct.

Consequences of changes to configuration parameters must be managed outside the `RunConfig` code. For example, configuration reload for the CLI must be managed within CLI commands, and not within the `Runner` or `RunConfig`. That said, `Runner`s can implement behaviors specific to their execution environment, but they must not rely on references to the "outside world" being stored in the `RunConfig`.

Users of the `RunConfig` package should not build their interfaces using types exposed by `RunConfig` package and should not rely on their stability to guarantee their API contracts. For example, said types should not be used for externally facing interfaces like HTTP API if not for trivial cases.

### Excursus on Middleware Configs and Validation

Middleware configuration is a very important piece of ToolHive since most features are implemented as middleware functions and RunConfig must contain all parameters used to configure middleware in order be able to run and restart an MCP server consistently, but the middleware functions must be composed in the right order, which is logic that hardly belongs to `pkg/runconfig`. In fact, middleware configuration is represented in an opaque way via the [`NewMiddlewareConfig`](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/pkg/transport/types/transport.go#L41-L54) function that accepts a middleware type (string) and an opaque payload representing its configuration.</b>
This function is used by [`PopulateMiddlewareConfigs`](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/pkg/runner/middleware.go#L29-L31) that builds and collects middleware configurations using fields from `RunConfig`.</b>
The function `PopulateMiddlewareConfigs` is effectively responsible for collecting middleware functions in the correct order, which in the implementation is from the outermost to the innermost, but it does not perform any validation on the received parameters outside of null checks. Validation is instead delegated to `MiddlewareFactory` functions implemented for each middleware. All code mentioned is then glued to gether in the implementation of the [Run](https://github.com/stacklok/toolhive/blob/6e18a3c967d257e8ff715775beea948fe18230f2/pkg/runner/runner.go#L92-L127) function of the runner.

This design runs validation of middleware function parameters when the proxy is about to start, which is fine for CLI and UI, but in a Kubernetes environment this happens long after the CRD is created, delaying feedback to the user.

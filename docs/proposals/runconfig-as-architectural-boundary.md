# `RunConfig` as Architectural Boundary

## Problem Statement

As of the time of this writing, ToolHive has a few interfaces and as released as part of three user facing
systems, namely

* **ToolHive CLI** `thv`
* **ToolHive Operator**, which is used in both Kubernetes and OpenShift
* **ToolHive UI**, which internally uses APIs exposed by `thv serve`

These interfaces differ in their execution environment and "quality of life" features that one might implement. For example, one obvious difference is the way configuration is accessed: CLI must both accept CLI options and read files on file system, while the Kubernetes Operator requires all parameters to be fully specified in the main CRD or into "linked" ones (e.g. `MCPToolConfig`). Another one is in config reload semantics, which must implemented in an ad-hoc fashion for CLI, while Kubernetes handles it as part of the life cycle of resources. Yet another example is the location of runtime configuration and state. The Kubernetes Operator relies on both being stored "inside the cluster", while the CLI and UI must both rely on file system, yet the configuration might be semantically equivalent. A final useful use case is exporting `RunConfig` so that the same workload can be "moved" to a different place or shipped as configuration. A similar use case is that implemented by the `thv restart` command, which fetches the serialized version of a run config to restart the workload when necessary.

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

## Responsibility Split

We propose the following split in responsibilities

**RunConfig** data structure and routines will be responsible for holding configuration parameters, basic validation, and serialization, but not storage. Simply put, the package should accept bytes and readers, and return bytes, similarly to how `encoding/json` works.

**CLI** and **Operator** will be responsible for mapping their respective representation of configuration parameters to the representation allowed by `RunConfig`. Specifically, no file-based representation of configuration parameters is allowed in the `RunConfig` struct.

Consequences of changes to configuration parameters must be managed outside the `RunConfig` code. For example, configuration reload for the CLI must be managed within CLI commands, and not within the `Runner` or `RunConfig`. That said, `Runner`s can implement behaviors specific to their execution environment, but they must not rely on references to the "outside world" being stored in the `RunConfig`.

Types exposed by `RunConfig` package should not be used for externally facing formats like HTTP API if not for trivial cases. In case diverging becomes necessary, said types won't be modified (to avoid breaking changes) and CLI/Operator must expose their own type that is then mapped to the `RunConfig` equivalent.

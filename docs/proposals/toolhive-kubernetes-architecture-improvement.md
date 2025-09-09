# Improved Deployment Architecture for ToolHive Inside of Kubernetes.

This document outlines a proposal to improve ToolHive’s deployment architecture within Kubernetes. It provides background on the rationale for the current design, particularly the roles of the ProxyRunner and Operator, and introduces a revised approach intended to increase manageability, maintainability, and overall robustness of the system.

## Current Architecture

Currently ToolHive inside of Kubernetes comprises of 3 major components:

- ToolHive Operator
- ToolHive ProxyRunner
- MCP Server

The high-level resource creation flow is as follows:
```
+-------------------+
| ToolHive Operator |
+-------------------+
        |
     creates
        v
+-----------------------------------+
| ToolHive ProxyRunner Deployment  |
+-----------------------------------+
        |
     creates
        v
+---------------------------+
|   MCP Server StatefulSet  |
+---------------------------+
```

There are additional resources that are created around the edges but those are primarily for networking and RBAC.

At a medium-level, for each `MCPServer` CR, the Operator will create a ToolHive ProxyRunner Deployment and pass it a Kubernetes patch JSON that the `ProxyRunner` would use to create the underlying MCP Server `StatefulSet`.

### Auxiliary Resources

There are currently several supporting networkin resources that are created that enable the flow of traffic to occur within ToolHive and the underlying MCP server.

These networking resources are dependent on the transport type for the underlying MCP server.

#### Stdio MCP Servers

For stdio MCP servers, the Operator will create a single Kubernetes service that aims to provide access to the ToolHive ProxyRunner which then further forwards traffic to the underlying MCP server container via stdin.

The flow of traffic would be like so:

```
+--------------------------+
| ToolHive ProxyRunner SVC |            <--- created by the Operator
+--------------------------+
        |
        v
+-----------------------------------+
| ToolHive ProxyRunner Deployment   |   <--- created by the Operator
+-----------------------------------+
        |
     attaches
        v
+---------------------------+
|   MCP Server Container    |           <--- created by the ProxyRunner
+---------------------------+
```

#### SSE & Streamable HTTP MCP Servers

For SSE or Streamable HTTP MCP servers, the Operator will create a single Kubernetes service that aims to provide access to the ToolHive ProxyRunner, and the ToolHive ProxyRunner will create an additional headless service that provides access to the underlying MCP server via http. The reason why the ProxyRunner created the headless service to the underlying MCP container was because given it is the creator of the underlying MCP server StatefulSet, it's the only thing that knows about the port the server runs and proxies on so that the headless service can expose.

The flow of traffic would be like so:

```
+--------------------------+
| ToolHive ProxyRunner SVC |           <--- created by the Operator
+--------------------------+
        |
        v
+-----------------------------------+
| ToolHive ProxyRunner Deployment   |  <--- created by the Operator
+-----------------------------------+
        |
        v
+---------------------------+
|   Headless Service        |           <--- created by the ProxyRunner
+---------------------------+
        |
        v
+---------------------------+
|   MCP Server Container    |           <--- created by the ProxyRunner
+---------------------------+
```

### Reasoning

The architecture came from two early considerations: scalability and deployment context. At the time, MCP and ToolHive were new, and we knew scaling would eventually matter but didn’t yet know how. ToolHive itself started as a local-only CLI, even though we anticipated running it in Kubernetes later.

The `thv run` command in the CLI was responsible for creating the MCP Server container (via Docker or Podman) and setting up the proxy for communication. So when Kubernetes support arrived, it was a natural fit: since `thv run` was already the component that both created and proxied requests to the MCP Server, it also became the logical creator and proxy of the MCP Server resource inside Kubernetes.

This evolution led to the `Proxy` being renamed to `ProxyRunner` in the Kubernetes context. As complexity grew with `SSE` and `Streamable HTTP`, it became clear that the ProxyRunner also needed to create additional resources, such as headless services, since it was the only component aware of the ephemeral port on which the MCP pod was being proxied.

However, what began as a logical and straightforward implementation gradually became difficult and hacky to work with when complexity increased, for the following reasons:

1) **Split service creation** <br>
The headless service is created by the `ProxyRunner`, while the proxy service is created by the Operator. This means two services are managed in different places, which adds complexity and makes the design harder to reason about.
2) **Orphaned resources** <br>
When an `MCPServer` CR is removed, the Operator correctly deletes the `ProxyRunner` (as its owner) but could not delete the associated `MCPServer` `StatefulSet`, since it was not the creator. This leaves orphaned resources and forced us to implement [finalizer logic](https://github.com/stacklok/toolhive/blob/main/cmd/thv-operator/controllers/mcpserver_controller.go#L820-L846) in the Operator to handle `StatefulSet` and headless service cleanup.
3) **Coupled changes across components** <br>
When the Operator creates the `ProxyRunner` Deployment, it must pass a `--k8s-pod-patch` flag containing the user-provided `podTemplateSpec` from the `MCPServer` resource. The `ProxyRunner` then merges this with the `StatefulSet` it creates. As a result, changes that should live together are split across the `MCPServer` CR, Operator code, and `ProxyRunner` code, increasing maintenance overhead and complexity to testing assurance.
4) **Difficult testing** <br>
Changes to certain resources, such as secrets management for an MCP Server, may require modifications in both the Operator and `ProxyRunner`. There is no reliable way to validate this interaction in isolation, so we depend heavily on end-to-end tests, which are more expensive and less precise than unit tests. 

## New Deployment Architecture Proposal

As described above, the current deployment architecture has it's pains. The aim with the new proposal is to make these pains less painful (hopefully entirely) by moving some of the responsibilities over to other components of ToolHive inside of a Kubernetes context.

The high-level idea is to repurpose the ProxyRunner so that it acts purely as a proxy. By removing the “runner” responsibilities from ProxyRunner, we can leverage the Operator to focus on what it does best: creating and managing Kubernetes resources. This restores clear ownership, idempotency, and drift correction via the reconciliation loop.

```
+-------------------+                        +-----------------------------------+
| ToolHive Operator | ------ creates ------> | ToolHive ProxyRunner Deployment  |
+-------------------+                        +-----------------------------------+
        |                                                       |
     creates                                                    |
        |                                                proxies request (HTTP / stdio)
        v                                                       |
+---------------------------+                                   |
|   MCP Server StatefulSet  | <---------------------------------+
+---------------------------+
```

This new approach would enable us to:

1) **Centralize service creation** – Have the Operator create all services required for both the Proxy and the MCP headless service, avoiding the need for extra finalizer code to clean them up during deletion.
2) **Properly manage StatefulSets** – Allow the Operator to create MCPServer StatefulSets with correct owner references, ensuring clean deletion without custom finalizer logic.
3) **Keep logic close to the CR** – By having the Operator manage the MCPServer StatefulSet directly, changes or additions only require updates in a single component. This removes the need to pass pod patches to ProxyRunner and allows for easier unit testing of the final StatefulSet manifest.
4) **Simplify ProxyRunner** – Reduce ProxyRunner’s responsibilities so it focuses solely on proxying requests.
5) **Clear boundaries** - Keep clear boundaries on responsibilities of ToolHive components.
6) **Minimize RBAC surface area** – With fewer responsibilities, ProxyRunner requires far fewer Kubernetes permissions.

### Auxiliary Resources

In the new architecture, the supporting resources still exist but they are - like everything else - created by the Operator. 

#### Stdio MCP Servers

For stdio MCP servers, the Operator will create a single Kubernetes service that aims to provide access to the ToolHive ProxyRunner which then further forwards traffic to the underlying MCP server container via stdin.

The flow of traffic would be like so:

```
+--------------------------+
| ToolHive ProxyRunner SVC |            <--- created by the Operator
+--------------------------+
        |
        v
+-----------------------------------+
| ToolHive ProxyRunner Deployment   |   <--- created by the Operator
+-----------------------------------+
        |
     attaches
        v
+---------------------------+
|   MCP Server Container    |           <--- created by the Operator
+---------------------------+
```

#### SSE & Streamable HTTP MCP Servers

For SSE or Streamable HTTP MCP servers, the Operator will create a single Kubernetes service that aims to provide access to the ToolHive ProxyRunner, and the ToolHive ProxyRunner will create an additional headless service that provides access to the underlying MCP server via http. The reason why the ProxyRunner created the headless service to the underlying MCP container was because given it is the creator of the underlying MCP server StatefulSet, it's the only thing that knows about the port the server runs and proxies on so that the headless service can expose.

The flow of traffic would be like so:

```
+--------------------------+
| ToolHive ProxyRunner SVC |           <--- created by the Operator
+--------------------------+
        |
        v
+-----------------------------------+
| ToolHive ProxyRunner Deployment   |  <--- created by the Operator
+-----------------------------------+
        |
        v
+---------------------------+
|   Headless Service        |           <--- created by the ProxyRunner
+---------------------------+
        |
        v
+---------------------------+
|   MCP Server Container    |           <--- created by the Operator
+---------------------------+
```

### Scaling Concerns

The original architecture gave ProxyRunner responsibility for both creating and scaling the MCPServer, so it could adjust replicas as needed. Even if ProxyRunner is reduced to a pure proxy, we can still allow it to scale the MCPServer by granting the necessary RBAC permissions to modify replica counts on the StatefulSet—without also giving it the burden of creating and managing those resources.

There are concerns that adopting this architecture too early could create challenges for future solutions around scaling certain MCP servers. However, since the architectural change _**only**_ affects what creates the resources and not how they function, I don’t believe future solutions will be impacted. Right now, the ProxyRunner is responsible for creating the underlying MCP server StatefulSets. I don’t anticipate any scenario where we would need to return creation privileges to the ProxyRunner - adjustments to replica counts may be necessary, but I don’t foresee a solution that would require it to create entirely new StatefulSets to address scalability.


### Technical Implementation

There are multiple fronts which would need implementation changes.

First the Operator will have to create the underlying workloads and services, this should be relatively easy given we already have the code for this in the ProxyRunner, and it also drastically simplifies the underlying run configurations and moves them up a layer, reducing the need to pass them through the components.

The harder more trickier change would be in the ToolHive Proxy (which would have formelly been called ProxyRunner) where we would need to go straight into the proxying layer and skipping all of the run logic that happens before hand.
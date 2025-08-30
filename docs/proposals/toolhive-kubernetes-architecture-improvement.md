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
| ToolHive ProxyRunner Deploypment  |
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

As described above, the current deployment architecture has several pain points. The goal of this proposal is to reduce (ideally eliminate) those issues by shifting certain responsibilities to more appropriate components within ToolHive’s Kubernetes deployment.

The high-level idea is to repurpose the ProxyRunner so that it acts purely as a proxy. By removing the “runner” responsibilities from ProxyRunner, we can leverage the Operator to focus on what it does best: creating and managing Kubernetes resources. This restores clear ownership, idempotency, and drift correction via the reconciliation loop.

```
+-------------------+                        +-----------------------------------+
| ToolHive Operator | ------ creates ------> | ToolHive ProxyRunner Deploypment  |
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



### Scaling Concerns

The original architecture gave ProxyRunner responsibility for both creating and scaling the MCPServer, so it could adjust replicas as needed. Even if ProxyRunner is reduced to a pure proxy, we can still allow it to scale the MCPServer by granting the necessary RBAC permissions to modify replica counts on the StatefulSet—without also giving it the burden of creating and managing those resources.


### Technical Implementation

@Chris to refine here
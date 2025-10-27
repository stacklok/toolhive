# ToolHive Runtime Authoring Guide

This guide defines a stable, implementation-agnostic contract for adding new ToolHive runtimes.

Contents
- Scope and glossary
- Runtime contract (capabilities and API shape)
- Workload lifecycle (deploy, list, info, logs, stop, remove, attach)
- Transports and port exposure
- Network isolation reference design
- Permissions and security mapping
- Secrets handling
- Labeling and discoverability
- Idempotency and reconciliation
- Error handling, logging, and monitoring
- Observability and telemetry
- Testing and conformance
- Security posture hardening guidelines
- Performance and scalability considerations
- Compatibility and portability
- Implementation checklist
- Acceptance criteria

## 1. Scope and glossary

- Runtime: A backend that materializes an MCP server as a managed “workload” on a given platform (e.g., Docker, Kubernetes, future platforms).
- Workload: The process/container/pod that runs the MCP server.
- Auxiliary components: Supporting processes/containers (DNS, egress proxy, ingress proxy) created to implement network isolation and ingress exposure.
- Transport: How ToolHive proxies communicate with the MCP server:
  - stdio (no network exposure)
  - SSE
  - Streamable HTTP
- Permission profile: A JSON-level description of allowed file-system access, process privileges, and network policy for a workload. The CLI resolves profiles and passes an effective configuration to the runtime.
- Isolation: When enabled, ToolHive enforces outbound network ACLs via an egress proxy, restricts DNS via a DNS service, and, for non-stdio transports, exposes ingress only through a controlled proxy.

## 2. Runtime contract

A runtime must implement the following capabilities with consistent semantics:

- Deploy workload
  - Inputs: See `RunConfig` struct in `pkg/runner/config.go` for the complete set of parameters including image reference, workload name, command/args, environment variables, labels, permission profile, transport type, deploy options, and network isolation flag.
  - Output: an integer host port when the transport requires ingress exposure; otherwise 0 (e.g., stdio).
  - Constraints:
    - **Note on current implementation**: As of this writing, `thv run` returns an error if a workload with the same name already exists. The desired behavior described below represents the target state for runtime implementations.
    - Idempotent (target behavior): If the same workload (by name) already exists with the same effective configuration, reuse it and start if stopped.
    - Reconcile differences: If configuration diverges, replace the workload accordingly.
- List workloads
  - Return a list of managed workloads, excluding auxiliary components used for isolation.
  - Include human-readable status string, normalized WorkloadStatus enum, labels, created time, and port mappings.
- Get workload info
  - Return a detailed view for a single workload, including normalized state, labels, created time, and port mappings.
- Get workload logs
  - Return combined stdout/stderr, optionally following.
- Stop workload
  - Idempotent: Success if already stopped or missing.
  - If isolated, attempt to stop auxiliary components (best-effort).
- Remove workload
  - Idempotent: Success if already removed.
  - Remove auxiliary components and internal networks for isolated workloads (best-effort).
- Attach (optional, platform-dependent)
  - Provide an interactive stdio attach for platforms that support it (e.g., Kubernetes exec/attach semantics).

Data model expectations (conceptual, not code):
- ContainerInfo:
  - name: unique workload name
  - image: original image string
  - status: human-readable (e.g., “Up 1m”, “Pending”)
  - state: normalized enum (Running, Starting, Stopped, Removing, Unknown)
  - created: timestamp
  - labels: map[string]string
  - ports: list of {containerPort, hostPort, protocol}
- DeployWorkloadOptions (conceptual):
  - attachStdio: bool (attach stdin/stdout/stderr; typically true for stdio transport, false for HTTP-based transports)
  - exposedPorts: map of “port/proto” -> empty struct (e.g., “8080/tcp”)
  - portBindings: map of “port/proto” -> list of {hostIP, hostPort}
  - platform-specific extension fields (e.g., Kubernetes pod template patch) must be optional and ignored by other runtimes.

## 3. Workload lifecycle

Deploy
- Resolve and validate the effective permission configuration and deploy options.
- Ensure the image is available (pull, or gracefully continue if present locally and pull fails).
- If isolateNetwork=false:
  - Configure filesystem and process security from the permission config.
  - Configure exposed ports and host port bindings if the transport needs ingress.
- If isolateNetwork=true:
  - Build the isolation topology (see Network isolation reference design).
  - Inject proxy environment variables (HTTP_PROXY, HTTPS_PROXY, NO_PROXY) into the workload.
  - For non-stdio transports, publish a host port via an ingress proxy and return the assigned port.
- Apply standard labels (see Labeling and discoverability).
- If attachStdio=true, enable interactive session wiring where platform supports it (does not impact return semantics).
- Return 0 for stdio transport, or the published host port for SSE/Streamable HTTP.

Info
- Provide the same normalization guarantees as List but for a single workload.
- Do not assume the workload is running; report current state.

Logs
- Provide combined stdout/stderr, with follow semantics if requested.
- Never include secrets in logs; redact or avoid printing environment variable values.

Stop
- If the workload is running, request graceful termination with a reasonable timeout.
- If the workload participated in isolation, best-effort stop of auxiliary components.
- If not found, success (idempotency).

Remove
- Remove workload and auxiliary resources; clean up isolation networks when orphaned.
- If not found, success (idempotency).

## 4. Transports and port exposure

- stdio
  - No network exposure.
  - Deploy returns hostPort=0.
  - Communication runs over stdio via the ToolHive proxy process.
- SSE and Streamable HTTP
  - The MCP server exposes an HTTP endpoint.
  - Non-isolated: publish a host port with a deterministic or random binding (respect input mappings).
  - Isolated: front with an ingress HTTP proxy that publishes a host port and reverse-proxies to the internal service.

Port binding policy
- When the caller supplied an explicit host port mapping for a user-facing workload, honor it (except when isolation forces ingress proxy ownership of the host port).
- For automatic/random port assignment, set exactly one host port per deployment for the primary exposed service.

## 5. Network isolation reference design

When isolateNetwork=true, instantiate the following topology:

- Networks
  - “External” network: shared link to host networking.
  - “Internal” per-workload network: private segment named by workload; accessible only to the workload and auxiliary components.
- Components
  - Egress proxy (HTTP/HTTPS)
    - Enforces outbound ACLs from the permission profile.
    - Termination point for all outbound HTTP/HTTPS; other protocols are not guaranteed and should be blocked by default.
    - Inject HTTP(S)_PROXY and NO_PROXY environment variables into the workload.
  - DNS
    - Provide controlled name resolution, ensuring outbound destinations match permitted hosts.
  - Ingress proxy (HTTP)
    - Only for SSE/Streamable HTTP.
    - Publishes a host port on the external network and reverse-proxies to the workload on the internal network.
- Traffic flow
  - Workload → DNS/Egress proxy → External destinations (HTTP/HTTPS).
  - External client → Ingress proxy (host port) → Workload service (internal network).
- Limitations
  - Isolation is defined for HTTP/HTTPS through the egress proxy and domain-based ACLs.
  - If a server must use arbitrary TCP protocols, recommend running without isolation; rely on the platform’s default container isolation.
- Clean-up
  - Stop/remove auxiliary components when stopping/removing the workload.
  - Remove per-workload internal networks when not referenced by other live components.

## 6. Permissions and security mapping

A runtime must map effective permission configuration into platform-native primitives:

- Filesystem
  - Mounts:
    - Bind host paths into the workload with read-only/read-write per profile.
    - Fail fast if requested mounts cannot be honored.
- Process privileges
  - Capabilities:
    - Drop all by default; selectively add minimal required capabilities.
  - Privileged:
    - Strongly discouraged; allow only when explicitly requested by the profile.
  - Security options:
    - Apply platform-appropriate confinement (e.g., seccomp/AppArmor; read-only root filesystem when possible).
  - User:
    - Run as non-root by default; enable configurable user/group when supported.
- Network mode (non-isolated runs)
  - Respect configured network mode as supported by the platform (e.g., bridge/none/host semantics).
- Restart policy
  - Use a safe, non-aggressive default (e.g., restart-on-failure or unless-stopped for long-lived proxies), with platform-specific tuning.

Platform guidance examples
- Kubernetes-style platforms
  - Prefer pod/container security contexts that enforce:
    - Non-root execution
    - No privilege escalation
    - Read-only root filesystem (unless explicitly required)
    - Capability drops (“ALL” by default)
  - For OpenShift-like environments:
    - Allow platform to assign UID/GID/FSGroup when required by security constraints.
    - Set seccomp profile to runtime/default where appropriate.

## 7. Secrets handling

- Secrets are injected as environment variables at deploy time by the CLI and passed through verbatim by the runtime.
- Do not log secret values. Avoid printing full environment vectors.
- When isolation is enabled (isolateNetwork=true), overlay proxy-related environment variables:
  - HTTP_PROXY, HTTPS_PROXY, http_proxy, https_proxy (pointing to the egress proxy)
  - NO_PROXY, no_proxy (including loopback addresses and internal network ranges)
  - Preserve pre-existing keys by overriding only the proxy variables and leaving other keys unchanged.
- Runtimes must treat secrets as opaque; they are not stored by the runtime.

## 8. Labeling and discoverability

Apply consistent labels to all resources:
- toolhive=true on all primary workloads.
- Name labels:
  - Use the workload name (and “app” on orchestrators that prefer it).
- Tool type:
  - Label the main MCP server workload to distinguish it from auxiliary components.
- Auxiliary flag:
  - Mark isolation components (ingress/egress/DNS) as auxiliary so they can be excluded from List.
- Isolation flag:
  - Mark primary workloads that were deployed with isolation; lifecycle operations should use this to decide whether auxiliary clean-up is required.

List/Info behavior:
- Exclude auxiliary components.
- Surface labels to help operators and other ToolHive components reason about inventory.

## 9. Idempotency and reconciliation

Deploy must:
- Determine if a workload with the requested name already exists.
- Compare effective configuration (image, command, env, labels, mount set, privilege set, security options, exposed ports/bindings, and, when isolated, presence of proxy/DNS wiring).
- If equal: start if stopped and return success.
- If different: replace the workload; ensure minimal downtime and consistent labels.

Stop/Remove must:
- Treat missing workloads as success.
- For isolated workloads, stop/remove auxiliary components and remove unused per-workload internal networks.

## 10. Error handling, logging, and monitoring

- Wrap platform errors with context that includes workload name or resource identity.
- Classify “not found” conditions as non-fatal in stop/remove paths.
- Provide clear messages for “exited unexpectedly” including last known logs and reported status.
- Implement a monitor that periodically checks “is running” state and reports an error when the workload disappears or stops unexpectedly, including a short log excerpt.

## 11. Observability and telemetry

- Emit structured logs with clear operation names (deploy, list, info, logs, stop, remove, attach).
- Include correlation identifiers (workload name) and outcome (success/failure with reason).
- Optionally expose metrics for:
  - Deploy durations and outcomes
  - Running workload count
  - Proxy start failures
  - Image pull outcomes
- Avoid logging environment variables or sensitive values.

## 12. Testing and conformance

Unit-test matrix (minimum):
**Note**: The following test requirements represent the target state. Current runtime implementations may not yet meet all these requirements.

- Deploy stdio (isolated and non-isolated) – returns port 0; no ingress proxy.
- Deploy SSE/Streamable HTTP (isolated and non-isolated) – returns published host port.
- Port-binding behaviors:
  - Honor explicit bindings; assign exactly one random host port when requested.
- Isolation topology:
  - Creation of internal network, DNS, egress proxy, ingress proxy (where applicable).
  - Proxy env injection and DNS passing to workload.
- Labeling:
  - Primary workloads labeled; auxiliary flagged and filtered from listings.
- List/Info:
  - State normalization; port mapping extraction; created time handling.
- Stop/Remove:
  - Idempotent when missing.
  - Auxiliary clean-up and network teardown (best-effort).
- Errors:
  - Propagate platform API errors; wrap with context.
- Permissions:
  - Mounts, capabilities, privileged, security options applied as requested.
- Platform-specific extensions (where applicable):
  - Security contexts and platform detection shape.

Conformance guidance:
- Provide a black-box conformance suite that deploys representative MCP servers across transports, toggles isolation, and asserts runtime-invariant behavior (ports, labels, state machine, idempotency).
- Include regression tests for common edge cases (e.g., invalid port mapping keys, bad time formats, non-numeric port parsing).

## 13. Security posture hardening

Defaults
- Run as non-root.
- Read-only root filesystem where possible.
- Drop all capabilities; add only the minimal set required.
- Disallow privilege escalation.
- Disable container device access unless explicitly required.
- Avoid host network, host PID/IPC, or other host-level sharing by default.

Isolation
- Enforce egress policy via HTTP/HTTPS proxy and DNS control.
- Ensure the proxy images are pulled from trusted registries and are version-pinned where feasible.
- Consider name-resolution bypass mitigations (e.g., prevent /etc/hosts injection by workloads if supported by the platform).

Secrets
- Treat all secrets as opaque envs; do not persist, print, or export them.
- Recommend short-lived tokens or centralized providers (e.g., 1Password) for operators.

## 14. Performance and scalability

- Cache/pull optimization:
  - Attempt to pull images; if pull fails but image exists locally, continue.
- Reuse shared external network constructs where possible.
- Create per-workload internal networks only when isolation is enabled.
- Use exponential backoff and timeouts for platform API calls.
- Avoid tight polling in monitors; prefer modest intervals and backoff on errors.

## 15. Compatibility and portability

- Names:
  - Sanitize workload names to meet platform-specific constraints (length, allowed characters).
- Ports:
  - Detect collisions; provide actionable errors or retry randomized host ports when safe.
- OS/Kernel features:
  - Be resilient to missing features (cgroups, seccomp); degrade gracefully and warn.
- Network drivers:
  - Work with common defaults; document requirements for custom drivers.

## 16. Implementation checklist

- Initialization
  - Implement IsAvailable by creating a platform client with a short timeout.
- Deploy
  - Resolve permission configuration and deploy options.
  - Ensure image availability (pull with local fallback).
  - Map permission config to platform mounts, capabilities, privilege, and security options.
  - If isolateNetwork:
    - Create internal per-workload network.
    - Start DNS and egress proxy; inject proxy envs.
    - For non-stdio, start ingress proxy; publish host port and return it.
  - Else:
    - Expose ports directly with host bindings as requested.
  - Apply standard labels (primary workload vs auxiliary; isolation flag).
  - Attach stdio if requested (platform permitting).
- List/Info
  - Exclude auxiliary components; normalize status and ports; include created time and labels.
- Logs
  - Combined stdout/stderr; follow option.
- Stop/Remove
  - Idempotent; best-effort auxiliary/network cleanup.
- Errors
  - Wrap platform errors with workload identity; treat not-found as success on stop/remove.
- Tests
  - Cover success paths, mismatches, isolation, labeling, ports, and error propagation.

## 17. Acceptance criteria

A runtime implementation is considered conformant when the following are satisfied:

- Deploy (stdio)
  - Returns 0 host port; no ingress proxy created; isolation components created only if isolateNetwork=true.
- Deploy (SSE/Streamable HTTP)
  - Non-isolated: host port exposed by binding; connectivity reachable.
  - Isolated: host port exposed via ingress proxy; internal service not directly routable.
- Isolation
  - Outbound HTTP/HTTPS routes only via egress proxy; DNS queries resolved via controlled DNS.
  - Proxy env vars present in the workload; NO_PROXY includes loopback addresses at minimum.
- Permissions
  - Mounts, capabilities, privileged, security options mapped correctly per profile.
- Labels and listing
  - Primary workloads have toolhive=true (and analogous “tool-type” labels); auxiliary components flagged and excluded from List.
- Idempotency
  - Re-deploy with same configuration reuses existing workload (starts if stopped).
  - Re-deploy with different configuration replaces the workload and applies new config.
- Stop/Remove
  - No error on missing workloads; auxiliary and internal networks cleaned up when isolated.
- Errors and logs
  - Errors include workload identity and context; logs retrievable and followable.
- Conformance tests
  - Passes the conformance suite across transports and isolation modes.

---

This document is the source of truth for runtime behavior. New runtimes should use it as a checklist to ensure consistent UX, security posture, and operational characteristics across platforms while allowing platform-specific optimizations and extensions.
## Appendix: MCP_TRANSPORT and MCP_PORT contract (runtime obligations)

Goal
- Ensure every workload receives canonical transport-related environment variables in a way that remains stable across platforms and isolation modes.

Authoritative variables
- MCP_TRANSPORT: One of stdio, sse, streamable-http. This tells the MCP server how to expose itself.
- MCP_PORT: The TCP port inside the workload where the MCP server should bind (only for sse or streamable-http).
- FASTMCP_PORT (optional): Mirror of MCP_PORT for servers that also read FASTMCP_PORT.
- MCP_HOST (optional): The host interface the server should bind to; defaults to 0.0.0.0 when omitted.

Runtime requirements
- Always ensure MCP_TRANSPORT is present in the workload environment and matches the selected transport.
- For sse and streamable-http:
  - Ensure MCP_PORT is present and corresponds to the internal “target” port that the MCP server should bind to within the workload’s network namespace.
  - Optionally set FASTMCP_PORT to the same value as MCP_PORT for compatibility with servers that use it.
  - Optionally set MCP_HOST when the platform requires an explicit bind address (e.g., inside some orchestrators). Default assumed by servers should be 0.0.0.0.
- For stdio:
  - Do not set MCP_PORT; only MCP_TRANSPORT=stdio is required.

Precedence and merge strategy
- If MCP_TRANSPORT and/or MCP_PORT are already present in the caller-provided env, do not override them.
- Only inject defaults when absent.
- When network isolation is enabled and HTTP(S) proxy env vars are injected, overlay only proxy-related variables; avoid mutating MCP_* variables that already exist.

Determining MCP_PORT (sse/streamable-http)
- Single target port:
  - If the deploy options define a single clearly intended container service port (e.g., via exposedPorts), use that port for MCP_PORT.
- Multiple target ports:
  - Select a primary application port deterministically (e.g., the first declared “port/proto” entry in natural order) and document that policy.
- No explicit port provided:
  - Use a runtime-wide default (for example, 8080) that is documented and consistently applied.
  - The default should be overridable by the caller via env or options.
- Important: MCP_PORT represents the in-container binding port for the MCP server. It is not the host/ingress port. The runtime may allocate/publish a host port (directly or through an ingress proxy), but MCP_PORT must remain the workload’s internal port so the process knows where to listen.

Interaction with host/ingress ports
- Non-isolated:
  - The runtime may bind hostPort → containerPort; return the selected host port from Deploy.
  - The workload receives MCP_PORT=containerPort. The caller-facing port (host) is distinct and is not injected as MCP_PORT.
- Isolated:
  - The runtime creates an ingress proxy that publishes hostPort and forwards to the workload’s MCP_PORT on the internal network.
  - Return the published hostPort from Deploy.
  - The workload still receives MCP_PORT=containerPort (internal target port).
  - Do not inject hostPort as MCP_PORT.

MCP_HOST (optional)
- Runtimes should default the server bind host to 0.0.0.0 when not set (or omit MCP_HOST if servers already default correctly).
- If set, MCP_HOST should typically be 0.0.0.0 for containerized environments unless the platform dictates a specific interface.

Examples
- stdio
  - Inject MCP_TRANSPORT=stdio
  - Do not set MCP_PORT
  - Deploy returns 0
- sse (non-isolated)
  - Inject MCP_TRANSPORT=sse, MCP_PORT=8080 (or chosen/declared container target port)
  - Publish a host port binding (random or requested)
  - Deploy returns hostPort (e.g., 18080)
- sse (isolated)
  - Inject MCP_TRANSPORT=sse, MCP_PORT=8080 (or chosen target port)
  - Ingress proxy publishes hostPort (e.g., 18080) and forwards to 8080 inside the internal network
  - Deploy returns hostPort (18080)
- streamable-http
  - Same as sse in terms of MCP_TRANSPORT/MCP_PORT
  - Optionally add FASTMCP_PORT=MCP_PORT and MCP_HOST=0.0.0.0 if the target server expects them

Security and logging
- Treat MCP_* variables as non-secret but avoid dumping complete environment sets in logs.
- Never log user-provided env var values verbatim.

Portability notes
- Do not rely on host networking details inside the workload; MCP_PORT is always the internal port.
- If the higher-level toolchain injects MCP_* already, the runtime must not override them; the runtime’s job is to guarantee presence when absent and to return the published hostPort (when applicable) to the caller.

Cross-cutting consistency
- The Deploy return value for non-stdio transports is the externally reachable host port (direct binding or via ingress proxy).
- The MCP_PORT env value is the internal service port used by the MCP server process.
- This separation allows upper layers to route traffic correctly while keeping server configuration consistent.

Implementation guidance (non-normative)
- Determine target container port from deploy options (exposed ports, pod template extension, or defaults).
- Before container/pod creation, merge env:
  - Respect user vars → overlay MCP_TRANSPORT/MCP_PORT only if missing → overlay proxy envs (when isolated).
- Avoid platform-specific leakage into MCP_PORT semantics (e.g., do not pass NodePort/LoadBalancer ports to the workload).
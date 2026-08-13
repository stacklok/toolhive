# SPIFFE Association Declarations

The embedded authorization server can carry a **configuration-only** SPIFFE association model across the `RunConfig` boundary. This model defines which future SPIFFE-authenticated workload may use a configured OAuth client and its authorization policy. It is intentionally not an authentication mechanism.

## Model and boundaries

Configuration separates trust declarations from grants:

- `spiffe_trust_domains` names a canonical SPIFFE trust domain, explicitly lists permitted future methods (`spiffe_x509` and/or `spiffe_jwt`), and selects one bundle-source declaration.
- `inbound_grants.spiffe_client_auth` associates an exact SPIFFE ID or a terminal `/*` descendant pattern in that declared domain with one explicit OAuth `client_id`, methods, and policy.

A resolved `NormalizedSPIFFEPrincipal` contains the configured OAuth client ID, canonical concrete SPIFFE ID, canonical trust domain, selected method, and immutable authorization policy. It is a policy result, not evidence of identity. The OAuth client ID is configured explicitly; it is never derived from the SPIFFE ID or pattern. Resources, token audiences, scopes, grant types, and token-exchange permission remain distinct policy fields.

An association is valid only when it references a declared domain, its pattern belongs to that domain, and its methods are enabled by that domain. Patterns may be exact or end in `/*`; the wildcard matches descendants at a path boundary only, not its base path or a partial segment. Each pattern and client ID has one owner. Duplicate or overlapping patterns, duplicate client IDs, unknown domains, disabled methods, and invalid or incomplete policies cause startup validation to fail rather than relying on configuration order.

## Bundle-source declarations

Each trust-domain declaration chooses exactly one future bundle source:

- `bundle_endpoint` with an HTTPS Bundle Endpoint URL
- `workload_api` with no endpoint payload

The declaration records intended trust-bundle provenance only. It does not fetch, load, or rotate a bundle, and it does not connect to a Workload API. A Workload API declaration does not deploy SPIRE or mount a Workload API socket.

## Static OAuth client registry

At authorization-server startup, validated associations build an immutable registry and static OAuth-client overlay. The overlay is consulted before dynamic DCR storage and CIMD lookup. Its clients are configuration-only: they are never stored in memory or Redis, cannot be registered or replaced dynamically, and retain only the association's configured policy.

Startup fails closed if persistent storage already contains a configured static client ID; the server does not silently shadow either definition. An unknown `spiffe://` client ID does not trigger CIMD resolution. The overlay is installed before the server accepts traffic.

On every startup, the server reconstructs this static authority from serialized configuration. A restart with the same configuration produces the same associations; a changed or removed association takes effect after restart. Dynamic clients remain subject to their storage backend's persistence, but no stale static client is restored from storage.

## Security and delivery scope

Configuration is not authentication. In particular, a client ID, a declared association, a request header, an unverified SPIFFE-looking URI, or a client-supplied trust domain is never workload identity. Until credential validation is implemented, configured SPIFFE clients remain non-public OAuth clients without a secret and token requests cannot authenticate through these declarations.

Issue #6200 establishes only this normalized configuration, policy, and static-registry boundary. It does **not**:

- load or rotate bundles ([#6201](https://github.com/stacklok/toolhive/issues/6201));
- validate X.509-SVIDs ([#6202](https://github.com/stacklok/toolhive/issues/6202));
- validate JWT-SVIDs ([#6203](https://github.com/stacklok/toolhive/issues/6203));
- issue grants or advertise discovery metadata for SPIFFE methods ([#6204](https://github.com/stacklok/toolhive/issues/6204));
- deploy SPIRE or mount Workload API sockets ([#6205](https://github.com/stacklok/toolhive/issues/6205)); or
- claim full SPIFFE interoperability or end-to-end coverage ([#6206](https://github.com/stacklok/toolhive/issues/6206)).

Future credential-validation code must establish identity from validated SVIDs and then resolve that verified identity through this registry. It must fail closed for missing associations, client-ID ownership mismatches, unknown trust domains, and methods not enabled by policy.

## Related documentation

- [Auth Server Storage Architecture](11-auth-server-storage.md) — dynamic storage and CIMD behavior below the static overlay
- [Kubernetes Operator Architecture](09-operator-architecture.md) — operator-to-runner configuration boundary
- [External Subject-Token Exchange](17-token-exchange-delegation.md) — separate RFC 8693 delegation trust model

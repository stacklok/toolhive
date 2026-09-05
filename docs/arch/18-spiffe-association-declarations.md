# SPIFFE Association Declarations

The embedded authorization server can carry a **configuration-only** SPIFFE association model across the `RunConfig` boundary. This model registers associations and static OAuth clients for workloads that may later authenticate with SPIFFE. It does not currently verify live X.509-SVIDs or JWT-SVIDs, and it does not fetch or load a trust bundle.

## Model and boundaries

Configuration separates top-level trust declarations from canonical client associations:

- `spiffe_trust_domains` in `RunConfig` (`spiffeTrustDomains` in the CRD) names a canonical SPIFFE trust domain, explicitly lists permitted future methods (`spiffe_x509` and/or `spiffe_jwt`), and declares a future trust-bundle source.
- `inbound_grants.spiffe_client_auth` in `RunConfig` (`inboundGrants.spiffeClientAuth` in the CRD) associates an exact SPIFFE ID or a terminal `/*` descendant `principalPattern` with one explicit OAuth `client_id`, methods, resources, audiences, and scopes. This is a sibling of `inbound_grants.token_exchange` and `inbound_grants.jwt_bearer`, not nested under either — client authentication does not by itself confer a grant.

The OAuth client ID is configured explicitly; it is never derived from the SPIFFE ID or pattern. Every SPIFFE client implicitly receives only the RFC 8693 token-exchange grant; the declaration has no configurable `grant_types` field, the runtime always supplies it.

### Resources and audiences are separate dimensions

Each association can configure two independent request dimensions:

- `audiences` are RFC 8693 token audiences. They are not bounded by the server's `allowed_audiences` allowlist and may contain non-URI logical identifiers.
- `resources` are RFC 8707 resource indicators. Each one must be a syntactically valid absolute HTTP(S) URI, and each must also be a member of the server's `allowed_audiences` allowlist (`RunConfig.AllowedAudiences`) — the same allowlist `DelegateClientRunConfig.Audiences` is validated against.

Permission in one dimension never implies permission in the other: a value allowed as a `resource` is not automatically a permitted `audience`, and vice versa. `resources` is optional; `audiences` is required.

`resources` is validated for shape and allowlist membership at startup, but the runtime OAuth client built for a SPIFFE association (`registration.NewSPIFFEClient`) is currently constructed from only `scopes` and `audiences` — `resources` does not yet flow into the client object fosite consults during a token-exchange request. Wiring `resources` into request-time enforcement is a later step.

### Trust-bundle source (declared, not yet used)

Every trust domain must declare exactly one `bundle_source`, a discriminated union naming where a future bundle loader would get the trust bundle from. It is validated for shape only — nothing fetches or loads a bundle from it yet:

- `type: bundle_endpoint` requires an `endpoint` block with:
  - `url`: an absolute HTTPS URL with no userinfo, query string, or fragment; the host must not be an IP literal and must not be a loopback address.
  - `profile`: either `https_web` (the endpoint's TLS connection is authenticated with a Web PKI certificate) or `https_spiffe` (authenticated with an X.509-SVID trusted by a separately distributed root), per the SPIFFE Bundle Endpoint profiles.
- `type: workload_api` selects the local SPIFFE Workload API and carries no payload.

The following canonical operator excerpt shows the supported shape:

```yaml
spec:
  type: embeddedAuthServer
  embeddedAuthServer:
    issuer: https://auth.example.com
    allowedAudiences: [https://mcp.example.com]
    spiffeTrustDomains:
      - name: production
        trustDomain: example.org
        methods: [spiffe_x509, spiffe_jwt]
        bundleSource:
          type: bundle_endpoint
          endpoint:
            url: https://bundle.example.org/spiffe
            profile: https_web
    inboundGrants:
      spiffeClientAuth:
        - trustDomainRef: production
          principalPattern: spiffe://example.org/workloads/reporting/*
          clientId: reporting-workloads
          methods: [spiffe_x509]
          resources: [https://mcp.example.com]
          audiences: [https://mcp.example.com]
          scopes: [openid]
```

An association is valid only when it references a declared domain, its `principalPattern` belongs to that domain, and its methods are enabled by that domain. A principal consisting only of a trust domain, such as `spiffe://example.org`, is invalid because it cannot match an SVID. Patterns may be exact or end in `/*`; the domain-wide `spiffe://example.org/*` wildcard remains valid. A wildcard matches descendants at a path boundary only, not its base path or a partial segment. Each pattern and client ID has one owner. Duplicate or overlapping patterns, duplicate client IDs, unknown domains, disabled methods, an unreferenced trust domain, and invalid or incomplete policies all cause startup validation to fail rather than relying on configuration order.

## Static OAuth client registry

At authorization-server startup, validated associations build an immutable registry and static OAuth-client overlay (`SPIFFEStorageDecorator`), installed as the outermost storage decorator — after CIMD (`decorateStorageForSPIFFE` in `pkg/authserver/server_impl.go`). Its clients are configuration-only: they are held in memory and never written to the storage backend (memory or Redis), cannot be registered or replaced dynamically through `/oauth/register`, and retain only the association's configured policy. `GetClient` checks this static map first and only falls through to the dynamic backend (CIMD, then DCR) when the requested client ID is not one of the configured associations, so a durable client can never shadow a static one. `RegisterClient` rejects any DCR or delegate-client registration attempt that targets a reserved static client ID. An unknown `spiffe://` client ID does not trigger CIMD resolution.

### Startup collision handling

Startup does not simply refuse to start whenever a client with a static ID already exists in durable storage. Each configured static client ID is durably claimed through `ReconcileConfiguredClient` (`preflightDurableCollisions` in `pkg/authserver/storage/spiffe_decorator.go`), which is create-only for anything except a matching restart:

- If no client is stored at that ID, it creates an inert placeholder and starts normally.
- If a placeholder with the same configured association (same scopes, audiences, and origin) already exists — the restart-with-unchanged-configuration case — reconciliation succeeds idempotently.
- If the existing record is DCR-issued, or is a configured client with a different fingerprint (a *different* association reusing the same client ID), reconciliation fails and the server refuses to start.

The durably-claimed record is always an inert placeholder — a client with no grant types and no response types, so it can never itself be issued a token — never the real SPIFFE client with its configured scopes and audiences. This durable claim, not just an in-process `GetClient` check, closes a cross-replica race: with Redis and multiple replicas, an older or still-rolling replica without this SPIFFE config could otherwise DCR-register the same client ID after a newer replica's read-only check passed. Claiming the ID durably makes the reservation visible to every replica immediately.

On every startup, the server reconstructs the static registry and its overlay from serialized configuration. A restart with the same configuration produces the same associations and reconciles cleanly against the previous run's placeholders; a changed or removed association takes effect after restart. Dynamic clients remain subject to the storage backend's own persistence, but no stale static client is restored from storage — the overlay's clients always come from the current configuration, never from a prior run's storage state.

## Security and delivery scope

Configuration is not authentication. In particular, a client ID, a declared association, a request header, an unverified SPIFFE-looking URI, or a client-supplied trust domain is never workload identity. Until credential validation is implemented, configured SPIFFE clients remain non-public OAuth clients without a secret and token requests cannot authenticate through these declarations.

Configured SPIFFE associations establish configuration, policy, and static-client ownership only. They do **not**:

- fetch, load, or rotate a trust bundle, even though a `bundle_source` is declared;
- authenticate workloads with X.509-SVIDs or JWT-SVIDs;
- authenticate token requests or issue tokens through SPIFFE;
- pair users with applications;
- advertise discovery metadata for SPIFFE methods;
- deploy SPIRE or mount Workload API sockets; or
- claim full SPIFFE interoperability or end-to-end coverage.

Future credential-validation code must establish identity from validated SVIDs and then resolve that verified identity through this registry. It must fail closed for missing associations, client-ID ownership mismatches, unknown trust domains, and methods not enabled by policy.

## Related documentation

- [Auth Server Storage Architecture](11-auth-server-storage.md) — dynamic storage and CIMD behavior below the static overlay
- [Kubernetes Operator Architecture](09-operator-architecture.md) — operator-to-runner configuration boundary
- [External Subject-Token Exchange](17-token-exchange-delegation.md) — separate RFC 8693 delegation trust model

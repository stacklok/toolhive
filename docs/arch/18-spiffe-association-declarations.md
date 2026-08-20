# SPIFFE Association Declarations

The embedded authorization server carries a SPIFFE association model across the `RunConfig` boundary. The model defines which SPIFFE-authenticated workload may use a configured OAuth client and its authorization policy. It also declares the trust-bundle source that the bundle registry loads and rotates. A loaded bundle is trust material, not evidence of workload identity.

## Model and boundaries

Configuration separates trust declarations from grants:

- `spiffe_trust_domains` names a canonical SPIFFE trust domain, explicitly lists permitted methods (`spiffe_x509` and/or `spiffe_jwt`), and selects one bundle-source declaration.
- `inbound_grants.spiffe_client_auth` associates an exact SPIFFE ID or a terminal `/*` descendant pattern in that declared domain with one explicit OAuth `client_id`, methods, and policy.

A resolved `NormalizedSPIFFEPrincipal` contains the configured OAuth client ID, canonical concrete SPIFFE ID, canonical trust domain, selected method, and immutable authorization policy. It is a policy result, not evidence of identity. The OAuth client ID is configured explicitly; it is never derived from the SPIFFE ID or pattern. Resources, token audiences, scopes, grant types, and token-exchange permission remain distinct policy fields. `grant_types` is a non-empty set containing `client_credentials`, the RFC 8693 token-exchange grant, or both. `token_exchange.enabled` is required only when the token-exchange grant is selected, and is otherwise absent.

An association is valid only when it references a declared domain, its pattern belongs to that domain, and its methods are enabled by that domain. Every declared trust domain must be referenced by at least one `inbound_grants.spiffe_client_auth` association; otherwise startup validation fails. Patterns may be exact or end in `/*`; the wildcard matches descendants at a path boundary only, not its base path or a partial segment. Each pattern and client ID has one owner. Duplicate or overlapping patterns, duplicate client IDs, unknown domains, disabled methods, and invalid or incomplete policies cause startup validation to fail rather than relying on configuration order.

## Bundle registry lifecycle and rotation

Each trust-domain declaration chooses exactly one bundle source:

- `file` reads a SPIFFE trust-bundle JSON document from a local file. The operator uses it for a same-trust-domain authorization server by projecting one ConfigMap key into a read-only directory; it does not use `subPath`, so kubelet ConfigMap rotation remains visible.
- `bundle_endpoint` specifies an HTTPS SPIFFE Bundle Endpoint URL for federation. Its TLS connection uses the platform WebPKI root store; this scope does not use SPIFFE bundle-derived or custom TLS trust for the endpoint connection.
- `workload_api` connects to the local SPIFFE Workload API and has no endpoint payload. It remains available to generic runtime consumers, but the operator rejects it because it does not deploy a Workload API socket for the authorization server.

The authorization server needs public trust material to validate incoming SVIDs. It does not need its own SVID and must not receive a Workload API socket or private-key access.

`SPIFFEBundleRegistry` is built only from a validated trust configuration and is keyed by parsed trust domains. It implements both `x509bundle.Source` and `jwtbundle.Source`. It retains each domain's explicitly enabled methods. `Start` waits for each domain to have at least one authority for each enabled credential type: an X.509 authority when `spiffe_x509` is enabled and a JWT authority when `spiffe_jwt` is enabled. It does not require authority types that a domain does not enable. Startup has a 10-second readiness limit (or an earlier caller deadline), before it serves lookups. A failed start closes its sources; `Close` stops endpoint and file refresh loops plus Workload API sources.

The registry checks that a requested trust domain is declared **before** it delegates to a backing source. This gate is essential for `workload_api`: a Workload API source can know federated or otherwise available domains that ToolHive did not declare. Delegating first would silently extend trust beyond configuration. Before startup, after shutdown, for an unready domain, or for an undeclared domain, lookups fail rather than returning an empty bundle or consulting another domain.

A bundle endpoint fetches one SPIFFE bundle document for a configured trust domain. The request includes `spiffe_id=spiffe://<configured-trust-domain>`, and the response is parsed in that configured trust-domain context. The bundle format does not provide a response trust-domain identifier that ToolHive independently verifies. That one document yields both the X.509 and JWT bundles; ToolHive does not fetch or rotate those authority types independently.

On a successful endpoint refresh, the complete document atomically replaces the previous document. ToolHive intentionally has no local key-overlap window: a bundle is an authority set, and the trust domain publishes old and new authorities together when overlap is appropriate. Retaining withdrawn local authorities would make removed keys continue to validate. The authority set in the current document therefore defines the valid keys.

Endpoint documents may carry `spiffe_sequence`. A refresh with a lower sequence is rejected and retains the current document. Once the current document has a sequence, an unsequenced document is also a rollback and is rejected. Equal sequences retain the current whole document. Two unsequenced documents may replace each other.

If a refresh fails after an initial successful fetch, the registry continues serving the complete last-known-good document, including both its X.509 and JWT authority material, and retries with exponential backoff. This availability policy is presently unbounded and does not impose a deadline on stale or revoked material. A configurable maximum-staleness policy is required to enforce revocation and rotation deadlines. `// ponytail: TODO` add that policy before deployments that require those deadlines. A refresh failure, malformed document, missing authority for an enabled method, or rejected sequence never clears current material.

A file source synchronously loads its initial SPIFFE trust-bundle JSON document, so startup fails if the file is unreadable, malformed, or lacks an authority required by an enabled method. After startup it polls once per minute rather than watching filesystem events: ConfigMap volume updates rotate symlinks, for which polling is reliable. Every valid reload atomically replaces the complete authority set, so removed authorities stop validating immediately. Failed reads, parses, and method-incomplete updates retain the last-known-good bundle.

SPIRE must publish this document with the SPIRE v1.15 `k8s_configmap` publisher configuration used by the E2E harness:

```hcl
BundlePublisher "k8s_configmap" {
  plugin_data {
    clusters = {
      "kind" = {
        namespace      = "toolhive-system"
        configmap_name = "spire-bundle"
        configmap_key  = "bundle.json"
        format         = "spiffe"
        refresh_hint   = "5m"
      }
    }
  }
}
```

`Notifier "k8sbundle"` writes PEM-encoded X.509 material instead. PEM cannot carry the JWK authorities required for JWT-SVID validation, so it is not a suitable source when JWT authentication is enabled.

## Supported SPIRE deployment topology

The supported production topology separates public verification material from workload credentials:

- SPIRE publishes the SPIFFE JSON trust bundle to a Kubernetes ConfigMap with `format = "spiffe"`.
- The authorization server (AS) uses the `file` bundle source and mounts that ConfigMap as a read-only public file. It does not receive a Workload API socket, an SVID, or private-key access.
- Each attested client workload mounts its local SPIRE Workload API socket and obtains its own SVID through that API. The socket must be available only to the workload that needs its credential.

A public bundle lets the AS verify credentials; it does not confer a SPIFFE identity to the AS or to any pod that can read it. Identity is established only when an attested workload presents a credential that validates against the bundle and is authorized by its configured association.

The cert-manager CSI materials under `deploy/spiffe-poc/` are an optional development-only certificate-file path, not the supported production topology. They mount certificate files and do not provide a SPIFFE Workload API socket. Therefore, they cannot replace `workload_api` or provide the dynamically issued, attested credentials that a SPIRE Workload API client obtains.

### Bundle Endpoint SSRF constraints

Bundle Endpoint URLs must be absolute HTTPS URLs with a valid non-IP-literal, non-loopback authority. Credentials, query strings, and fragments are rejected. The fetch client enforces a 10-second timeout, disables keep-alives so every fetch receives a fresh dial-time check, and rejects private, loopback, and link-local resolved addresses; this applies after DNS resolution and protects against DNS rebinding. Responses are limited to 1 MiB. Redirects must remain HTTPS and use the exact original host and port; the redirect chain is capped at 10 hops. These controls constrain the configured endpoint's network reach and prevent a response from redirecting bundle fetches to a different service.

## Static OAuth client registry

At authorization-server startup, validated associations build an immutable registry and static OAuth-client overlay. The overlay is consulted before dynamic DCR storage and CIMD lookup. Its clients are configuration-only: they are never stored in memory or Redis, cannot be registered or replaced dynamically, and retain only the association's configured policy.

Startup fails closed if persistent storage already contains a configured static client ID; the server does not silently shadow either definition. An unknown `spiffe://` client ID does not trigger CIMD resolution. The overlay is installed before the server accepts traffic.

On every startup, the server reconstructs this static authority from serialized configuration. A restart with the same configuration produces the same associations; a changed or removed association takes effect after restart. Dynamic clients remain subject to their storage backend's persistence, but no stale static client is restored from storage.

## Discovery capabilities and X.509 transport boundary

After the SPIFFE bundle registry has started successfully, the authorization server takes one immutable snapshot of the association registry's enabled X.509-SVID method, JWT-SVID method, and `client_credentials` grant. The same stored `client_credentials` capability controls both registration of the client-credentials provider and discovery metadata. Discovery handlers do not read the association registry. Bundle rotation changes verification authorities but does not change this capability snapshot or discovery metadata; association capability changes take effect on restart.

Both OAuth and OpenID Connect discovery advertise `client_credentials` after the existing grant types when the snapshot permits it, as server-wide capabilities under RFC 8414 §2. They advertise the exact Section 4 values from `draft-ietf-oauth-spiffe-client-auth-02`: `spiffe_x509` for X.509-SVID client authentication and `spiffe_jwt` for JWT-SVID client authentication. These values follow any existing token endpoint authentication methods in X.509-then-JWT order.

The proxy runner wraps its `/oauth/` routes with `spiffeauth.Middleware`, which extracts the claimed SPIFFE ID only from a TLS peer certificate. The listener TLS configuration is a separate deployment boundary and must request client certificates for X.509-SVID authentication to reach the OAuth strategy. The strategy re-verifies the certificate against the configured SPIFFE bundle; the middleware does not validate the certificate chain. External callers embedding `authserver.New` must provide the same TLS and OAuth-route middleware wiring.

## JWT-SVID client authentication

JWT-SVID client authentication is operational for configured associations. The immutable dispatcher selects the SPIFFE JWT arm only when the first `client_assertion_type` form value is the SPIFFE JWT type. If its first value is non-SPIFFE, dispatch falls through to Fosite's default strategy even when a later duplicate value is the SPIFFE JWT type. Only after the SPIFFE JWT arm is selected does it enforce the malformed-field and mixed-credential rules below.

The authorization server accepts a serialized JWT-SVID client assertion, limited to 16 KiB, and validates it with go-spiffe `jwtsvid.ParseAndValidate` against the configured `SPIFFEBundleRegistry`. Validation requires the assertion's sole audience to be the configured authorization-server issuer. This path reaches go-jose/v4's default one-minute claim leeway through go-spiffe v2.7.0; the leeway is inherited and not configurable in this code path.

After validation, the authentication strategy derives the SPIFFE ID context solely to call the shared `SPIFFEAssociationRegistry.Resolve` path synchronously through its JWT resolver. The registry verifies the configured client-ID ownership and enabled JWT method, then binds the configured immutable static OAuth client to the resolved principal for downstream grant handling. The configured OAuth client ID may differ from the SPIFFE ID.

After the SPIFFE JWT arm is selected, malformed request fields use generic OAuth `invalid_request` errors; validation, association, and mixed-credential failures use generic `invalid_client` errors. In that arm, an HTTP Basic authorization header or any `client_secret` form field causes client authentication to fail. Credential material is not logged or included in errors.

JWT-SVID assertions currently have no application-level replay protection, `jti` persistence, nonce, or proof-of-possession binding. A captured valid assertion can therefore be reused until its expiry, including any acceptance allowed by the inherited claim leeway.

## SPIFFE client credentials

An association may permit `client_credentials`, token exchange, or both. A client-credentials request is accepted only after a verified SVID resolves through the shared association registry. Its access-token `sub` is the canonical concrete SPIFFE ID and its `client_id` remains the configured OAuth client ID. The request must select exactly one configured RFC 8707 resource and may request only configured association scopes. Resources and token-exchange audiences remain separate policy fields; an audience-only value cannot authorize a client-credentials resource.

The configured OAuth client ID may differ from the SPIFFE ID under ToolHive's explicit association model. This behavior does not claim literal compliance with draft-ietf-oauth-spiffe-client-auth-02's X.509 `client_id` URI-SAN requirement.

## Security and delivery scope

Configuration and loaded bundles are not authentication by themselves. A client ID, a declared association, a request header, an unverified SPIFFE-looking URI, a client-supplied trust domain, or a loaded bundle is never workload identity. SPIFFE credential validation establishes identity only after the credential validates against configured trust material and the association registry authorizes the resulting SPIFFE ID and configured client ID. A successful authentication binds that exact resolved principal to the static OAuth client; storage lookup alone is never authenticated provenance.

Issue [#6201](https://github.com/stacklok/toolhive/issues/6201) loads and rotates trust bundles. SPIFFE JWT-SVID and X.509-SVID client authentication, including association-constrained `client_credentials` and discovery metadata integration for SPIFFE methods, are implemented by [#6203](https://github.com/stacklok/toolhive/issues/6203), [#6202](https://github.com/stacklok/toolhive/issues/6202), and [#6204](https://github.com/stacklok/toolhive/issues/6204). Issue [#6205](https://github.com/stacklok/toolhive/issues/6205) adds a real SPIRE integration E2E that covers the positive flow through SPIRE-published JSON bundle ConfigMap material, the AS file source, and an attested client obtaining X.509-SVID and JWT-SVID credentials from the Workload API for equivalent `client_credentials` flows. It also covers X.509 and JWT local-authority/SVID rotation without restarting the client pod. Negative-path coverage is not asserted by this E2E.

The authorization-server file source is limited to public trust material. The supported deployment does not mount a Workload API socket into the authorization-server pod or use it to obtain an authorization-server SVID.

## Related documentation

- [Auth Server Storage Architecture](11-auth-server-storage.md) — dynamic storage and CIMD behavior below the static overlay
- [Kubernetes Operator Architecture](09-operator-architecture.md) — operator-to-runner configuration boundary
- [External Subject-Token Exchange](17-token-exchange-delegation.md) — separate RFC 8693 delegation trust model

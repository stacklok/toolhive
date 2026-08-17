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

- `bundle_endpoint` specifies an HTTPS SPIFFE Bundle Endpoint URL. Its TLS connection uses the platform WebPKI root store; this scope does not use SPIFFE bundle-derived or custom TLS trust for the endpoint connection.
- `workload_api` connects to the local SPIFFE Workload API and has no endpoint payload.

`SPIFFEBundleRegistry` is built only from a validated trust configuration and is keyed by parsed trust domains. It implements both `x509bundle.Source` and `jwtbundle.Source`. It retains each domain's explicitly enabled methods. `Start` waits for each domain to have at least one authority for each enabled credential type: an X.509 authority when `spiffe_x509` is enabled and a JWT authority when `spiffe_jwt` is enabled. It does not require authority types that a domain does not enable. Startup has a 10-second readiness limit (or an earlier caller deadline), before it serves lookups. A failed start closes its sources; `Close` stops endpoint refresh loops and Workload API sources.

The registry checks that a requested trust domain is declared **before** it delegates to a backing source. This gate is essential for `workload_api`: a Workload API source can know federated or otherwise available domains that ToolHive did not declare. Delegating first would silently extend trust beyond configuration. Before startup, after shutdown, for an unready domain, or for an undeclared domain, lookups fail rather than returning an empty bundle or consulting another domain.

A bundle endpoint fetches one SPIFFE bundle document for a configured trust domain. The request includes `spiffe_id=spiffe://<configured-trust-domain>`, and the response is parsed in that configured trust-domain context. The bundle format does not provide a response trust-domain identifier that ToolHive independently verifies. That one document yields both the X.509 and JWT bundles; ToolHive does not fetch or rotate those authority types independently.

On a successful endpoint refresh, the complete document atomically replaces the previous document. ToolHive intentionally has no local key-overlap window: a bundle is an authority set, and the trust domain publishes old and new authorities together when overlap is appropriate. Retaining withdrawn local authorities would make removed keys continue to validate. The authority set in the current document therefore defines the valid keys.

Endpoint documents may carry `spiffe_sequence`. A refresh with a lower sequence is rejected and retains the current document. Once the current document has a sequence, an unsequenced document is also a rollback and is rejected. Equal sequences retain the current whole document. Two unsequenced documents may replace each other.

If a refresh fails after an initial successful fetch, the registry continues serving the complete last-known-good document, including both its X.509 and JWT authority material, and retries with exponential backoff. This availability policy is presently unbounded and does not impose a deadline on stale or revoked material. A configurable maximum-staleness policy is required to enforce revocation and rotation deadlines. `// ponytail: TODO` add that policy before deployments that require those deadlines. A refresh failure, malformed document, missing authority for an enabled method, or rejected sequence never clears current material.

### Bundle Endpoint SSRF constraints

Bundle Endpoint URLs must be absolute HTTPS URLs with a valid non-IP-literal, non-loopback authority. Credentials, query strings, and fragments are rejected. The fetch client enforces a 10-second timeout, disables keep-alives so every fetch receives a fresh dial-time check, and rejects private, loopback, and link-local resolved addresses; this applies after DNS resolution and protects against DNS rebinding. Responses are limited to 1 MiB. Redirects must remain HTTPS and use the exact original host and port; the redirect chain is capped at 10 hops. These controls constrain the configured endpoint's network reach and prevent a response from redirecting bundle fetches to a different service.

## Static OAuth client registry

At authorization-server startup, validated associations build an immutable registry and static OAuth-client overlay. The overlay is consulted before dynamic DCR storage and CIMD lookup. Its clients are configuration-only: they are never stored in memory or Redis, cannot be registered or replaced dynamically, and retain only the association's configured policy.

Startup fails closed if persistent storage already contains a configured static client ID; the server does not silently shadow either definition. An unknown `spiffe://` client ID does not trigger CIMD resolution. The overlay is installed before the server accepts traffic.

On every startup, the server reconstructs this static authority from serialized configuration. A restart with the same configuration produces the same associations; a changed or removed association takes effect after restart. Dynamic clients remain subject to their storage backend's persistence, but no stale static client is restored from storage.

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

Issue [#6201](https://github.com/stacklok/toolhive/issues/6201) loads and rotates trust bundles. SPIFFE JWT-SVID and X.509-SVID client authentication, including association-constrained `client_credentials`, are implemented by [#6203](https://github.com/stacklok/toolhive/issues/6203), [#6202](https://github.com/stacklok/toolhive/issues/6202), and [#6204](https://github.com/stacklok/toolhive/issues/6204). The following remain separate and pending:

- discovery metadata integration for SPIFFE methods; and
- deploy SPIRE or mount Workload API sockets ([#6205](https://github.com/stacklok/toolhive/issues/6205)).

For [#6205](https://github.com/stacklok/toolhive/issues/6205), `workloadapi.X509Source` implements both `x509svid.Source` and `x509bundle.Source`, so one Workload API connection can also provide the authorization server's own certificate when deployment wiring is added. The v1alpha1 `ClientCASecretRef` plus `subPath` shape cannot support a rotating bundle and must not be reused for this purpose.

## Related documentation

- [Auth Server Storage Architecture](11-auth-server-storage.md) — dynamic storage and CIMD behavior below the static overlay
- [Kubernetes Operator Architecture](09-operator-architecture.md) — operator-to-runner configuration boundary
- [External Subject-Token Exchange](17-token-exchange-delegation.md) — separate RFC 8693 delegation trust model

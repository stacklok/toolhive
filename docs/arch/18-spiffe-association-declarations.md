# SPIFFE Association Declarations

**Current status: SPIFFE configuration is still rejected at startup.** `RunConfig.Validate()` validates the trust declarations, then `validateSPIFFENotYetEnforced` rejects any non-empty `spiffeTrustDomains` before the runner starts. This branch wires `newServer` to construct the configured bundle source and pass its X.509 and JWT views into the authorization server, and the proxy runner and vMCP can start the auth-server TLS listener. However, X.509-SVID verification is not implemented and SPIFFE client authentication is not deployable yet.

The embedded authorization server carries the SPIFFE association model across the `RunConfig` boundary, and the file-bundle source can load and poll a local trust-bundle file. The source wiring is present in server construction; the startup validation gate still prevents configured associations from being used. This document describes the implemented components and remaining gaps, not an available SPIFFE deployment capability.

## Auth-server TLS listener

The proxy runner and vMCP can serve authorization-server routes on an additional TLS listener at fixed port `8443`. Configure the serving certificate and private key in the embedded auth-server `RunConfig` (for vMCP, the `authserver-config.yaml` file next to the vMCP config); mount both files read-only into the process and keep the private key accessible only to that process:

```yaml
tls_listener:
  cert_file: /etc/toolhive/auth-server/tls.crt
  key_file: /etc/toolhive/auth-server/tls.key
```

The listener binds the same host as the MCP listener (the proxy runner's host or vMCP's `--host`, falling back to loopback when unset). A missing or invalid initial certificate/key pair, a bind failure, or a port-8443 conflict with the MCP port (proxy port or vMCP `--port`) or the enabled diagnostics listener fails startup; the listener does not select another port. Certificate files are checked lazily during TLS handshakes, at most once every 30 seconds. A valid changed pair is used for subsequent handshakes; a failed reload keeps serving the last good pair.

This listener serves OAuth/OIDC authorization-server routes only. Those routes remain mounted on the MCP transport port as well; enabling the listener does not make MCP traffic HTTPS. The listener uses the process's bind host, so binding a non-loopback address exposes it on that network interface: restrict network reachability with deployment firewall/network policy controls and protect the certificate key. SPIFFE trust-domain configuration is still rejected at startup, and X.509-SVID verification is not implemented, so this listener does not make SPIFFE authentication deployable. vMCP starts this listener from `vmcp serve` / `thv vmcp serve`; programs that embed the vMCP server library directly do not. Operator configuration and Service/port or certificate-mount exposure for this listener are not supported.

The token-endpoint middleware rejects certificates containing a malformed SPIFFE identity with HTTP 401 and JSON `{"error":"invalid_client"}`, using `Content-Type: application/json` and `Cache-Control: no-store`. Rejected requests do not reach the authentication strategy; certificates without a SPIFFE URI SAN pass through unchanged. Extracting a claimed SPIFFE ID does not verify the certificate or enable SPIFFE authentication.

## Model and boundaries

Configuration separates top-level trust declarations from canonical client associations:

- `spiffe_trust_domains` in `RunConfig` (`spiffeTrustDomains` in the CRD) names a canonical SPIFFE trust domain, declares the methods intended for authentication, and selects a trust-bundle source.
- `inbound_grants.spiffe_client_auth` in `RunConfig` (`inboundGrants.spiffeClientAuth` in the CRD) associates an exact SPIFFE ID or a terminal `/*` descendant `principalPattern` with one explicit OAuth `client_id`, methods, resources, audiences, and scopes. This is a sibling of `inbound_grants.token_exchange` and `inbound_grants.jwt_bearer`, not nested under either — client authentication does not by itself confer a grant.

The OAuth client ID is configured explicitly; it is never derived from the SPIFFE ID or pattern. Every SPIFFE client implicitly receives only the RFC 8693 token-exchange grant. The standalone `RunConfig` schema (`SPIFFEClientAuthRunConfig.GrantTypes`) does expose a `grant_types` field, but validation restricts it to that single value; the CRD omits it entirely and the operator's conversion always synthesizes it (`cmd/thv-operator/pkg/controllerutil/authserver.go`).

### Resources and audiences are separate dimensions

Each association can configure two independent request dimensions:

- `audiences` are RFC 8693 token audiences. They are not bounded by the server's `allowed_audiences` allowlist and may contain non-URI logical identifiers.
- `resources` are RFC 8707 resource indicators. Each one must be a syntactically valid absolute HTTP(S) URI, and each must also be a member of the server's `allowed_audiences` allowlist (`RunConfig.AllowedAudiences`) — the same allowlist `DelegateClientRunConfig.Audiences` is validated against.

Permission in one dimension never implies permission in the other: a value allowed as a `resource` is not automatically a permitted `audience`, and vice versa. `resources` is optional; `audiences` is required.

`resources` is validated for shape and allowlist membership at startup, and the runtime OAuth client built for a SPIFFE association (`registration.NewSPIFFEClient`) is constructed from `scopes`, `audiences`, and `resources` — `Resources()` and `GetAudience()` are checked independently during a token-exchange request, matching the `audiences`/`resources` dimension split above.

### Trust-bundle source (file loader and server wiring implemented; configuration blocked)

Every trust domain must declare exactly one `bundle_source`, a discriminated union naming the trust-bundle source. `newServer` constructs the configured source and passes its X.509 and JWT bundle views to the authorization server. The runner's startup validation still rejects any non-empty SPIFFE trust configuration, so this source wiring cannot currently be reached through normal startup:

- `type: bundle_endpoint` requires an `endpoint` block with an absolute HTTPS URL (no userinfo, query, fragment, IP-literal, or loopback host) and a `https_web` or `https_spiffe` profile. The declaration is validated, but no endpoint loader is implemented.
- `type: file` requires a `file` block naming a locally mounted SPIFFE JWKS trust-bundle document. The loader reads regular files (including symlinks to regular files) and polls for rotations. On `RunConfig` this is an absolute filesystem path; the CRD instead names a `configMapName`/`configMapKey` pair, since the operator projects that ConfigMap key into a per-domain directory under `/etc/toolhive/authserver/spiffe-bundles/<index>/bundle.json` rather than accepting a raw path. `newServer` wires this source for both X.509 and JWT bundle lookups, but the startup validation gate prevents configured SPIFFE authentication from running.
- `type: workload_api` selects the local SPIFFE Workload API and carries no payload; no Workload API loader is implemented.

The following canonical operator excerpt shows the supported shape. It illustrates the configuration schema only — as noted above, startup validation rejects any non-empty `spiffe_trust_domains`, so this is not yet deployable as-is. `allowedAudiences` is intentionally absent here: it is not a configurable field on `embeddedAuthServer` — it is derived at reconcile time from the resolved incoming OIDC configuration.

```yaml
spec:
  type: embeddedAuthServer
  embeddedAuthServer:
    issuer: https://auth.example.com
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
- If a placeholder with the same configured association (same scopes, audiences, resources, and SPIFFE identity) already exists — the restart-with-unchanged-configuration case — reconciliation succeeds idempotently.
- If the existing record is DCR-issued, or is a configured client with a different fingerprint (a *different* association reusing the same client ID), reconciliation fails and the server refuses to start.

The durably-claimed record is always an inert placeholder — a client with no grant types and no response types, so it can never itself be issued a token — never the real SPIFFE client with its configured scopes and audiences. This durable claim, not just an in-process `GetClient` check, closes a cross-replica race: with Redis and multiple replicas, an older or still-rolling replica without this SPIFFE config could otherwise DCR-register the same client ID after a newer replica's read-only check passed. Claiming the ID durably makes the reservation visible to every replica immediately.

On every startup, the server reconstructs the static registry and its overlay from serialized configuration. A restart with the same configuration produces the same associations and reconciles cleanly against the previous run's placeholders. A **changed** association (a different fingerprint — scopes, audiences, resources, grant types, response types, or SPIFFE identity — at the same client ID) does not take effect: `ReconcileConfiguredClient` fails and the server refuses to start, exactly as described under "Startup collision handling" above. A **removed** association's active policy does take effect on a successful restart — the in-memory overlay is rebuilt from the current configuration, so a client with no matching association is no longer served as a static client. What does *not* clean up is its durable reservation: nothing currently deletes the inert placeholder `ReconcileConfiguredClient` claimed for that client ID, so it persists in storage indefinitely, preventing the ID from being reused by DCR or a delegate client (tracked as [#6477](https://github.com/stacklok/toolhive/issues/6477)). Dynamic clients remain subject to the storage backend's own persistence, but no stale static client is restored from storage — the in-memory overlay's clients always come from the current configuration, never from a prior run's storage state.

## JWT-SVID client authentication

The dispatch and validation logic for JWT-SVID client authentication is implemented for configured associations, though it is not yet reachable end to end (see "Security and delivery scope" below). The immutable dispatcher selects the SPIFFE JWT arm whenever any `client_assertion_type` form value is the SPIFFE JWT type — the shared dispatcher itself is otherwise untouched, so an entirely non-SPIFFE request (including RFC 7523 private-key JWT) still reaches Fosite's default strategy unchanged. Within the SPIFFE JWT arm, a duplicate or otherwise ambiguous `client_assertion_type` is rejected. Absent any SPIFFE-selecting value, the request falls through to Fosite's default strategy unless an ambient SPIFFE X.509 identity is present; that identity selects the fail-closed, not-yet-implemented X.509 arm instead.

The implemented JWT-SVID assertion validator accepts a serialized client assertion, limited to 16 KiB, and validates it with go-spiffe `jwtsvid.ParseAndValidate` against the configured JWT bundle source (`AuthorizationServerParams.SPIFFEJWTBundleSource`). The validator requires the assertion's sole audience to be the configured authorization-server issuer. It does not require an `iss` claim: RFC 7519 makes `iss` OPTIONAL, and the SPIFFE JWT-SVID specification does not mandate it either, so a conformant SVID signed by a bundle-trusted key is accepted whether or not it carries one. This path reaches go-jose/v4's default one-minute claim leeway through go-spiffe v2.7.0; the leeway is inherited and not configurable in this code path. The validator rejects an assertion whose remaining validity exceeds six minutes, representing the recommended five-minute JWT-SVID issuer lifetime plus that one-minute clock-skew allowance. The validator and source wiring are not reachable through normal startup while configured SPIFFE trust domains are rejected by `RunConfig.Validate()`.

`client_id` is optional for this authentication method. When it is omitted, the registry derives the configured OAuth client from the verified SPIFFE ID association. When it is supplied, it is treated as an exact selector and must match that association's configured client ID; the server does not normalize the value. For example:

```console
curl -X POST https://auth.example.com/oauth/token \
  --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
  --data-urlencode "subject_token=$SUBJECT_TOKEN" \
  --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:access_token' \
  --data-urlencode 'client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-spiffe' \
  --data-urlencode "client_assertion=$JWT_SVID"
```

After validation, the authentication strategy calls the shared `SPIFFEAssociationRegistry.Resolve` path synchronously through its JWT resolver. The registry verifies the enabled JWT method and either derives the association's configured client ID or checks the supplied selector's ownership, then returns the configured immutable static OAuth client. The derived identity context is not propagated to downstream request handling.

After the SPIFFE JWT arm is selected, malformed request fields use generic OAuth `invalid_request` errors; validation, association, and mixed-credential failures use generic `invalid_client` errors. In that arm, an HTTP Basic authorization header or any `client_secret` form field causes client authentication to fail. Credential material is not logged or included in errors.

JWT-SVID assertions currently have no application-level replay protection, `jti` persistence, nonce, or proof-of-possession binding. A captured valid assertion can therefore be reused until its expiry, subject to the six-minute maximum remaining-validity policy and any acceptance allowed by the inherited claim leeway.

## Security and delivery scope

Configuration and loaded bundles are not authentication by themselves. A client ID, a declared association, a request header, an unverified SPIFFE-looking URI, a client-supplied trust domain, or a loaded bundle is never workload identity. JWT-SVID validation establishes identity only after the assertion validates against configured trust material and the association registry authorizes the resulting SPIFFE ID and configured client ID.

The JWT-SVID client-authentication path described above is implemented by [#6203](https://github.com/stacklok/toolhive/issues/6203). `newServer` (`pkg/authserver/server_impl.go`) constructs and wires the multi-domain bundle source into both JWT and X.509 verification interfaces. However, `RunConfig.Validate()` rejects every non-empty SPIFFE trust configuration before normal server startup, so neither authentication path is currently deployable. The file loader and its rotation logic are wired into server construction, but are not reachable through that startup path. The auth-server TLS listener (proxy runner and vMCP) is implemented and documented above; X.509-SVID verification ([#6202](https://github.com/stacklok/toolhive/issues/6202)) remains unimplemented. Workload API and bundle-endpoint loaders, SPIFFE method integration with grants or discovery metadata ([#6204](https://github.com/stacklok/toolhive/issues/6204)), and deploying SPIRE or mounting Workload API sockets ([#6205](https://github.com/stacklok/toolhive/issues/6205)) also remain pending.

For [#6205](https://github.com/stacklok/toolhive/issues/6205), `workloadapi.X509Source` implements both `x509svid.Source` and `x509bundle.Source`, so one Workload API connection can also provide the authorization server's own certificate when deployment wiring is added. The v1alpha1 `ClientCASecretRef` plus `subPath` shape cannot support a rotating bundle and must not be reused for this purpose.

Configured SPIFFE associations remain non-deployable: `RunConfig.Validate()` still hard-rejects any non-empty `spiffeTrustDomains` (see "Current status" above), so none of the SPIFFE authentication paths runs against a real deployment yet.

Future X.509-SVID credential-validation code must establish identity from validated SVIDs and then resolve that verified identity through this registry. It must fail closed for missing associations, client-ID ownership mismatches, unknown trust domains, and methods not enabled by policy.

## Related documentation

- [Auth Server Storage Architecture](11-auth-server-storage.md) — dynamic storage and CIMD behavior below the static overlay
- [Kubernetes Operator Architecture](09-operator-architecture.md) — operator-to-runner configuration boundary
- [External Subject-Token Exchange](17-token-exchange-delegation.md) — separate RFC 8693 delegation trust model

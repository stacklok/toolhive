# SPIFFE Association Declarations

The embedded authorization server supports SPIFFE X.509-SVID and JWT-SVID client authentication for configured associations. Each association maps a verified SPIFFE ID or terminal `/*` descendant pattern to an explicit OAuth `client_id` and its token-exchange policy.

## Configuration

`spiffe_trust_domains` declares the canonical trust domain, enabled methods (`spiffe_x509` and/or `spiffe_jwt`), and one bundle source. `inbound_grants.spiffe_client_auth` associates a principal pattern with a client ID, methods, scopes, audiences, optional resources, and the RFC 8693 token-exchange grant. The client ID is explicit; it is never derived from the SPIFFE ID.

An association must reference a declared domain, remain within that domain, and enable only methods allowed by the domain. Duplicate or overlapping patterns, duplicate client IDs, unknown domains, and disabled methods fail startup validation. Runtime bundle lookup also fails closed when a requested trust domain is unknown or the requested authentication method is not enabled for that domain.

`resources` and `audiences` are independent. Resources are absolute HTTP(S) URIs and must be in `allowed_audiences`; audiences are required RFC 8693 audiences and are not limited by that resource allowlist.

- `spiffe_trust_domains` in `RunConfig` (`spiffeTrustDomains` in the CRD) names a canonical SPIFFE trust domain, explicitly lists enabled methods (`spiffe_x509` and/or `spiffe_jwt`), and declares its trust-bundle source.
- `inbound_grants.spiffe_client_auth` in `RunConfig` (`inboundGrants.spiffeClientAuth` in the CRD) associates an exact SPIFFE ID or a terminal `/*` descendant `principalPattern` with one explicit OAuth `client_id`, methods, resources, audiences, and scopes. This is a sibling of `inbound_grants.token_exchange` and `inbound_grants.jwt_bearer`, not nested under either — client authentication does not by itself confer a grant.

A resolved `NormalizedSPIFFEPrincipal` contains the configured OAuth client ID, canonical concrete SPIFFE ID, canonical trust domain, selected method, and immutable authorization policy. It is a policy result, not evidence of identity. The OAuth client ID is configured explicitly; it is never derived from the SPIFFE ID or pattern. Resources, token audiences, scopes, grant types, and token-exchange permission remain distinct policy fields. `grant_types` is a non-empty set containing `client_credentials`, the RFC 8693 token-exchange grant, or both. `token_exchange.enabled` is required only when the token-exchange grant is selected, and is otherwise absent.

### Resources and audiences are separate dimensions

Each association can configure two independent request dimensions:

- `audiences` are RFC 8693 token audiences. They are not bounded by the server's `allowed_audiences` allowlist and may contain non-URI logical identifiers.
- `resources` are RFC 8707 resource indicators. Each one must be a syntactically valid absolute HTTP(S) URI, and each must also be a member of the server's `allowed_audiences` allowlist (`RunConfig.AllowedAudiences`) — the same allowlist `DelegateClientRunConfig.Audiences` is validated against.

Permission in one dimension never implies permission in the other: a value allowed as a `resource` is not automatically a permitted `audience`, and vice versa. `resources` is optional; `audiences` is required.

`resources` is validated for shape and allowlist membership at startup, and the runtime OAuth client built for a SPIFFE association (`registration.NewSPIFFEClient`) is constructed from `scopes`, `audiences`, and `resources` — `Resources()` and `GetAudience()` are checked independently during a token-exchange request, matching the `audiences`/`resources` dimension split above.

Every trust domain declares exactly one `bundle_source`, a discriminated union naming where the live bundle loader (see "Live bundle sources" below) gets the trust bundle from:

- `type: bundle_endpoint` requires an `endpoint` block with:
  - `url`: an absolute HTTPS URL with no userinfo, query string, or fragment; the host must not be an IP literal and must not be a loopback address.
  - `profile`: either `https_web` (the endpoint's TLS connection is authenticated with a Web PKI certificate) or `https_spiffe` (authenticated with an X.509-SVID trusted by a separately distributed root), per the SPIFFE Bundle Endpoint profiles.
- `type: file` requires a `file` block naming a locally mounted SPIFFE JWKS trust-bundle document. On `RunConfig` this is an absolute filesystem path; the CRD instead names a `configMapName`/`configMapKey` pair, since the operator projects that ConfigMap key into a per-domain directory under `/etc/toolhive/authserver/spiffe-bundles/<index>/bundle.json` rather than accepting a raw path.
- `type: workload_api` selects the local SPIFFE Workload API and carries no payload.

The following canonical operator excerpt shows the supported shape. `allowedAudiences` is intentionally absent here: it is not a configurable field on `embeddedAuthServer` — it is derived at reconcile time from the resolved incoming OIDC configuration.

```yaml
spiffe_trust_domains:
  - name: production
    trust_domain: example.org
    methods: [spiffe_x509, spiffe_jwt]
    bundle_source:
      type: bundle_endpoint
      endpoint:
        url: https://bundle.example.org/spiffe
        profile: https_web
inbound_grants:
  spiffe_client_auth:
    - trust_domain_ref: production
      principal_pattern: spiffe://example.org/workloads/reporting/*
      client_id: reporting-workloads
      methods: [spiffe_x509]
      resources: [https://mcp.example.com]
      audiences: [https://mcp.example.com]
      scopes: [openid]
      grant_types: [urn:ietf:params:oauth:grant-type:token-exchange]
```

## Live bundle sources

The server creates and initially loads every configured bundle source before it starts. Initial loading has a 30-second timeout; a missing, invalid, or authority-empty bundle for an enabled method fails server construction rather than leaving that method usable without trust material. The resulting multi-domain source is passed to both X.509-SVID and JWT-SVID verification and is closed with the server.

## Supported SPIRE deployment topology

The supported production topology separates public verification material from workload credentials:

- SPIRE publishes the SPIFFE JSON trust bundle to a Kubernetes ConfigMap with `format = "spiffe"`, using the SPIRE v1.15 `k8s_configmap` `BundlePublisher` plugin configuration exercised by the E2E harness.
- The authorization server (AS) uses the `file` bundle source and mounts that ConfigMap as a read-only public file. It does not receive a Workload API socket, an SVID, or private-key access.
- Each attested client workload mounts its local SPIRE Workload API socket and obtains its own SVID through that API. The socket must be available only to the workload that needs its credential.

A public bundle lets the AS verify credentials; it does not confer a SPIFFE identity to the AS or to any pod that can read it. Identity is established only when an attested workload presents a credential that validates against the bundle and is authorized by its configured association.

The cert-manager CSI materials under `deploy/spiffe-poc/` are an optional development-only certificate-file path, not the supported production topology. They mount certificate files and do not provide a SPIFFE Workload API socket. Therefore, they cannot replace `workload_api` or provide the dynamically issued, attested credentials that a SPIRE Workload API client obtains.

Supported sources are:

- `workload_api`, which uses the local SPIFFE Workload API's live bundle watch.
- `bundle_endpoint` with the `https_web` profile. The server initially fetches the bundle over Web-PKI-authenticated HTTPS, then refreshes it using the bundle refresh hint (at least 30 seconds and no more than 24 hours), or every five minutes when no hint is supplied. A failed refresh is retried after one minute and retains the last known good bundle for at most 24 hours; after that, authentication fails closed until a valid refresh succeeds. Initial bundles may omit `spiffe_sequence` for compatibility. Once a response supplies it, later responses must also supply a nondecreasing sequence. A lower or missing sequence, or changed content at an equal sequence, is rejected as a refresh failure and cannot extend the last-known-good age; an equal sequence with identical content is accepted.

Bundle endpoint URLs must be absolute HTTPS URLs without userinfo, query, or fragment. Their hosts cannot be IP literals or loopback addresses; redirects are restricted to the same host. The endpoint response is limited to 1 MiB.

- `file`, which loads a local SPIFFE JWKS trust-bundle document (typically a mounted ConfigMap or Secret) and reloads it every 30 seconds. A reload failure is logged and the last known good bundle is retained; polling, not `fsnotify`, is used because a ConfigMap-mounted file is updated via a kubelet symlink swap that inotify on the mounted path frequently misses.

`https_spiffe` is not supported. It requires future endpoint identity and bootstrap-trust configuration to authenticate the endpoint with an X.509-SVID.

## Authentication behavior

X.509-SVID authentication re-verifies the TLS peer certificate against the selected X.509 bundle before resolving the verified SPIFFE ID and requested client ID through the association registry. JWT-SVID authentication validates the assertion against the selected JWT bundle and requires its sole audience to be the authorization-server issuer. Both paths reject malformed or mixed credentials, unconfigured identities, client-ID ownership mismatches, unknown domains, unavailable bundles, and methods disallowed by policy.

## JWT-SVID client authentication

The dispatch and validation logic for JWT-SVID client authentication is implemented for configured associations, though it is not yet reachable end to end (see "Security and delivery scope" below). The immutable dispatcher selects the SPIFFE JWT arm whenever any `client_assertion_type` form value is the SPIFFE JWT type — the shared dispatcher itself is otherwise untouched, so an entirely non-SPIFFE request (including RFC 7523 private-key JWT) still reaches Fosite's default strategy unchanged. Within the SPIFFE JWT arm, a duplicate or otherwise ambiguous `client_assertion_type` is rejected. Absent any SPIFFE-selecting value, the request falls through to Fosite's default strategy unless an ambient SPIFFE X.509 identity is present; that identity selects the fail-closed, not-yet-implemented X.509 arm instead.

The authorization server accepts a serialized JWT-SVID client assertion, limited to 16 KiB, and validates it with go-spiffe `jwtsvid.ParseAndValidate` against the configured JWT bundle source (`AuthorizationServerParams.SPIFFEJWTBundleSource`). Validation requires the assertion's sole audience to be the configured authorization-server issuer. It does not require an `iss` claim: RFC 7519 makes `iss` OPTIONAL, and the SPIFFE JWT-SVID specification does not mandate it either, so a conformant SVID signed by a bundle-trusted key is accepted whether or not it carries one. This path reaches go-jose/v4's default one-minute claim leeway through go-spiffe v2.7.0; the leeway is inherited and not configurable in this code path. The server also rejects an assertion whose remaining validity exceeds six minutes, representing the recommended five-minute JWT-SVID issuer lifetime plus that one-minute clock-skew allowance.

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

The JWT-SVID client-authentication path described above is implemented by [#6203](https://github.com/stacklok/toolhive/issues/6203), but `newServer` (`pkg/authserver/server_impl.go`) does not yet construct and wire in the JWT bundle source that path validates assertions against, so `jwtsvid.ParseAndValidate` is unreachable with real trust material today and every JWT-SVID authentication attempt fails closed. Issue [#6201](https://github.com/stacklok/toolhive/issues/6201) loads and rotates trust bundles and will supply that source. The following remain separate and pending:

- validate X.509-SVIDs ([#6202](https://github.com/stacklok/toolhive/issues/6202));
- integrate SPIFFE methods with grants or discovery metadata ([#6204](https://github.com/stacklok/toolhive/issues/6204)); and
- deploy SPIRE or mount Workload API sockets ([#6205](https://github.com/stacklok/toolhive/issues/6205)).

For [#6205](https://github.com/stacklok/toolhive/issues/6205), `workloadapi.X509Source` implements both `x509svid.Source` and `x509bundle.Source`, so one Workload API connection can also provide the authorization server's own certificate when deployment wiring is added. The v1alpha1 `ClientCASecretRef` plus `subPath` shape cannot support a rotating bundle and must not be reused for this purpose.

Configured SPIFFE associations remain non-deployable independent of the above: `RunConfig.Validate()` still hard-rejects any non-empty `spiffeTrustDomains` (see "Current status" above), so none of this runs against a real deployment yet.

Future X.509-SVID credential-validation code must establish identity from validated SVIDs and then resolve that verified identity through this registry. It must fail closed for missing associations, client-ID ownership mismatches, unknown trust domains, and methods not enabled by policy.

## Discovery capabilities and X.509 transport boundary

After the SPIFFE bundle registry has started successfully, the authorization server takes one immutable snapshot of the association registry's enabled X.509-SVID method, JWT-SVID method, and `client_credentials` grant. The same stored `client_credentials` capability controls both registration of the client-credentials provider and discovery metadata. Discovery handlers do not read the association registry. Bundle rotation changes verification authorities but does not change this capability snapshot or discovery metadata; association capability changes take effect on restart.

Both OAuth and OpenID Connect discovery advertise `client_credentials` after the existing grant types when the snapshot permits it, as server-wide capabilities under RFC 8414 §2. They advertise the exact Section 4 values from `draft-ietf-oauth-spiffe-client-auth-02`: `spiffe_x509` for X.509-SVID client authentication and `spiffe_jwt` for JWT-SVID client authentication. These values follow any existing token endpoint authentication methods in X.509-then-JWT order.

The proxy runner wraps its `/oauth/` routes with `spiffeauth.Middleware`, which extracts the claimed SPIFFE ID only from a TLS peer certificate. The listener TLS configuration is a separate deployment boundary and must request client certificates for X.509-SVID authentication to reach the OAuth strategy. The strategy re-verifies the certificate against the configured SPIFFE bundle; the middleware does not validate the certificate chain. External callers embedding `authserver.New` must provide the same TLS and OAuth-route middleware wiring.

## JWT-SVID client authentication

The immutable dispatcher selects the SPIFFE JWT arm only when the first `client_assertion_type` form value is the SPIFFE JWT type. If its first value is non-SPIFFE, dispatch falls through to Fosite's default strategy even when a later duplicate value is the SPIFFE JWT type. Only after the SPIFFE JWT arm is selected does it enforce the malformed-field and mixed-credential rules below.

The authorization server accepts a serialized JWT-SVID client assertion, limited to 16 KiB, and validates it with go-spiffe `jwtsvid.ParseAndValidate` against the configured JWT bundle source (`AuthorizationServerParams.SPIFFEJWTBundleSource`). Validation requires the assertion's sole audience to be the configured authorization-server issuer. This path reaches go-jose/v4's default one-minute claim leeway through go-spiffe v2.7.0; the leeway is inherited and not configurable in this code path.

After validation, the authentication strategy derives the SPIFFE ID context solely to call the shared `SPIFFEAssociationRegistry.Resolve` path synchronously through its JWT resolver. The registry verifies the configured client-ID ownership and enabled JWT method, then binds the configured immutable static OAuth client to the resolved principal for downstream grant handling. The configured OAuth client ID may differ from the SPIFFE ID.

After the SPIFFE JWT arm is selected, malformed request fields use generic OAuth `invalid_request` errors; validation, association, and mixed-credential failures use generic `invalid_client` errors. In that arm, an HTTP Basic authorization header or any `client_secret` form field causes client authentication to fail. Credential material is not logged or included in errors.

JWT-SVID assertions currently have no application-level replay protection, `jti` persistence, nonce, or proof-of-possession binding. A captured valid assertion can therefore be reused until its expiry, including any acceptance allowed by the inherited claim leeway.

## SPIFFE client credentials

An association may permit `client_credentials`, token exchange, or both. A client-credentials request is accepted only after a verified SVID resolves through the shared association registry. Its access-token `sub` is the canonical concrete SPIFFE ID and its `client_id` remains the configured OAuth client ID. The request must select exactly one configured RFC 8707 resource and may request only configured association scopes. Resources and token-exchange audiences remain separate policy fields; an audience-only value cannot authorize a client-credentials resource.

The configured OAuth client ID may differ from the SPIFFE ID under ToolHive's explicit association model. This behavior does not claim literal compliance with draft-ietf-oauth-spiffe-client-auth-02's X.509 `client_id` URI-SAN requirement.

## Security and delivery scope

Configuration and loaded bundles are not authentication by themselves. A client ID, a declared association, a request header, an unverified SPIFFE-looking URI, a client-supplied trust domain, or a loaded bundle is never workload identity. SPIFFE credential validation establishes identity only after the credential validates against configured trust material and the association registry authorizes the resulting SPIFFE ID and configured client ID. A successful authentication binds that exact resolved principal to the static OAuth client; storage lookup alone is never authenticated provenance.

The JWT-SVID client-authentication path described above is implemented by [#6203](https://github.com/stacklok/toolhive/issues/6203), association-constrained `client_credentials` alongside token exchange by [#6204](https://github.com/stacklok/toolhive/issues/6204) for both the JWT and X.509 arms ([#6202](https://github.com/stacklok/toolhive/issues/6202)), and live trust-bundle loading and rotation by [#6201](https://github.com/stacklok/toolhive/issues/6201), including discovery-metadata advertisement of `spiffe_x509`, `spiffe_jwt`, and `client_credentials` once the underlying association snapshot permits them. `newServer` (`pkg/authserver/server_impl.go`) constructs and wires the JWT and X.509 bundle sources these paths validate credentials against, so `jwtsvid.ParseAndValidate` and X.509 chain verification are reachable with real trust material.

Issue [#6205](https://github.com/stacklok/toolhive/issues/6205) adds a real SPIRE integration E2E that covers the positive flow through SPIRE-published JSON bundle ConfigMap material, the AS file source, and an attested client obtaining X.509-SVID and JWT-SVID credentials from the Workload API for equivalent `client_credentials` flows. It also covers X.509 and JWT local-authority/SVID rotation without restarting the client pod. Negative-path coverage is not asserted by this E2E.

For [#6205](https://github.com/stacklok/toolhive/issues/6205), `workloadapi.X509Source` implements both `x509svid.Source` and `x509bundle.Source`, so one Workload API connection can also provide the authorization server's own certificate when deployment wiring is added. The v1alpha1 `ClientCASecretRef` plus `subPath` shape cannot support a rotating bundle and must not be reused for this purpose.

The authorization-server file source is limited to public trust material. The supported deployment does not mount a Workload API socket into the authorization-server pod or use it to obtain an authorization-server SVID.

## Related documentation

- [Auth Server Storage Architecture](11-auth-server-storage.md) — dynamic storage and the static client overlay
- [Kubernetes Operator Architecture](09-operator-architecture.md) — operator-to-runner configuration boundary
- [External Subject-Token Exchange](17-token-exchange-delegation.md) — separate RFC 8693 delegation trust model

# SPIFFE Association Declarations

The embedded authorization server supports SPIFFE X.509-SVID and JWT-SVID client authentication for configured associations. Each association maps a verified SPIFFE ID or terminal `/*` descendant pattern to an explicit OAuth `client_id` and its token-exchange policy.

## Configuration

`spiffe_trust_domains` declares the canonical trust domain, enabled methods (`spiffe_x509` and/or `spiffe_jwt`), and one bundle source. `inbound_grants.spiffe_client_auth` associates a principal pattern with a client ID, methods, scopes, audiences, optional resources, and the RFC 8693 token-exchange grant. The client ID is explicit; it is never derived from the SPIFFE ID.

An association must reference a declared domain, remain within that domain, and enable only methods allowed by the domain. Duplicate or overlapping patterns, duplicate client IDs, unknown domains, and disabled methods fail startup validation. Runtime bundle lookup also fails closed when a requested trust domain is unknown or the requested authentication method is not enabled for that domain.

`resources` and `audiences` are independent. Resources are absolute HTTP(S) URIs and must be in `allowed_audiences`; audiences are required RFC 8693 audiences and are not limited by that resource allowlist.

- `spiffe_trust_domains` in `RunConfig` (`spiffeTrustDomains` in the CRD) names a canonical SPIFFE trust domain, explicitly lists enabled methods (`spiffe_x509` and/or `spiffe_jwt`), and declares its trust-bundle source.
- `inbound_grants.spiffe_client_auth` in `RunConfig` (`inboundGrants.spiffeClientAuth` in the CRD) associates an exact SPIFFE ID or a terminal `/*` descendant `principalPattern` with one explicit OAuth `client_id`, methods, resources, audiences, and scopes. This is a sibling of `inbound_grants.token_exchange` and `inbound_grants.jwt_bearer`, not nested under either — client authentication does not by itself confer a grant.

An embedded auth server whose only clients are SPIFFE workloads needs no upstream identity provider. Those workloads authenticate with their SVIDs and get tokens through `client_credentials` or token exchange, so there is no interactive login to redirect. Both `validateZeroUpstreamMode` (`pkg/authserver/config.go`) and `validateAuthServerRunConfig` (`pkg/vmcp/config/validator.go`) accept a configuration with no upstreams when at least one SPIFFE client association, delegate client, or trusted issuer with the JWT bearer grant is configured.

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

- `file`, which loads a local SPIFFE JWKS trust-bundle document (typically a mounted ConfigMap or Secret) and reloads it every 30 seconds. A reload failure is logged and the last known good bundle is retained; polling, not `fsnotify`, is used because a ConfigMap-mounted file is updated via a kubelet symlink swap that inotify on the mounted path frequently misses. This is the only source with a real deployment story: it matches the topology above, is exercised by the E2E harness, and is the only one the operator can currently deploy.

`workload_api` and `bundle_endpoint` are declared in the type (`SPIFFEBundleSourceType`) and validated for shape, but neither has a live bundle loader in this build, and neither is part of the supported surface:

- `workload_api` would give the AS pod itself a Workload API socket to watch bundles live. That directly contradicts the topology above: the whole point of the `file` + `BundlePublisher` design is that the AS holds no Workload API socket, no SVID, and no private key, so a compromised AS pod cannot itself impersonate a workload. Nothing in this repo names a deployment where `BundlePublisher` (SPIRE's standard, first-class answer for exactly this relying-party shape) is unavailable and `workload_api` would be the only option. Until such a scenario is named — and the docs and operator wiring updated to support it deliberately — this source type should not be built out further.
- `bundle_endpoint` (federation with a different trust domain, `https_web` profile) is a reasonable idea for a scenario nobody has asked for yet: nothing here names a partner-org or cross-cluster federation use case. It is sound in isolation and cheap to keep unit-tested; it just is not worth live infrastructure (a second trust domain in the E2E harness) until a real use case shows up.

`https_spiffe` is not supported. It requires future endpoint identity and bootstrap-trust configuration to authenticate the endpoint with an X.509-SVID.

## Authentication behavior

The client-authentication strategy (`newSPIFFEClientAuthenticationStrategy` in `pkg/authserver/server/spiffe_client_auth.go`) wraps Fosite's default strategy and picks one path per token request:

1. An ambient X.509 identity (a SPIFFE ID the connection middleware read from the TLS peer certificate) combined with any `client_assertion_type` value equal to the SPIFFE JWT type is rejected with `invalid_client`. This check runs even when no SPIFFE associations are configured.
2. When no SPIFFE associations are configured, the server has no SPIFFE client resolver and every other request goes to Fosite's default strategy unchanged.
3. If any `client_assertion_type` value is the SPIFFE JWT type, the SPIFFE JWT arm runs. Every value of the repeated form key is checked, so a SPIFFE assertion type cannot hide behind an earlier non-SPIFFE value. A duplicated `client_assertion_type` is then rejected inside the JWT arm as ambiguous.
4. Otherwise, if the request carries an ambient X.509 identity, the SPIFFE X.509 arm runs.
5. Otherwise, the request goes to Fosite's default strategy, so non-SPIFFE methods such as RFC 7523 private-key JWT keep working.

X.509-SVID authentication (`authenticateSPIFFEX509Client`) requires exactly one non-empty `client_id` and rejects any request that also carries HTTP Basic credentials, `client_secret`, `client_assertion` or `client_assertion_type`. It then re-verifies the TLS peer certificate chain with go-spiffe `x509svid.Verify` against the bundle for the leaf's trust domain, rather than trusting the identity the middleware extracted. The leaf must also meet ToolHive's stricter profile: a critical key-usage extension with digital signature and without certificate or CRL signing, a basic-constraints extension marking it as a non-CA, a subject-alternative-name extension (critical when the subject is empty), a non-root canonical SPIFFE ID path, and, if an extended-key-usage extension is present, both server and client authentication. The re-verified SPIFFE ID must equal the claimed one, and the association registry must resolve it to the requested client ID.

JWT-SVID authentication validates the assertion against the selected JWT bundle and requires its sole audience to be the authorization-server issuer (details below). Both paths reject malformed or mixed credentials, unconfigured identities, client-ID ownership mismatches, unknown domains, unavailable bundles, and methods disallowed by policy.

## JWT-SVID client authentication

The authorization server accepts a serialized JWT-SVID client assertion, limited to 16 KiB, and validates it with go-spiffe `jwtsvid.ParseAndValidate` against the configured JWT bundle source (`AuthorizationServerParams.SPIFFEJWTBundleSource`). Validation requires the assertion's sole audience to be the configured authorization-server issuer. It does not require an `iss` claim: RFC 7519 makes `iss` OPTIONAL, and the SPIFFE JWT-SVID specification does not mandate it either, so a conformant SVID signed by a bundle-trusted key is accepted whether or not it carries one. This path reaches go-jose/v4's default one-minute claim leeway through go-spiffe v2.7.0; the leeway is inherited and not configurable in this code path.

The server rejects an assertion whose remaining validity exceeds six minutes (`maxSPIFFEJWTAssertionRemainingValidity`): the recommended five-minute JWT-SVID lifetime plus the one-minute clock-skew allowance. The SPIFFE issuer must therefore mint JWT-SVIDs that live about five minutes or less. With SPIRE, set `default_jwt_svid_ttl` on the server (the SPIRE hardened Helm chart exposes it as `spire-server.defaultJwtSvidTTL`) or request a short TTL per SVID. The hardened chart defaults to one hour, and the authorization server rejects every JWT-SVID issued with that lifetime. The SPIRE E2E harness uses `default_jwt_svid_ttl = "5m"` for this reason.

`client_id` is optional for this authentication method. When it is omitted, the registry derives the configured OAuth client from the verified SPIFFE ID association. When it is supplied, it is treated as an exact selector and must match that association's configured client ID; the server does not normalize the value. For example:

```console
curl -X POST https://auth.example.com/oauth/token \
  --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
  --data-urlencode "subject_token=$SUBJECT_TOKEN" \
  --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:access_token' \
  --data-urlencode 'client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-spiffe' \
  --data-urlencode "client_assertion=$JWT_SVID"
```

After validation, the authentication strategy calls the shared `SPIFFEAssociationRegistry.Resolve` path synchronously through its JWT resolver. The registry verifies the enabled JWT method and either derives the association's configured client ID or checks the supplied selector's ownership. The resolver then binds the configured immutable static OAuth client to the resolved principal (`registration.NewAuthenticatedSPIFFEClient`) for downstream grant handling. The configured OAuth client ID may differ from the SPIFFE ID.

After the SPIFFE JWT arm is selected, malformed request fields use generic OAuth `invalid_request` errors; validation, association, and mixed-credential failures use generic `invalid_client` errors. In that arm, an HTTP Basic authorization header or any `client_secret` form field causes client authentication to fail. Credential material is not logged or included in errors.

JWT-SVID assertions have no application-level replay protection, `jti` persistence, nonce, or proof-of-possession binding. A captured valid assertion can therefore be reused until its expiry, subject to the six-minute maximum remaining-validity policy and any acceptance allowed by the inherited claim leeway.

## Diagnosing client authentication failures

A failed SPIFFE client authentication returns a generic `invalid_client` (or `invalid_request` for malformed fields) to the client. This is deliberate: a detailed error would tell an attacker which part of a credential the server rejected.

The reason is logged at debug level instead. Enable debug logging with `--debug` or `TOOLHIVE_DEBUG=true` on the proxy runner, or `--debug` on vMCP. The relevant messages start with:

- `SPIFFE client auth: ...` — which path the dispatcher chose (JWT, X.509 or the default strategy) and mixed-credential rejections, from `pkg/authserver/server/spiffe_client_auth.go`.
- `SPIFFE X.509: ...` — from `pkg/authserver/spiffe/middleware.go` (whether the request was over TLS, carried a client certificate, and had a valid SPIFFE URI SAN) and from `pkg/authserver/server/spiffe_client_auth.go` (missing `client_id`, extra credentials, chain verification failure, claimed and verified ID mismatch, no matching association).
- `SPIFFE JWT: ...` — malformed form fields, failed validation, wrong audience, a lifetime longer than six minutes, or no matching association, from `pkg/authserver/server/spiffe_client_auth.go`.
- `auth server TLS listener ...` — whether the TLS listener asked for a client certificate and what the connection presented, from `pkg/authserver/runner/tls_listener.go`.

These messages log identity fields only, such as the SPIFFE ID, client ID, expected audience, certificate URI SANs, issuer, chain length and expiry. Certificates, assertions and tokens are never logged.

## Discovery capabilities and X.509 transport boundary

After the SPIFFE bundle registry has started successfully, the authorization server takes one immutable snapshot of the association registry's enabled X.509-SVID method, JWT-SVID method, and `client_credentials` grant. The same stored `client_credentials` capability controls both registration of the client-credentials provider and discovery metadata. Discovery handlers do not read the association registry. Bundle rotation changes verification authorities but does not change this capability snapshot or discovery metadata; association capability changes take effect on restart.

Both OAuth and OpenID Connect discovery advertise `client_credentials` after the existing grant types when the snapshot permits it, as server-wide capabilities under RFC 8414 §2. They advertise the exact Section 4 values from `draft-ietf-oauth-spiffe-client-auth-02`: `spiffe_x509` for X.509-SVID client authentication and `spiffe_jwt` for JWT-SVID client authentication. These values follow any existing token endpoint authentication methods in X.509-then-JWT order.

`spiffeauth.Middleware` extracts the claimed SPIFFE ID only from a TLS peer certificate; it does not validate the certificate chain. The OAuth strategy re-verifies the certificate against the configured SPIFFE bundle. The proxy runner and vMCP receive client certificates through the embedded auth server's dedicated TLS listener on port 8443, which requests them only when `spiffe_x509` is configured and leaves MCP traffic on plain HTTP; see [Auth Server TLS Listener](19-auth-server-tls-listener.md). Callers that use `authserver.New` directly, rather than `EmbeddedAuthServer`, must provide their own TLS and middleware wiring.

## SPIFFE client credentials

An association may permit `client_credentials`, token exchange, or both. A client-credentials request is accepted only after a verified SVID resolves through the shared association registry. Its access-token `sub` is the canonical concrete SPIFFE ID and its `client_id` remains the configured OAuth client ID. The request must select exactly one configured RFC 8707 resource and may request only configured association scopes. Resources and token-exchange audiences remain separate policy fields; an audience-only value cannot authorize a client-credentials resource.

The configured OAuth client ID may differ from the SPIFFE ID under ToolHive's explicit association model. This behavior does not claim literal compliance with draft-ietf-oauth-spiffe-client-auth-02's X.509 `client_id` URI-SAN requirement.

## SPIFFE RFC 8693 delegation

A SPIFFE-authenticated client may use RFC 8693 only when its association enables
both the token-exchange grant and `token_exchange.enabled`. The authenticated
principal has two deliberately different identities: its canonical concrete
SPIFFE ID is the resolved actor identity, while the configured OAuth client ID
remains the authenticated client identity. A successful delegated token uses the
canonical SPIFFE ID as the outer `act.sub` and retains the configured OAuth
client ID as its `client_id` claim. Neither value is derived from the other.

An optional self-issued `actor_token` must bind both identities: its `client_id`
must exactly equal the configured OAuth client ID, and its `sub` must exactly
equal the canonical resolved SPIFFE ID. A SPIFFE client cannot use an
`actor_token` to substitute a delegate persona or another workload identity.

SPIFFE token exchange requires exactly one explicit RFC 8707 `resource` form
value. It rejects every `audience` form value, including an otherwise valid
configured token-exchange audience. The resource must be a server-allowed URI,
be listed in the association's resource policy, and be covered by the subject
token's `aud`. Association resources and token-exchange audiences are separate
policy inputs: an audience does not authorize a resource, and a resource does
not authorize an `audience` request.

For an externally issued subject token, `may_act.sub` is compared with the
resolved actor identity — the canonical SPIFFE ID — whereas
`allowedDelegateClients` is always checked against the configured OAuth client
ID. This preserves the distinction between workload provenance and the OAuth
client authorization that is permitted to exchange the external token. See
[External Subject-Token Exchange](17-token-exchange-delegation.md) for the
external-issuer consent model.

### Authorization parity and diagnostics

SPIFFE X.509-SVID and JWT-SVID authentication have parity when they produce the
same stable authorization inputs and authorization decisions for the same
resolved association: canonical SPIFFE ID, configured OAuth client ID,
association policy, grant, scopes, and requested resource. Parity does not
require byte-for-byte identical issued JWTs or matching volatile issuance
claims such as timestamps, expiry, token IDs, or signatures.

`authentication_method` (`spiffe_x509` or `spiffe_jwt`) is debug-only
operational metadata emitted in token-exchange diagnostics. It is not added to
the issued JWT and is excluded from Cedar authorization inputs. When Cedar
evaluates the presented ToolHive-issued JWT, it can read the nested `act` claim
in Cedar context when a policy needs delegation provenance; its principal identifier continues to use `sub`. A configured primary upstream
provider instead supplies Cedar's claim source.

## Security and delivery scope

Configuration and loaded bundles are not authentication by themselves. A client ID, a declared association, a request header, an unverified SPIFFE-looking URI, a client-supplied trust domain, or a loaded bundle is never workload identity. SPIFFE credential validation establishes identity only after the credential validates against configured trust material and the association registry authorizes the resulting SPIFFE ID and configured client ID. A successful authentication binds that exact resolved principal to the static OAuth client; storage lookup alone is never authenticated provenance.

The pieces described in this document map to these issues: live trust-bundle loading and rotation, plus discovery-metadata advertisement of `spiffe_x509`, `spiffe_jwt`, and `client_credentials`, [#6201](https://github.com/stacklok/toolhive/issues/6201); X.509-SVID client authentication and the auth server TLS listener that carries it, [#6202](https://github.com/stacklok/toolhive/issues/6202); JWT-SVID client authentication, [#6203](https://github.com/stacklok/toolhive/issues/6203); association-constrained `client_credentials` alongside token exchange for both arms, [#6204](https://github.com/stacklok/toolhive/issues/6204). `newServer` (`pkg/authserver/server_impl.go`) constructs and wires the JWT and X.509 bundle sources these paths validate credentials against.

Issue [#6205](https://github.com/stacklok/toolhive/issues/6205) adds a real SPIRE integration E2E that covers the positive flow through SPIRE-published JSON bundle ConfigMap material, the AS file source, and an attested client obtaining X.509-SVID and JWT-SVID credentials from the Workload API for equivalent `client_credentials` flows. It also covers X.509 and JWT local-authority/SVID rotation without restarting the client pod. Negative-path coverage is not asserted by this E2E.

The authorization-server file source is limited to public trust material. The supported deployment does not mount a Workload API socket into the authorization-server pod or use it to obtain an authorization-server SVID.

## Related documentation

- [Auth Server Storage Architecture](11-auth-server-storage.md) — dynamic storage and the static client overlay
- [Kubernetes Operator Architecture](09-operator-architecture.md) — operator-to-runner configuration boundary
- [External Subject-Token Exchange](17-token-exchange-delegation.md) — separate RFC 8693 delegation trust model

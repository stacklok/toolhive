# SPIFFE Association Declarations

The embedded authorization server supports SPIFFE X.509-SVID and JWT-SVID client authentication for configured associations. Each association maps a verified SPIFFE ID or terminal `/*` descendant pattern to an explicit OAuth `client_id` and its token-exchange policy.

## Configuration

`spiffe_trust_domains` declares the canonical trust domain, enabled methods (`spiffe_x509` and/or `spiffe_jwt`), and one bundle source. `inbound_grants.spiffe_client_auth` associates a principal pattern with a client ID, methods, scopes, audiences, optional resources, and the RFC 8693 token-exchange grant. The client ID is explicit; it is never derived from the SPIFFE ID.

An association must reference a declared domain, remain within that domain, and enable only methods allowed by the domain. Duplicate or overlapping patterns, duplicate client IDs, unknown domains, and disabled methods fail startup validation. Runtime bundle lookup also fails closed when a requested trust domain is unknown or the requested authentication method is not enabled for that domain.

`resources` and `audiences` are independent. Resources are absolute HTTP(S) URIs and must be in `allowed_audiences`; audiences are required RFC 8693 audiences and are not limited by that resource allowlist.

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

Supported sources are:

- `workload_api`, which uses the local SPIFFE Workload API's live bundle watch.
- `bundle_endpoint` with the `https_web` profile. The server initially fetches the bundle over Web-PKI-authenticated HTTPS, then refreshes it using the bundle refresh hint (at least 30 seconds and no more than 24 hours), or every five minutes when no hint is supplied. A failed refresh is retried after one minute and retains the last known good bundle for at most 24 hours; after that, authentication fails closed until a valid refresh succeeds. Initial bundles may omit `spiffe_sequence` for compatibility. Once a response supplies it, later responses must also supply a nondecreasing sequence. A lower or missing sequence, or changed content at an equal sequence, is rejected as a refresh failure and cannot extend the last-known-good age; an equal sequence with identical content is accepted.

Bundle endpoint URLs must be absolute HTTPS URLs without userinfo, query, or fragment. Their hosts cannot be IP literals or loopback addresses; redirects are restricted to the same host. The endpoint response is limited to 1 MiB.

`https_spiffe` is not supported. It requires future endpoint identity and bootstrap-trust configuration to authenticate the endpoint with an X.509-SVID. A `file` bundle source is deferred and is not supported by the current configuration or runtime.

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

## Related documentation

- [Auth Server Storage Architecture](11-auth-server-storage.md) — dynamic storage and the static client overlay
- [Kubernetes Operator Architecture](09-operator-architecture.md) — operator-to-runner configuration boundary
- [External Subject-Token Exchange](17-token-exchange-delegation.md) — separate RFC 8693 delegation trust model

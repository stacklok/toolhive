# SPIFFE Association Declarations

**Current status: SPIFFE client authentication is enforced when the trust bundle comes from a `file` source.** A SPIFFE trust configuration whose bundle source is `file` starts and is enforced for both JWT-SVID and X.509-SVID client authentication. Only the `file` source has a loader. `bundle_endpoint` (both the `https_web` and `https_spiffe` profiles) and `workload_api` are rejected in four places: by `RunConfig.Validate()`; by the `Config`-level check (`ValidateRuntimeSPIFFEBundleSourceType` and `validateRuntimeSPIFFEBundleSources` in `pkg/authserver/config.go`); by `MCPExternalAuthConfig` validation, so the config resource is reported invalid; and by the VirtualMCPServer inline `authServerConfig` validation. A workload that references a config also fails again when it reconciles. The CRD schema still accepts them on purpose; the restriction is meant to be lifted when loaders exist.

A SPIFFE-only deployment with no upstream, delegate client or trusted issuer is supported: `validateZeroUpstreamMode` counts SPIFFE associations. Each association's `grant_types` alone decides which grants it may use. RFC 8693 token exchange is enabled for an association only when that association lists the token-exchange grant; an association that grants only `client_credentials` leaves it off, and the `client_credentials` handler is registered only when some association lists that grant. For a deployment whose associations grant only `client_credentials`, and that has no delegate clients, `token_exchange` block or legacy trusted-issuer token-exchange policy, the token-exchange grant is not registered at the token endpoint, is not listed in `grant_types_supported` in `/.well-known/oauth-authorization-server`, and DCR rejects a registration that requests it. Still not supported: RFC 8705 discovery metadata (`tls_client_auth`, `mtls_endpoint_aliases` are not advertised). A SPIFFE-only VirtualMCPServer cannot set `incomingAuth.authzConfig`: `validateAuthzUpstreamAvailable` in `cmd/thv-operator/controllers/virtualmcpserver_controller.go` rejects any non-nil `authzConfig` when the embedded auth server has no upstreams, as it does for delegate-only setups. The plain `thv run` CLI has no flag for `embedded_auth_server_config`; the only CLI route is a hand-written runner RunConfig loaded with `thv run --from-config`.

The embedded authorization server carries the SPIFFE association model across the `RunConfig` boundary. `newServer` builds the configured bundle source, which loads and polls a local trust-bundle file, and passes its X.509 and JWT views to the client-authentication strategy.

## Auth-server TLS listener

The proxy runner and vMCP can serve authorization-server routes on an additional TLS listener at fixed port `8443`. Configure the serving certificate and private key in the embedded auth-server `RunConfig` (for vMCP, the `authserver-config.yaml` file next to the vMCP config); mount both files read-only into the process and keep the private key accessible only to that process:

```yaml
tls_listener:
  cert_file: /etc/toolhive/auth-server/tls.crt
  key_file: /etc/toolhive/auth-server/tls.key
```

The listener binds the same host as the MCP listener (the proxy runner's host or vMCP's `--host`, falling back to loopback when unset). A missing or invalid initial certificate/key pair, a bind failure, or a port-8443 conflict with the MCP port (proxy port or vMCP `--port`) or the enabled diagnostics listener fails startup; the listener does not select another port. Certificate files are checked lazily during TLS handshakes, at most once every 30 seconds. A valid changed pair is used for subsequent handshakes; a failed reload keeps serving the last good pair.

This listener serves OAuth/OIDC authorization-server routes only. Those routes remain mounted on the MCP transport port as well; enabling the listener does not make MCP traffic HTTPS. The listener uses the process's bind host, so binding a non-loopback address exposes it on that network interface: restrict network reachability with deployment firewall/network policy controls and protect the certificate key. In the proxy runner and vMCP, this listener is the only place X.509-SVID client authentication can work (see [X.509 client authentication](#x509-client-authentication)). vMCP starts this listener from `vmcp serve` / `thv vmcp serve`; programs that embed the vMCP server library directly do not. X.509 client authentication needs TLS passthrough to the listener: a proxy or Ingress that terminates TLS removes the client certificate, and one that opens its own TLS connection to the listener is authenticated as itself, which is worse (see "Where it works" under X.509 client authentication).

The listener asks for a client certificate (`tls.RequestClientCert`) only when an association enables `spiffe_x509` (`RunConfig.HasSPIFFEX509ClientAuth()`). Otherwise it sets `tls.NoClientCert`. When it does request one, it does not verify the chain during the TLS handshake. Verification happens later, in the client-authentication strategy, against the live trust bundle, and only for a request whose `client_id` names an association enabled for `spiffe_x509`. Every other client on this listener is unaffected by a certificate it presents.

### Operator configuration

In the operator, set `embeddedAuthServer.tlsListener` with `certificateSecretRef` and `privateKeySecretRef` (both required). The operator mounts them read-only as a directory at `/etc/toolhive/authserver/tls` (`tls.crt` and `tls.key`), so Secret content changes reach the listener's reload without a pod restart; pointing the listener, signing keys, HMAC secrets or the auth server Redis storage CA (`storage.redis.tls` or `sentinelTls` `caCertSecretRef`) at a different Secret or key, or a SPIFFE bundle file at a different ConfigMap or key, rolls the pods. `spiffe_x509` in `inboundGrants.spiffeClientAuth` requires `tlsListener`. The operator checks this when it reconciles the resource, and the runner checks it again when it validates its configuration. The API server does not reject it, so a violation shows up as a failed status condition on the resource that uses the auth server, and the `MCPExternalAuthConfig` is reported invalid too because its validation calls `ValidateTLSListener`. `spiffeTrustDomains` alone does not require a listener. MCPServer and MCPRemoteProxy reject a `proxyPort` of 8443 when the referenced auth server sets `tlsListener`. This is also reported as a failed status condition, not by the API server. When the listener is enabled, the operator adds an `https-auth` container port and Service port 8443 alongside the existing `http` port. The operator does not treat extra ports on the Service or the container as drift, but any update it makes to the Service or the Deployment, including adding or removing `https-auth`, replaces that port list, so ports added by hand are removed at that point. A container port named `https-auth` on 8443/TCP (the port the operator renders) is removed while the listener is off. If a vMCP `spec.podTemplateSpec` adds that exact port itself, the Deployment is updated on every reconcile. A port with that name on another number or protocol is not treated as the operator's port. The Service port sets `appProtocol: https`, which meshes and Gateway implementations may read to choose the protocol. Port 8443 is published wherever the Service is published, so on a VirtualMCPServer with `serviceType: LoadBalancer` or `NodePort` it is reachable from outside the cluster. It serves the same authorization-server routes that the `http` port already serves. The auth server takes client certificate identity only from the TLS connection itself, never from headers such as `X-Forwarded-Client-Cert`, so a mesh or Ingress in front of the listener cannot pass a client identity along in a header. A hop that opens its own TLS connection to the listener is a different case, covered under "Where it works" in the X.509 section.

## Model and boundaries

Configuration separates top-level trust declarations from canonical client associations:

- `spiffe_trust_domains` in `RunConfig` (`spiffeTrustDomains` in the CRD) names a canonical SPIFFE trust domain, declares the methods intended for authentication, and selects a trust-bundle source.
- `inbound_grants.spiffe_client_auth` in `RunConfig` (`inboundGrants.spiffeClientAuth` in the CRD) associates an exact SPIFFE ID or a terminal `/*` descendant `principalPattern` with one explicit OAuth `client_id`, methods, resources, and scopes. This is a sibling of `inbound_grants.token_exchange` and `inbound_grants.jwt_bearer`, not nested under either — client authentication does not by itself confer a grant.

The OAuth client ID is configured explicitly; it is never derived from the SPIFFE ID or pattern. Each association declares its grants in `grant_types`. Each entry is `client_credentials` or `urn:ietf:params:oauth:grant-type:token-exchange`. The list must be non-empty, and an entry may appear only once; an empty or unknown value is rejected. There is no separate per-association `token_exchange` switch, so listing the grant is the only way to enable it. The CRD does not expose `grantTypes` yet: the operator always supplies the token-exchange grant in `buildSPIFFEClientAuthRunConfigs` (`cmd/thv-operator/pkg/controllerutil/authserver.go`), so only a hand-written RunConfig can list `client_credentials`.

### Resources are the only target list

Each association lists the RFC 8707 `resources` its workload may ask for. Both grants use them the same way:

- `resources` is required and must be non-empty on every association, whatever its `grant_types`. Each entry must be a syntactically valid absolute HTTP(S) URI, and each must also be a member of the server's `allowed_audiences` allowlist (`RunConfig.AllowedAudiences`) — the same allowlist `DelegateClientRunConfig.Audiences` is validated against. The CRD requires a non-empty list (a CEL rule) and checks the shape of each entry (an item pattern). The operator checks allowlist membership at reconcile time, and `validateSPIFFEResources` in `pkg/authserver/spiffe_trust.go` checks all of it again at startup.
- `audiences` is deprecated and ignored. It stays in the CRD and the RunConfig as an optional field because v0.65.0 required it, so existing objects keep validating. A non-empty value is listed by the `DeprecatedInboundGrantConfiguration` status condition (the operator reports `inboundGrants.spiffeClientAuth[i].audiences`, with `...spiffeClientAuth[i].resources` as the replacement), and the auth server logs one WARN per association at startup. It never affects which tokens are issued. Remove it and list the target in `resources`.

The runtime OAuth client built for a SPIFFE association (`registration.NewSPIFFEClient`) is constructed from `grant_types`, `scopes`, and `resources`. Its `GetAudience()` is always empty; request handlers read `Resources()`.

### Token exchange requests

Clients that carry a resource list, such as SPIFFE clients, are targeted by exactly one RFC 8707 resource from their association in RFC 8693 token exchange, and `client_credentials` uses the same rule. The handler is `grantAndBoundAudiences` in `pkg/authserver/server/tokenexchange/handler.go`, which hands those clients to `GrantSingleResource` in `pkg/authserver/server/audience.go`. `client_credentials` uses the same function, so the two grants refuse the same requests with the same hints:

- The request must carry exactly one non-empty `resource`. A missing, empty or repeated `resource` is rejected with `invalid_target`. There is no default audience.
- Any `audience` parameter is rejected with `invalid_target`, even when a valid `resource` is also sent.
- The resource must be an absolute `http` or `https` URI with a host and no fragment, must be a member of the server's `allowed_audiences`, and must be an **exact** member of the association's `resources`. Otherwise the request fails with `invalid_target`.
- The granted audience must also be covered by the subject token's `aud`, as for every client. A resource outside the subject token's audience is rejected.

Delegate clients and other clients (`private_key_jwt`, `client_secret`, DCR and CIMD) keep the existing rule: `audience`, `resource` or the default audience, checked against the client's own audiences.

### client_credentials requests

The `client_credentials` grant is handled by `Handler` in `pkg/authserver/server/clientcredentials/handler.go`, a thin wrapper around Fosite's stock client-credentials handler. It adds the association policy checks below and then lets Fosite issue the token, so lifetime and signing match the other grants. A request is handled as follows:

- The client must be an authenticated SPIFFE client (JWT-SVID or X.509-SVID) whose association lists `client_credentials`. Any other client, or a SPIFFE client without the grant, gets `unauthorized_client`. This check runs before the scope and resource checks, so a client without the grant learns nothing about its lists.
- The request must carry exactly one non-empty `resource`. A missing, empty or repeated `resource` is rejected with `invalid_target`. Any `audience` parameter is rejected with `invalid_target`; this grant takes `resource` only.
- The resource must be an absolute `http` or `https` URI with a host and no fragment, must be a member of the server's `allowed_audiences`, and must be an **exact** member of the association's `resources`. Otherwise the request fails with `invalid_target`.
- Token exchange for SPIFFE clients follows the same rule (see above), so neither grant falls back to a default audience.
- Each requested scope must be in the association's `scopes`, otherwise the request fails with `invalid_scope`. If no scope is requested, the token carries no scopes. Resource servers must not read a token without scopes as unrestricted.

The access token has these claims: `sub` is the verified SPIFFE ID; `client_id` is the association's client ID; `aud` is the requested resource; `scp` is the granted scopes as an array, as for the other grants (there is no space-separated `scope` string); and `https://toolhive.dev/no_upstream_session` (`claimnames.NoUpstreamSessionKey`) is `true`, marking a token that never came from an upstream IdP login. The JOSE `typ` header is `at+jwt` when the server's access-token type is `at+jwt`, as for all grants. No refresh token is issued.

```console
curl -X POST https://auth.example.com/oauth/token \
  --data-urlencode 'grant_type=client_credentials' \
  --data-urlencode 'resource=https://mcp.example.com' \
  --data-urlencode 'scope=openid' \
  --data-urlencode 'client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-spiffe' \
  --data-urlencode "client_assertion=$JWT_SVID"
```

### Upgrading

Compared with v0.65.0, every association needs `resources`, and SPIFFE token exchange now uses the resource-only rule above. An association without `resources` (previously allowed for token exchange only) fails validation. The CEL rules reject a CR at admission when it is created, or when its `spiffeClientAuth` list is changed, without `resources`. The list is at `spec.embeddedAuthServer.inboundGrants.spiffeClientAuth` on MCPExternalAuthConfig and `spec.authServerConfig.inboundGrants.spiffeClientAuth` on VirtualMCPServer. On clusters with CRD validation ratcheting (the `CRDValidationRatcheting` feature, on by default since Kubernetes 1.30), an update that leaves that list unchanged is still admitted, including other edits to the auth server config and finalizer removal on delete. The MCPExternalAuthConfig's own `Valid` condition then reports the missing `resources`, and servers that consume it fail to reconcile until it is added. An inline VirtualMCPServer configuration reports it on its `AuthServerConfigValidated` condition. The auth server refuses to start with such a RunConfig. A SPIFFE client that sends `audience=` or no target now gets `invalid_target` instead of a token; it must send one `resource` listed in its association. On a backend that keeps placeholders between runs (Redis), the first start of the new version logs one `configured client replaced with a different policy` WARN for each association. This happens because every placeholder stored by v0.65.0 carries `audiences` (they were required then), and the new placeholder does not. It happens even if you already removed `audiences` from the configuration, since the WARN comes from the stored row, not the current config. Each replica that reads the old row before it is rewritten logs it, and it can repeat if a v0.65.0 replica restarts during the rollout and writes the old row back. Later restarts do not log it, because a nil and an empty `audiences` list compare equal. Nothing is authorized differently by the replacement. Setting `audiences` still validates and is only reported as deprecated.

A second break: an existing CR or RunConfig that has a `spiffe_jwt` association together with `insecureAllowHTTP` (`insecure_allow_http` in a RunConfig), or with an `http://` issuer and no loopback opt-in, now fails validation (see "Known limitation: JWT-SVID replay"). For a CR, the CEL rules reject the object at admission when it sets `insecureAllowHTTP`, or has an `http://` issuer without `insecureAllowConfidentialOverLoopbackHTTP`. A non-loopback `http://` issuer that does set the loopback opt-in passes the CEL rules and is rejected only by the Go validation at reconcile. On clusters with CRD validation ratcheting, an update that leaves `embeddedAuthServer` unchanged may still be admitted, but reconcile fails in every case. For a RunConfig, `RunConfig.Validate` fails. To fix it, remove `insecureAllowHTTP` (`insecure_allow_http`), which is rejected whatever the issuer scheme, and serve the issuer over TLS with an `https://` URL. A plain-HTTP issuer is accepted only when it is a loopback address and `insecureAllowConfidentialOverLoopbackHTTP` (`insecure_allow_confidential_over_loopback_http`) is set; a non-loopback `http://` issuer is always rejected.

### Trust-bundle source

Every trust domain must declare exactly one `bundle_source`, a discriminated union naming the trust-bundle source. `newServer` constructs the configured source and passes its X.509 and JWT bundle views to the authorization server.

**Supported bundle sources.** Only `file` has a loader. The other two types pass the structural checks below but are rejected by `RunConfig.Validate()`, the `Config`-level check, the `MCPExternalAuthConfig` validation and the VirtualMCPServer inline validation, so they fail at reconcile time in the operator and at startup elsewhere:

- `type: file` requires a `file` block naming a locally mounted SPIFFE JWKS trust-bundle document. The loader reads regular files (including symlinks to regular files) and polls for rotations, so a rotated bundle applies without a restart. A reload whose sequence number is missing, lower than, or conflicts with the one already observed is rejected and the last known good bundle is kept. That bundle stops authenticating once 24 hours have passed since the last successful load (not since the bundle was issued), and SPIFFE client authentication then fails closed. The server logs a WARN once when a reload is rejected for its sequence number (`SPIFFE bundle file reload rejected: sequence number rejected; retaining last known good bundle, which stops authenticating once it goes stale`), and once for any other reload failure (`SPIFFE bundle file reload failed; retaining last known good bundle`); a WARN is logged again only when the failure kind changes or after a recovery. It also logs a WARN each time the held bundle enters the stale state (`SPIFFE trust bundle is stale; SPIFFE client authentication fails closed until a fresh bundle loads`), including again after a recovery. Recovery is logged at Debug. The WARN lines carry the trust domain and file path and the Debug recovery lines only the trust domain; none carries bundle contents. Re-reading an unchanged file counts as a successful load, so staleness does not detect a publisher that stopped updating the file. On `RunConfig` this is an absolute filesystem path (`file.path`); the CRD instead names a `configMapName`/`configMapKey` pair, since the operator projects that ConfigMap key into a per-domain directory under `/etc/toolhive/authserver/spiffe-bundles/<index>/bundle.json` rather than accepting a raw path.
- `type: bundle_endpoint` requires an `endpoint` block with an absolute HTTPS URL (no userinfo, query, fragment, IP-literal, or loopback host) and a `https_web` or `https_spiffe` profile. `RunConfig.Validate` checks the URL structure through `ValidateSPIFFETrust`, but at `MCPExternalAuthConfig` admission `ValidateSPIFFEBundleSourceTypes` rejects every non-file source first, so the endpoint URL check cannot fail on that path until a loader exists.
- `type: workload_api` selects the local SPIFFE Workload API and carries no payload. No Workload API loader exists, so it is rejected as described above.

The following canonical operator excerpt shows a supported shape. `allowedAudiences` is intentionally absent here: it is not a configurable field on `embeddedAuthServer` — it is derived at reconcile time from the resolved incoming OIDC configuration.

```yaml
spec:
  type: embeddedAuthServer
  embeddedAuthServer:
    issuer: https://auth.example.com
    tlsListener:
      certificateSecretRef:
        name: auth-server-tls
        key: tls.crt
      privateKeySecretRef:
        name: auth-server-tls
        key: tls.key
    spiffeTrustDomains:
      - name: production
        trustDomain: example.org
        methods: [spiffe_x509, spiffe_jwt]
        bundleSource:
          type: file
          file:
            configMapName: spiffe-trust-bundle
            configMapKey: bundle.json
    inboundGrants:
      spiffeClientAuth:
        - trustDomainRef: production
          principalPattern: spiffe://example.org/workloads/reporting/*
          clientId: reporting-workloads
          methods: [spiffe_x509]
          resources: [https://mcp.example.com]
          scopes: [openid]
```

An association is valid only when it references a declared domain, its `principalPattern` belongs to that domain, and its methods are enabled by that domain. A principal consisting only of a trust domain, such as `spiffe://example.org`, is invalid because it cannot match an SVID. Patterns may be exact or end in `/*`; the domain-wide `spiffe://example.org/*` wildcard remains valid. A wildcard matches descendants at a path boundary only, not its base path or a partial segment. Each pattern and client ID has one owner. Duplicate or overlapping patterns, duplicate client IDs, unknown domains, disabled methods, an unreferenced trust domain, and invalid or incomplete policies all cause validation to fail rather than relying on configuration order.

## Static OAuth client registry

At authorization-server startup, validated associations build an immutable registry and static OAuth-client overlay (`SPIFFEStorageDecorator`), installed as the outermost storage decorator — after CIMD (`decorateStorageForSPIFFE` in `pkg/authserver/server_impl.go`). Its clients are configuration-only: they are held in memory and never written to the storage backend (memory or Redis), cannot be registered or replaced dynamically through `/oauth/register`, and retain only the association's configured policy. `GetClient` checks this static map first and only falls through to the dynamic backend (CIMD, then DCR) when the requested client ID is not one of the configured associations, so a durable client can never shadow a static one. `RegisterClient` rejects a DCR registration, and `ReconcileConfiguredClient` rejects a delegate client, that targets a reserved static client ID. An unknown `spiffe://` client ID does not trigger CIMD resolution. A SPIFFE `client_id` must not equal a delegate `client_id`: `RunConfig.Validate()`, `Config.Validate()` and the operator's `MCPExternalAuthConfig` validation reject it (`ValidateSPIFFEClientIDsNotDelegates`), so the collision no longer surfaces at storage startup.

### Startup collision handling

Within one configuration, a SPIFFE client ID that equals a delegate client ID is rejected by `ValidateSPIFFEClientIDsNotDelegates` at `RunConfig.Validate`, at `Config.Validate`, at `MCPExternalAuthConfig` validation and at the VirtualMCPServer inline `authServerConfig` validation (`EmbeddedAuthServerConfig.ValidateInboundGrants`). A placeholder or delegate row that an earlier configuration left in storage is not covered by that check: `ReconcileConfiguredClient` still refuses it at startup, as described below.

Startup does not simply refuse to start whenever a client with a static ID already exists in durable storage. Each configured static client ID is durably claimed through `ReconcileConfiguredClient` (`preflightDurableCollisions` in `pkg/authserver/storage/spiffe_decorator.go`), which creates the placeholder or replaces an existing one, and refuses anything that is not a placeholder:

- If no client is stored at that ID, it creates an inert placeholder and starts normally.
- If a placeholder for the same client ID already exists, reconciliation replaces it. With an unchanged configuration (the restart case) nothing differs and nothing is logged. With a changed association (different scopes, resources, grant types, or SPIFFE identity) the server logs the `configured client replaced with a different policy` WARN, and the change takes effect on rollout.
- If the existing record is DCR-issued, or is not a reserved placeholder (for example a delegate client), reconciliation fails and the server refuses to start. A placeholder and a non-placeholder client never replace each other, in either direction.

The durably-claimed record is always an inert placeholder — a client with no grant types and no response types, so it can never itself be issued a token — never the real SPIFFE client with its configured scopes and resources. This durable claim, not just an in-process `GetClient` check, closes a cross-replica race: with Redis and multiple replicas, an older or still-rolling replica without this SPIFFE config could otherwise DCR-register the same client ID after a newer replica's read-only check passed. Claiming the ID durably makes the reservation visible to every replica immediately.

On every startup, the server reconstructs the static registry and its overlay from serialized configuration. A restart with the same configuration produces the same associations and reconciles cleanly against the previous run's placeholders. A **changed** association (different scopes, resources, grant types, principal pattern, methods, or trust-domain reference at the same client ID) replaces the stored placeholder, logs the `configured client replaced with a different policy` WARN with the old and new policy values, and takes effect when the new pods roll out. For the SPIFFE identity the WARN shows only `identity_changed=true`, not the hash or any grant value. A changed grant-type list therefore logs the WARN with `identity_changed=true` and no grant value, because the placeholder stores no grant types. On the first rollout after upgrading to this version, every SPIFFE placeholder logs one such WARN, because its stored identity hash changes now that the hash covers grant types. During a mixed-version rolling update, a replica still running the previous version writes the previous hash back, so a later replica start can log the WARN again. The placeholder row is inert: it has no grant types, is never admitted for a token, and `GetClient` serves each replica's in-process overlay, so replacing it authorizes nothing differently. Strictness remains only against non-reserved clients, so a DCR or delegate client cannot take over a SPIFFE client ID, and the reverse. Old and new replicas each serve their own configuration during a rollout, and nothing marks when it ends. A rename of the trust-domain reference counts as a change, because the reference is part of the identity hash. Edits that the placeholder does not store, such as a trust domain's bundle source, do not log the WARN.

A **removed** association's active policy does take effect on a successful restart — the in-memory overlay is rebuilt from the current configuration, so a client with no matching association is no longer served as a static client. What does *not* clean up is its durable reservation: nothing currently deletes the inert placeholder `ReconcileConfiguredClient` claimed for that client ID, so it persists in storage indefinitely, preventing the ID from being reused by DCR or a delegate client (tracked as [#6477](https://github.com/stacklok/toolhive/issues/6477)). Dynamic clients remain subject to the storage backend's own persistence, but no stale static client is restored from storage — the in-memory overlay's clients always come from the current configuration, never from a prior run's storage state.

## JWT-SVID client authentication

JWT-SVID client authentication is enforced for configured associations whose trust domain uses a `file` bundle source. The client-authentication strategy (`newSPIFFEClientAuthenticationStrategy` in `pkg/authserver/server/spiffe_client_auth.go`) picks a path in this order:

1. If any `client_assertion_type` form value is the SPIFFE JWT type and a SPIFFE resolver is configured, the SPIFFE JWT path runs and the TLS client certificate is ignored. A leaf certificate presented alongside the assertion is transport identity, not a second credential. The shared dispatcher is otherwise untouched, so an entirely non-SPIFFE request (including RFC 7523 private-key JWT) still reaches Fosite's default strategy. Within the SPIFFE JWT path, a duplicate or otherwise ambiguous `client_assertion_type` is rejected.
2. If no SPIFFE resolver is configured, the request goes to Fosite's default strategy unchanged.
3. If any `client_id` form value names a configured association whose methods include `spiffe_x509`, the X.509 path runs (see below).
4. Otherwise the request goes to Fosite's default strategy and any peer certificate is ignored. A presented SVID is transport identity unless the requested client is registered for `spiffe_x509`, so JWT-SVID clients, delegate clients (`client_secret_post`, `private_key_jwt`, HTTP Basic) and public, DCR or CIMD clients keep working when their mesh presents an SVID on every outbound connection.

The server's own registration, never trial and error, picks the verifier, so exactly one runs per request. The X.509 path never falls back to the default strategy, and a failed default strategy never retries the X.509 path. A SPIFFE client cannot authenticate through the default strategy: it has no secret and is not public.

The implemented JWT-SVID assertion validator accepts a serialized client assertion, limited to 16 KiB, and validates it with go-spiffe `jwtsvid.ParseAndValidate` against the configured JWT bundle source (`AuthorizationServerParams.SPIFFEJWTBundleSource`). The validator requires the assertion's sole audience to be the configured authorization-server issuer. It does not require an `iss` claim: RFC 7519 makes `iss` OPTIONAL, and the SPIFFE JWT-SVID specification does not mandate it either, so a conformant SVID signed by a bundle-trusted key is accepted whether or not it carries one. This path reaches go-jose/v4's default one-minute claim leeway through go-spiffe v2.7.0; the leeway is inherited and not configurable in this code path. The validator rejects an assertion whose remaining validity exceeds six minutes, representing the recommended five-minute JWT-SVID issuer lifetime plus that one-minute clock-skew allowance. The JWT bundle view is the live, polled bundle, so a rotated bundle applies without a restart.

`client_id` is optional for this authentication method. When it is omitted, the registry derives the configured OAuth client from the verified SPIFFE ID association. When it is supplied, it is treated as an exact selector and must match that association's configured client ID; the server does not normalize the value. For example:

```console
curl -X POST https://auth.example.com/oauth/token \
  --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
  --data-urlencode "subject_token=$SUBJECT_TOKEN" \
  --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:access_token' \
  --data-urlencode 'client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-spiffe' \
  --data-urlencode "client_assertion=$JWT_SVID"
```

After validation, the authentication strategy calls the shared `SPIFFEAssociationRegistry.Resolve` path synchronously through its JWT resolver. The registry verifies the enabled JWT method and either derives the association's configured client ID or checks the supplied selector's ownership, then returns the configured static OAuth client wrapped in `registration.AuthenticatedSPIFFEClient`. Token exchange selects the single-resource rule for any client that carries a resource list, and the wrapper carries one through the client it embeds. The wrapper also carries the verified SPIFFE ID and authentication method and is what the `client_credentials` handler requires: any other client is refused with `unauthorized_client`. In production only this resolver builds it. It exists only on the client produced by client authentication: a client loaded from storage, for example when a stored request is read back, is the plain static client and does not carry it.

After the SPIFFE JWT arm is selected, malformed request fields use generic OAuth `invalid_request` errors; validation, association, and mixed-credential failures use generic `invalid_client` errors. In that arm, an HTTP Basic authorization header or any `client_secret` form field causes client authentication to fail. Credential material is not logged or included in errors.

### Known limitation: JWT-SVID replay

A JWT-SVID is a bearer credential, and the SPIFFE path has no application-level replay protection: no `jti` persistence, nonce, or proof-of-possession binding. Its `jti` is not stored, and the replay store that Fosite uses for `private_key_jwt` is wired only into the default strategy. A captured valid assertion can therefore be reused until its expiry, subject to these limits:

- The assertion's sole audience must be the authorization-server issuer.
- The assertion's remaining validity must not exceed six minutes when it is checked (`maxSPIFFEJWTAssertionRemainingValidity` in `pkg/authserver/server/spiffe_client_auth.go`), plus the one-minute clock leeway that go-spiffe inherits from go-jose.

Because the JWT-SVID is a bearer credential, an association that allows the JWT-SVID method counts as a confidential client in the cleartext-transport check (`RunConfig.hasConfidentialClients`, `Config.hasStaticConfidentialClients` and `ValidateConfidentialClientTransport`), as delegate clients do. `insecure_allow_http` is rejected, and an `http` issuer is accepted only when `insecure_allow_confidential_over_loopback_http` is set and the issuer host is loopback. The check runs in `RunConfig.Validate()`, `Config.Validate()` and the operator's `EmbeddedAuthServerConfig.ValidateInboundGrants`. An association that enables only `spiffe_x509` does not trigger it. The API server rejects these configurations at admission: the CEL rule that forbids `insecureAllowHTTP` with confidential clients, and the rule that requires `insecureAllowConfidentialOverLoopbackHTTP` for an `http://` issuer, both treat a `spiffeClientAuth` entry with `spiffe_jwt` in `methods` as a confidential client. The Go check in `ValidateInboundGrants` is the backstop for objects stored before those rules covered SPIFFE associations.

With the `client_credentials` grant, each request that presents a replayed SVID mints a new access token. One captured SVID can therefore mint any number of access tokens until it expires, not just one. Each token lives for the server's normal access-token lifetime (`AccessTokenLifespan`), and that lifetime is not capped to the SVID's expiry. Tokens are not bound to the client certificate or key (RFC 8705), and that binding is not implemented. This is accepted behavior: the SPIFFE specification treats JWT-SVIDs as bearer tokens and does not require `jti`.

## X.509 client authentication

X.509-SVID client authentication is implemented in `authenticateSPIFFEX509Client` and `verifySPIFFEX509` (`pkg/authserver/server/spiffe_client_auth.go`). The strategy reads the client certificate from `r.TLS` itself. There is no separate middleware and no identity in the request context.

**When it applies.** The X.509 path is used when any `client_id` form value names a configured association that enables `spiffe_x509` and the request carries no SPIFFE JWT assertion type. It then fails closed on any error, including a missing certificate, a certificate without a `spiffe` URI SAN, or a malformed SPIFFE ID. It never falls back to another client-authentication method. Every `client_id` value is checked, so a duplicated `client_id` where one value names such an association still reaches the exact-`client_id` check and is rejected. This holds on every endpoint that authenticates clients through Fosite's strategy and is served on the TLS listener, such as the token endpoint and the device authorization endpoint. For any other client, a presented certificate is ignored (see the routing order under JWT-SVID client authentication).

**Request rules.**

- `client_id` is required (RFC 8705 section 2).
- No other client credential may be present: no HTTP Basic header, `client_secret`, `client_assertion` or `client_assertion_type`.

A duplicated or ambiguous `client_id` gives `invalid_request` (HTTP 400); mixed credentials, a missing or invalid certificate, a failed leaf profile or chain check, or a resolver rejection give `invalid_client` (HTTP 401).

**Verification.** The chain is re-verified on every request with go-spiffe `x509svid.Verify` against the live, polled bundle, so rotation applies without a restart. ToolHive also enforces a leaf profile on top of go-spiffe:

- a critical key-usage extension that includes `digitalSignature`;
- a basic-constraints extension that is present, with CA set to false;
- a SAN extension, which must be critical when the subject is empty;
- a non-root SPIFFE ID with a canonical path;
- if an extended-key-usage extension is present, it must contain both `serverAuth` and `clientAuth`.

A client that presents more than four certificates (the leaf plus three intermediates) is rejected with `invalid_client` before any verification, which bounds the chain-building work a client can cause.

After verification, the SPIFFE ID and `client_id` are resolved through `SPIFFEAssociationRegistry.Resolve` to a configured association whose methods include `spiffe_x509`. The ID always comes from the verified leaf. The X.509 arm goes through the same resolver and also yields `registration.AuthenticatedSPIFFEClient`.

**Where it works.** In the proxy runner and vMCP, X.509 is reachable only on the auth-server TLS listener on port 8443, which requests a client certificate (`tls.RequestClientCert`) and leaves chain verification to the strategy. `RunConfig.Validate()` and the operator reject `spiffe_x509` associations when `tls_listener` is not set, and when the issuer is not an `https` URL. The issuer rule is checked at validation (`ValidateSPIFFEX509Issuer`), not only at startup, so an invalid `MCPExternalAuthConfig` or `VirtualMCPServer` is reported as invalid instead of crash-looping. At startup the runner logs a WARN when the issuer port is not 8443 (`auth server issuer port differs from TLS listener port`), and a WARN when the listener certificate does not match the issuer hostname (`auth server TLS listener certificate does not match issuer hostname`). A program that calls `authserver.New` directly must terminate TLS itself and request client certificates (see the godoc on `New` in `pkg/authserver/server.go`). Behind a TLS-terminating proxy, X.509-SVID authentication is unavailable; use JWT-SVIDs. Identity is never read from forwarded headers such as `X-Forwarded-Client-Cert`.

The authenticated client is whichever peer completes the TLS handshake, so TLS must reach the listener end to end from the workload. A proxy or gateway that opens its own TLS connection to port 8443 is authenticated as itself, not as the caller behind it. Take a gateway whose SVID matches an association's principal pattern, for example `spiffe://example.org/ns/prod/*`. An external caller who sends that association's `client_id` and no credential of its own then authenticates as that client. Token exchange still needs a valid subject token that passes the delegation consent check, which limits the damage, but the client-authentication protection against a stolen subject token is gone. Use TLS passthrough to port 8443, and write principal patterns that cannot match proxy or gateway identities: exact SPIFFE IDs or per-service-account paths, not a namespace wildcard that includes the gateway's service account. The `appProtocol: https` on the Service port is a hint some gateways read to choose the backend protocol. Do not pair it with a setting that makes a gateway re-originate TLS to this port.

**Logging.** Rejections are logged at Debug level with the failing stage. The certificate and assertion are never logged.

## ID-JAG grant exclusion

SPIFFE association clients are included in the explicit-grant client set of the ID-JAG handler (`idJAGExplicitGrantClientIDs` in `pkg/authserver/server_impl.go`). They cannot redeem an ID-JAG through the jwt-bearer grant; the redemption fails with `unauthorized_client` and the hint `The client is not registered for the jwt-bearer grant.` Their static client holds exactly the grant types the association declares (`client_credentials`, token exchange, or both), so jwt-bearer is never among them.

## Security and delivery scope

Configuration and loaded bundles are not authentication by themselves. A client ID, a declared association, a request header, an unverified SPIFFE-looking URI, a client-supplied trust domain, or a loaded bundle is never workload identity. JWT-SVID validation establishes identity only after the assertion validates against configured trust material and the association registry authorizes the resulting SPIFFE ID and configured client ID.

Both client-authentication paths (JWT-SVID, [#6203](https://github.com/stacklok/toolhive/issues/6203), and X.509-SVID, [#6202](https://github.com/stacklok/toolhive/issues/6202)) are implemented and enforced for trust domains that use a `file` bundle source. `newServer` (`pkg/authserver/server_impl.go`) constructs the multi-domain bundle source and wires it into both the JWT and X.509 verification interfaces. The auth-server TLS listener (proxy runner and vMCP) is documented above.

Still pending: bundle-endpoint and Workload API loaders; RFC 8705 discovery metadata ([#6204](https://github.com/stacklok/toolhive/issues/6204)); and deploying SPIRE or mounting Workload API sockets ([#6205](https://github.com/stacklok/toolhive/issues/6205)).

For [#6205](https://github.com/stacklok/toolhive/issues/6205), `workloadapi.X509Source` implements both `x509svid.Source` and `x509bundle.Source`, so one Workload API connection can also provide the authorization server's own certificate when deployment wiring is added. The v1alpha1 `ClientCASecretRef` plus `subPath` shape cannot support a rotating bundle and must not be reused for this purpose.

Both paths establish identity only from validated SVIDs and then resolve that identity through the association registry. They fail closed for missing associations, client-ID ownership mismatches, unknown trust domains, and methods not enabled by policy.

## Related documentation

- [Auth Server Storage Architecture](11-auth-server-storage.md) — dynamic storage and CIMD behavior below the static overlay
- [Kubernetes Operator Architecture](09-operator-architecture.md) — operator-to-runner configuration boundary
- [External Subject-Token Exchange](17-token-exchange-delegation.md) — separate RFC 8693 delegation trust model

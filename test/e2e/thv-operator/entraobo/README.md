# Entra OBO live-tenant e2e suite

This suite runs ToolHive's Microsoft Entra on-behalf-of (OBO) backend auth end
to end against a **real Entra tenant** on the local Kind cluster. Unit,
envtest, and integration tests cover OBO against a mock token endpoint; this
suite catches what a mock cannot: v1 vs v2 issuers, `aud` formats, `.default`
scope handling, consent errors, and conditional access.

It is opt-in and never runs in CI:

- Every spec skips unless all the required `TOOLHIVE_E2E_ENTRA_*` variables
  are set. The skip message names the missing ones.
- The specs carry the Ginkgo label `entra-live`, and
  `task thv-operator-e2e-test-run` (what CI runs) excludes that label, so a
  runner with stray variables still cannot reach a tenant.

## What it checks

Every scenario sends a real user token, calls a tool on a backend that records
the bearer tokens it receives, and then verifies the recorded token in the test
process. The token must be signed by the tenant (checked against its JWKS),
carry the v2 issuer and the backend audience, belong to the test user (`oid`),
and differ from the user's own token.

| Spec | Proves |
|---|---|
| `Entra OBO: MCPServer` | The proxyrunner exchanges the user token. A token for another audience (the exchanged backend token) is refused with 401 at the incoming OIDC layer. |
| `Entra OBO: MCPRemoteProxy` | The same exchange through a remote proxy. |
| `Entra OBO: VirtualMCPServer inline outgoingAuth` | Two backends with separate OBO configs and Secrets each receive their own exchanged token, and the vMCP pod carries two distinct `TOOLHIVE_OBO_CLIENT_SECRET_<NAME>` variables. |
| `Entra OBO: VirtualMCPServer discovered outgoingAuth` | vMCP exchanges the token for a discovered MCPServerEntry backend. The MCPServer-backend case is pending: vMCP reaches a discovered MCPServer through its proxy, which would exchange the token a second time. |
| `Entra OBO: exchange failures` | A wrong client secret gives 502 while the server still starts. Optionally, an unconsented scope gives 401, and a conditional-access challenge gives 403 plus an `obo_token_exchange` audit event. |

Tokens never appear in test output. Logs and failure messages show a short
SHA-256 fingerprint instead, and the backend's request log does not print the
tokens it records.

## One-time tenant setup

Do this once in the [Entra admin center](https://entra.microsoft.com). Copy
`entra-obo.env.example` to `.env.entra-obo` at the repo root (gitignored) and
fill in each value as you go.

### 1. Backend API app

The downstream API that the exchanged token is for.

1. **App registrations → New registration.** Name it e.g.
   `ToolHive OBO e2e backend`, single tenant, no redirect URI.
2. **Expose an API.** Set the Application ID URI to the default
   `api://<backend client ID>` and add a scope, e.g. `backend.access`, that
   admins and users can consent to.
3. **Manifest.** Set `"requestedAccessTokenVersion": 2` under `api` (the
   legacy manifest calls it `accessTokenAcceptedVersion`). Without it, the
   exchanged token carries the v1 issuer `https://sts.windows.net/<tid>/` and
   the suite rejects it.
4. Set `TOOLHIVE_E2E_ENTRA_BACKEND_AUDIENCE` to the backend's client ID and
   `TOOLHIVE_E2E_ENTRA_BACKEND_SCOPE` to `api://<backend client ID>/.default`.

### 2. Middle-tier app

The confidential client ToolHive uses for the exchange. User tokens are issued
for this app.

1. **New registration.** Name it e.g. `ToolHive OBO e2e proxy`, single tenant.
   Record its client ID as `TOOLHIVE_E2E_ENTRA_PROXY_CLIENT_ID` and the tenant
   GUID as `TOOLHIVE_E2E_ENTRA_TENANT_ID`.
2. **Certificates & secrets → New client secret.** Record the secret
   **value** (not its ID) as `TOOLHIVE_E2E_ENTRA_PROXY_CLIENT_SECRET`.
3. **Expose an API.** Set the Application ID URI to `api://<proxy client ID>`
   and add a scope, e.g. `proxy.access`.
4. **Expose an API → Add a client application.** Add the Azure CLI's client
   ID, `04b07795-8ddb-461a-bbee-02f9e1bf7b46`, and authorize the scope from
   step 3. This lets `az account get-access-token` mint a user token for this
   app with no consent prompt.
5. **Manifest.** Set `"requestedAccessTokenVersion": 2`.
6. **API permissions → Add → My APIs →** the backend app → `backend.access`,
   then **Grant admin consent**. Without consent, the exchange fails with
   AADSTS65001.

### 3. Test user

Use a cloud-only test account with no directory roles. MFA can stay on: you
sign in interactively once with `az login`, and the suite reuses that session.

### 4. Optional negative cases

- **Unconsented scope.** Expose a second scope on the backend app (e.g.
  `unconsented.scope`) and do **not** add it to the middle-tier app's API
  permissions. Set `TOOLHIVE_E2E_ENTRA_UNCONSENTED_SCOPE` to
  `api://<backend client ID>/unconsented.scope`.
- **Conditional access.** Needs Entra ID P1. Create a third API app and a
  conditional-access policy that targets it and requires something the test
  user cannot satisfy non-interactively, e.g. sign-in frequency "every time".
  Grant the middle-tier app permission on it, and set
  `TOOLHIVE_E2E_ENTRA_CA_SCOPE` to its `.default` scope.

## Running it

```bash
# Once per session: sign in as the test user. MFA is answered here.
az login --tenant <tenant GUID> --allow-no-subscriptions

# Kind cluster with the locally built operator (skip if it is already up).
task kind-setup-e2e
task operator-install-crds
task operator-deploy-local

# Run the suite. Reads .env.entra-obo from the repo root if it exists.
task thv-operator-e2e-test-entra

task kind-destroy
```

To use a token from somewhere else, set `TOOLHIVE_E2E_ENTRA_USER_TOKEN` to an
access token for the middle-tier app. The Azure CLI is then not needed. Access
tokens last about an hour.

Every resource is created in the `entra-obo-e2e` namespace, which is deleted
when the suite ends. To rerun after an aborted run, delete it first:
`kubectl --kubeconfig kconfig.yaml delete namespace entra-obo-e2e`.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `verifying user token ... id token issued by a different provider` | The middle-tier app issues v1 tokens. Set `requestedAccessTokenVersion: 2` (step 2.5). |
| `verifying exchanged backend token ... issued by a different provider` | The same for the backend app (step 1.3). |
| `discovering https://login.microsoftonline.com/...` fails | `TOOLHIVE_E2E_ENTRA_TENANT_ID` is a domain rather than the tenant GUID. |
| `az` reports AADSTS65001 | The Azure CLI is not pre-authorized on the middle-tier API (step 2.4). |
| 502 instead of a tool result | Entra refused the client (a wrong secret, AADSTS7000215) or could not be reached. The proxyrunner pod log names the Entra error. |
| 401 instead of a tool result | Entra refused the grant, most often because admin consent is missing on the backend scope (AADSTS65001, step 2.6). |

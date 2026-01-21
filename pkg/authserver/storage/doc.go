// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

/*
Package storage provides storage interfaces and implementations for the OAuth
authorization server. This package implements fosite's storage interfaces to
persist OAuth tokens and related data.

# Fosite Storage Architecture

Fosite uses Interface Segregation Principle to split storage into focused interfaces.
Each OAuth feature (authorization codes, access tokens, refresh tokens, PKCE) has its
own storage interface. This design allows:

  - Feature composition: Enable only the OAuth features you need
  - Testing isolation: Mock only the interfaces relevant to your test
  - Clear contracts: Each interface documents exactly what it requires

The main fosite storage interfaces we implement:

  - oauth2.AuthorizeCodeStorage: Authorization code grant (RFC 6749 Section 4.1)
  - oauth2.AccessTokenStorage: Access token persistence
  - oauth2.RefreshTokenStorage: Refresh token persistence
  - oauth2.TokenRevocationStorage: Token revocation (RFC 7009)
  - pkce.PKCERequestStorage: PKCE challenge storage (RFC 7636)
  - fosite.ClientManager: OAuth client lookup and JWT assertion tracking

# fosite.Requester: The Central Type

fosite.Requester is the core abstraction representing an OAuth request context. All
token storage methods store the full Requester, not just the token value, because:

  - Context preservation: Token validation requires the original request context
    (client, scopes, audience, session) to make authorization decisions
  - Introspection support: RFC 7662 token introspection returns metadata about the
    token (client_id, scope, exp, etc.) which lives in the Requester
  - Revocation support: Revoking by request ID requires finding all tokens from
    the same authorization grant, which means storing the grant context
  - Session data: The embedded Session contains expiration times per token type,
    subject, username, and custom claims needed for token generation

A Requester contains:

  - ID: Unique identifier for the authorization grant (request ID)
  - Client: The OAuth client that initiated the request
  - RequestedScopes/GrantedScopes: What scopes were requested and granted
  - RequestedAudience/GrantedAudience: What audiences were requested and granted
  - Session: Token expiration times, subject, and custom data
  - Form: Original request form values (sanitized for storage)

# Signature vs Request ID: Two Lookup Keys

Storage methods use two different keys for different operations:

Signature (token-specific operations):

  - Used by: CreateAccessTokenSession, GetAccessTokenSession, DeleteAccessTokenSession
  - What it is: A cryptographic signature or hash derived from the token value
  - Purpose: Look up a specific token when you have the token value
  - Example flow: Client sends access token -> derive signature -> look up session

Request ID (grant-wide operations):

  - Used by: RevokeAccessToken, RevokeRefreshToken, RotateRefreshToken
  - What it is: The unique identifier of the original authorization grant
  - Purpose: Find ALL tokens issued from the same authorization grant
  - Example flow: Revoke refresh token -> find request ID -> delete all related tokens

Why two keys? RFC 7009 requires that revoking a refresh token SHOULD also revoke
associated access tokens. This requires finding tokens by their common origin (request ID)
rather than by their individual values. The request ID ties together:

  - The authorization code (one-time use)
  - All access tokens issued from that grant
  - All refresh tokens issued from that grant

Our implementation stores tokens keyed by signature for O(1) token lookup, but
revocation requires O(n) scan by request ID. Production implementations often
maintain a reverse index (request_id -> signatures) for efficient revocation.

# fosite.Session: Token Metadata Container

fosite.Session is an interface for storing session data between OAuth requests.
Key design points:

Why GetExpiresAt lives on Session:

  - Different token types have different lifetimes (access: hours, refresh: days)
  - Expiration is metadata ABOUT the token, not the token itself
  - Session is the natural place for token metadata
  - Usage: session.GetExpiresAt(fosite.AccessToken) vs session.GetExpiresAt(fosite.RefreshToken)

Session vs Requester:

  - Session: Token-specific metadata (expiration, subject, username, claims)
  - Requester: Full request context including Session, Client, scopes, etc.
  - Session is embedded in Requester: requester.GetSession() returns the Session

Our session.Session type extends fosite's oauth2.JWTSession to add:

  - UpstreamSessionID: Links to tokens from our upstream IDP
  - JWT claims: Custom claims like "tsid" for token session lookup

# fosite.Client vs fosite.Requester

Client and Requester serve different roles in the OAuth lifecycle:

fosite.Client represents the registered OAuth application:

  - Static data: client_id, client_secret, redirect_uris, allowed scopes/grants
  - Loaded from ClientRegistry (our extension) or fosite.ClientManager
  - Used to validate incoming requests against client configuration

fosite.Requester represents a specific authorization request:

  - Dynamic data: specific scopes requested/granted, session, form values
  - Created during authorization, stored with tokens
  - Contains a reference to Client via GetClient()

The relationship:

	Client (static config) <--- Requester (instance) ---> Session (token metadata)
	      |                           |                         |
	   "What can this app do?"   "What did this request grant?"   "When does it expire?"

When to use each:

  - GetClient: Validate client_id/secret, check allowed scopes/redirects
  - Requester: Issue tokens, check what was actually granted, introspect tokens

# Get Methods Accept Session Parameter

Methods like GetAccessTokenSession(ctx, signature, session) accept a Session parameter.
This session is a "prototype" that may be used for deserialization:

  - Some storage backends serialize the full Requester (including Session)
  - On retrieval, they need a session instance to deserialize into
  - The prototype provides the concrete type for JSON/gob deserialization
  - If your storage keeps Requesters in memory, this parameter may be unused

Our in-memory implementation ignores this parameter since we store live Requester
objects. Persistent backends (SQL, Redis) would use it for deserialization.

# ToolHive Extensions

Beyond fosite's interfaces, we add ToolHive-specific storage:

  - UpstreamTokenStorage: Store tokens from upstream IDPs for proxy token swap
  - PendingAuthorizationStorage: Track in-flight authorizations during IDP redirect
  - ClientRegistry: Dynamic client registration (RFC 7591) via RegisterClient

These integrate with fosite's token storage to provide end-to-end OAuth proxy
functionality: store upstream tokens, link them to issued tokens via session IDs,
and enable transparent token swap for backend requests.

# Implementation Notes

Thread safety: MemoryStorage uses sync.RWMutex for all map access. Persistent
backends should use appropriate transaction isolation.

Expiration: We use timedEntry wrapper to track creation and expiration times.
A background goroutine periodically cleans expired entries. Production backends
might use database TTL features or scheduled jobs.

Defensive copies: Store and retrieve methods make deep copies to prevent aliasing
issues where callers might modify returned data.

Error mapping: Storage errors are wrapped with both our sentinel errors (ErrNotFound,
ErrExpired) and fosite errors (fosite.ErrNotFound) for compatibility with fosite's
error handling.

# References

  - RFC 6749: OAuth 2.0 Authorization Framework
  - RFC 7009: OAuth 2.0 Token Revocation
  - RFC 7636: Proof Key for Code Exchange (PKCE)
  - RFC 7591: OAuth 2.0 Dynamic Client Registration
  - RFC 7662: OAuth 2.0 Token Introspection
  - Fosite documentation: https://github.com/ory/fosite
*/
package storage

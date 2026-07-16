// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import "fmt"

// Key type constants for Redis storage.
// These define the different types of data stored in Redis.
const (
	// KeyTypeAccess is the key type for access tokens.
	KeyTypeAccess = "access"

	// KeyTypeRefresh is the key type for refresh tokens.
	KeyTypeRefresh = "refresh"

	// KeyTypeAuthCode is the key type for authorization codes.
	KeyTypeAuthCode = "authcode"

	// KeyTypePKCE is the key type for PKCE requests.
	KeyTypePKCE = "pkce"

	// KeyTypeClient is the key type for OAuth clients.
	KeyTypeClient = "client"

	// KeyTypeUser is the key type for users.
	KeyTypeUser = "user"

	// KeyTypeProvider is the key type for provider identities.
	KeyTypeProvider = "provider"

	// KeyTypeUpstream is the key type for upstream tokens.
	KeyTypeUpstream = "upstream"

	// KeyTypePending is the key type for pending authorizations.
	KeyTypePending = "pending"

	// KeyTypeInvalidated is the key type for invalidated authorization codes.
	KeyTypeInvalidated = "invalidated"

	// KeyTypeJWT is the key type for client assertion JWTs.
	KeyTypeJWT = "jwt"

	// KeyTypeReqIDAccess is the key type for request ID to access token mappings.
	KeyTypeReqIDAccess = "reqid:access"

	// KeyTypeReqIDRefresh is the key type for request ID to refresh token mappings.
	KeyTypeReqIDRefresh = "reqid:refresh"

	// KeyTypeUpstreamIdx is the key type for the session index set — a Redis SET that
	// tracks all per-provider token keys (upstream:{sid}:{provider}) belonging to a session.
	// This enables O(1) enumeration via SMEMBERS without scanning the keyspace.
	// Used by GetAllUpstreamTokens (bulk read) and DeleteUpstreamTokens (bulk delete).
	KeyTypeUpstreamIdx = "upstream:idx"

	// KeyTypeUserUpstream is the key type for user to upstream token reverse lookups.
	KeyTypeUserUpstream = "user:upstream"

	// KeyTypeUserProviders is the key type for user to provider identity reverse lookups.
	KeyTypeUserProviders = "user:providers"

	// KeyTypeDCR is the key type for RFC 7591 Dynamic Client Registration credentials
	// persisted by an authserver upstream-DCR resolver. Distinct from KeyTypeClient,
	// which holds the authserver's *own* OAuth clients — DCR entries are credentials
	// that *this* authserver registered against an *upstream* authorization server.
	KeyTypeDCR = "dcr"
)

// DeriveKeyPrefix creates the key prefix from the Kubernetes namespace and MCP server name.
// The format is "thv:auth:{ns:name}:" where {ns:name} is a Redis hash tag.
//
// Note: The hash tag format {ns:name} intentionally combines namespace and name
// into a single tag. In Redis Cluster, only the first hash tag determines slot
// assignment. Using {ns}:{name} would only hash on namespace, potentially
// spreading a single server's keys across multiple slots. The combined format
// ensures all keys for a specific server (namespace+name pair) are placed in
// the same slot, enabling atomic multi-key operations like token revocation.
func DeriveKeyPrefix(namespace, name string) string {
	return fmt.Sprintf("thv:auth:{%s:%s}:", namespace, name)
}

// redisKey generates a Redis key with the given prefix, type, and ID.
// The resulting format is "{prefix}{keyType}:{id}". This assumes the id does not
// contain colons; callers that need colon-safe keys should use redisProviderKey
// which uses a length-prefixed format. In practice, IDs passed here are UUIDs,
// opaque token signatures, or system-generated identifiers that do not contain colons.
func redisKey(prefix, keyType, id string) string {
	return fmt.Sprintf("%s%s:%s", prefix, keyType, id)
}

// redisProviderKey generates a Redis key for provider identities.
// Uses length-prefixed format to handle colons in provider IDs/subjects.
func redisProviderKey(prefix, providerID, providerSubject string) string {
	return fmt.Sprintf("%s%s:%d:%s:%s", prefix, KeyTypeProvider, len(providerID), providerID, providerSubject)
}

// redisDCRKey generates a Redis key for a DCR credential entry, identifying the
// (Issuer, UpstreamID, RedirectURI, ScopesHash) tuple that DCRKey canonicalises.
//
// Format: "{prefix}dcr:<len(issuer)>:<issuer>:<len(upstream_id)>:<upstream_id>:<len(redirect_uri)>:<redirect_uri>:<scopes_hash>"
//
// The first three segments are length-prefixed to handle colons in UpstreamID
// and RedirectURI (and, for symmetry, Issuer) without ambiguity, mirroring
// redisProviderKey. ScopesHash is expected to be a SHA-256 hex digest produced
// by storage.ScopesHash — only [0-9a-f] and never colon-bearing — so it is
// appended without a length prefix. The format is robust for that domain;
// validateDCRCredentialsForStore (called by every Store path) already
// rejects an empty ScopesHash, and callers are required to compute the hash
// via storage.ScopesHash. Length-prefix collision-safety is preserved on
// the leading segments either way.
//
// The UpstreamID segment (issue #5823) means two upstreams that share the
// consumer's Issuer and RedirectURI but differ only by their authorization
// server no longer map to the same Redis key. Adding it changes the key
// format, so entries written by an older binary (without the segment) will
// miss on lookup and be harmlessly re-registered under the new key. Orphaned
// old rows self-evict only when the upstream asserted an RFC 7591 §3.2.1
// client_secret_expires_at (StoreDCRCredentials sets a matching Redis TTL for
// those); entries for non-expiring secrets — the common §3.2.1 "never" case —
// carry no TTL and persist indefinitely, so they require manual cleanup.
// There is no automated one-shot migration for this key-format change today.
//
// The public-vs-confidential client distinction is intentionally NOT
// encoded here — see DCRKey's doc for the rationale. Today's two consumers
// register on disjoint RedirectURI address spaces (AS-origin vs RFC 8252
// loopback) so the persisted key cannot collide across profiles. A future
// consumer that defaults its RedirectURI into either space would need to
// add the distinguishing component back to the key format alongside an
// explicit migration story for existing Redis-cached entries.
func redisDCRKey(prefix string, key DCRKey) string {
	return fmt.Sprintf("%s%s:%d:%s:%d:%s:%d:%s:%s",
		prefix, KeyTypeDCR,
		len(key.Issuer), key.Issuer,
		len(key.UpstreamID), key.UpstreamID,
		len(key.RedirectURI), key.RedirectURI,
		key.ScopesHash)
}

// redisUpstreamKey generates a Redis key for a per-provider upstream token entry.
// Format: "{prefix}upstream:{sessionID}:{providerName}"
// This enables storing tokens from multiple upstream providers per session.
func redisUpstreamKey(prefix, sessionID, providerName string) string {
	return fmt.Sprintf("%s%s:%s:%s", prefix, KeyTypeUpstream, sessionID, providerName)
}

// redisSetKey generates a Redis key for a set that tracks multiple items.
// Used for secondary indexes like request ID -> token signature mappings.
// Same colon assumption as redisKey: the id must not contain colons.
func redisSetKey(prefix, keyType, id string) string {
	return fmt.Sprintf("%s%s:%s", prefix, keyType, id)
}

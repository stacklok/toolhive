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
)

// DeriveKeyPrefix creates the key prefix from server namespace and name.
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
func redisKey(prefix, keyType, id string) string {
	return fmt.Sprintf("%s%s:%s", prefix, keyType, id)
}

// redisProviderKey generates a Redis key for provider identities.
// Uses length-prefixed format to handle colons in provider IDs/subjects.
func redisProviderKey(prefix, providerID, providerSubject string) string {
	return fmt.Sprintf("%s%s:%d:%s:%s", prefix, KeyTypeProvider, len(providerID), providerID, providerSubject)
}

// redisSetKey generates a Redis key for a set that tracks multiple items.
// Used for secondary indexes like request ID -> token signature mappings.
func redisSetKey(prefix, keyType, id string) string {
	return fmt.Sprintf("%s%s:%s", prefix, keyType, id)
}

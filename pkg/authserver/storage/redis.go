// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"

	tcredis "github.com/stacklok/toolhive-core/redis"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// nullMarker is used to store nil upstream tokens in Redis.
const nullMarker = "null"

// pastExpiryDCRTTL is the bounded TTL applied when a caller writes DCR
// credentials whose ClientSecretExpiresAt is already in the past. The row is
// still accepted (the resolver decides when to re-register) but it self-evicts
// almost immediately so the store does not hold an already-expired secret
// forever. One second is small enough that no caller can usefully read the
// row, large enough that real Redis applies the TTL reliably (sub-second TTLs
// are technically supported via PEXPIRE but the second-grain TTL command is
// the broadly-tested path), and short enough that operational metrics do not
// confuse this row with a healthy long-lived registration.
const pastExpiryDCRTTL = time.Second

// maxDCRClaimRetries bounds StoreDCRCredentialsIfAbsent's WATCH/MULTI retry
// loop. go-redis does not retry Watch internally: a concurrent write to the
// watched key (another replica claiming, refreshing, or evicting the same
// row) aborts the pipelined EXEC with redis.TxFailedErr, which Watch returns
// to the caller unwrapped. Retrying a small, fixed number of times lets a
// real concurrent write during the exact race this method exists to close
// resolve on its own rather than failing the caller with a spurious error.
// Mirrors maxConfiguredClientReconcileRetries above for the same reason.
const maxDCRClaimRetries = 3

// warnOnCleanupErr logs a warning when a best-effort cleanup operation fails.
//
// Secondary index cleanup in Redis (SRem from reverse-lookup sets, Del of orphaned
// keys) is intentionally best-effort: the primary operation has already succeeded,
// and failing the overall request due to an index cleanup error would be worse than
// leaving a stale entry. Stale index entries are bounded by TTL expiration of the
// underlying keys and are tolerated on read (readers skip missing entries).
//
// TODO: Add a periodic background cleanup job to scan and remove stale entries
// from secondary index sets (KeyTypeReqIDAccess, KeyTypeReqIDRefresh,
// KeyTypeUserUpstream, KeyTypeUserProviders) to prevent unbounded growth.
func warnOnCleanupErr(err error, operation, key string) {
	if err != nil {
		slog.Warn("best-effort index cleanup failed",
			"operation", operation, "key", key, "error", err)
	}
}

// filterIndexMembersByPrefix splits SMEMBERS results into members that share
// this storage instance's key prefix and those that do not. Multi-key
// operations like MGet and Del fail with CROSSSLOT under Redis Cluster when
// any member hashes to a different slot, and a stray un-prefixed entry
// (legacy data, an external admin op, a test fixture) is the most likely
// source of such drift. Filtering at read time turns a hard cluster failure
// into a logged warning, while remaining a no-op on standalone Redis.
func filterIndexMembersByPrefix(prefix string, members []string) (kept, dropped []string) {
	for _, m := range members {
		if strings.HasPrefix(m, prefix) {
			kept = append(kept, m)
		} else {
			dropped = append(dropped, m)
		}
	}
	return kept, dropped
}

// warnDroppedIndexMembers emits a warning for each foreign member returned by
// filterIndexMembersByPrefix so operators can identify the source of cluster-
// incompatible entries.
func warnDroppedIndexMembers(operation, indexKey, expectedPrefix string, dropped []string) {
	for _, m := range dropped {
		slog.Warn("dropping foreign index member to prevent CROSSSLOT",
			"operation", operation, "indexKey", indexKey,
			"expectedPrefix", expectedPrefix, "member", m)
	}
}

// RedisStorage implements the Storage interface backed by Redis.
// Supports standalone mode (single endpoint), Sentinel failover mode, and
// Cluster mode. It provides distributed storage for OAuth2 tokens, authorization
// codes, user data, and pending authorizations, enabling horizontal scaling.
type RedisStorage struct {
	client    redis.UniversalClient
	keyPrefix string
}

// storedSession is a serializable wrapper for fosite.Requester.
// This allows storing fosite sessions in Redis as JSON.
type storedSession struct {
	ClientID          string              `json:"client_id"`
	RequestedAt       time.Time           `json:"requested_at"`
	RequestedScopes   []string            `json:"requested_scopes"`
	GrantedScopes     []string            `json:"granted_scopes"`
	RequestedAudience []string            `json:"requested_audience"`
	GrantedAudience   []string            `json:"granted_audience"`
	Form              map[string][]string `json:"form"`
	RequestID         string              `json:"request_id"`
	Session           json.RawMessage     `json:"session"`
}

// NewRedisStorage creates Redis-backed storage. Connection-mode topology,
// timeouts, TLS, and credentials are configured through cfg; keyPrefix is the
// per-tenant key prefix (a single Redis Cluster hash tag combining namespace
// and name — see DeriveKeyPrefix, e.g. "thv:auth:{ns:name}:") and must be
// non-empty.
//
// Connection-mode validation, timeout defaults, client construction (standalone,
// cluster, or sentinel), TLS plumbing, and connectivity verification are
// delegated to the shared toolhive-core redis package. cfg.Password may be
// empty when the Redis server does not require authentication (the auth server
// does not mandate ACL auth); the keyPrefix is storage-specific and required.
func NewRedisStorage(ctx context.Context, cfg tcredis.Config, keyPrefix string) (*RedisStorage, error) {
	if keyPrefix == "" {
		return nil, errors.New("invalid redis configuration: key prefix is required")
	}

	client, err := tcredis.NewClient(ctx, &cfg)
	if err != nil {
		return nil, err
	}

	return &RedisStorage{
		client:    client,
		keyPrefix: keyPrefix,
	}, nil
}

// NewRedisStorageWithClient creates a RedisStorage with a pre-configured client.
// This is useful for testing with miniredis.
func NewRedisStorageWithClient(client redis.UniversalClient, keyPrefix string) *RedisStorage {
	return &RedisStorage{
		client:    client,
		keyPrefix: keyPrefix,
	}
}

// defaultSessionFactory creates session prototypes for deserialization.
// Name and email are empty because they are preserved in the JWT Extra map
// from the serialized session data during deserialization.
func defaultSessionFactory(subject, idpSessionID, clientID string) fosite.Session {
	return session.New(subject, idpSessionID, clientID, session.UserClaims{})
}

// Health checks Redis connectivity.
func (s *RedisStorage) Health(ctx context.Context) error {
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis health check failed: %w", err)
	}
	return nil
}

// Close closes the Redis client connection.
func (s *RedisStorage) Close() error {
	return s.client.Close()
}

// -----------------------
// ClientRegistry
// -----------------------

// storedClient is a serializable wrapper for OAuth clients.
type storedClient struct {
	ID            string   `json:"id"`
	Secret        []byte   `json:"secret,omitempty"` //nolint:gosec // G117: field legitimately holds sensitive data
	RedirectURIs  []string `json:"redirect_uris"`
	GrantTypes    []string `json:"grant_types"`
	ResponseTypes []string `json:"response_types"`
	Scopes        []string `json:"scopes"`
	Audience      []string `json:"audience"`
	Public        bool     `json:"public"`
	// TokenEndpointAuthMethod is the auth method registered for the client
	// ("none", "client_secret_basic", "client_secret_post"). Empty means the
	// row predates confidential-client support; see GetClient for how legacy
	// rows are interpreted.
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method,omitempty"`
	// DCRIssued is true when the row was written from a dynamically registered
	// client. It is persisted rather than inferred from the auth method: a
	// pre-provisioned confidential client also carries a method, and inferring
	// from it would hand that permanent client an anti-bloat TTL on its first
	// token request. Rows predating this field unmarshal with DCRIssued=false.
	// DCR mints a new client_id per registration and never rewrites an existing
	// row, so a pre-existing row with no DCRIssued value is itself DCR-issued —
	// it just reads back unmarked. clientFromStored compensates for this on
	// read using the Redis key's TTL as a discriminator: a legacy row with no
	// auth method, Public=true, AND a live TTL on its key is treated as
	// DCR-issued even though the persisted field says otherwise, because
	// RegisterClient only ever wrote that TTL for DCR-issued rows — a
	// pre-provisioned public client (a bare *fosite.DefaultClient) was always
	// written with no TTL. A legacy confidential row (no method, Public=false)
	// is not compensated for — it predates confidential DCR support entirely,
	// so it cannot be DCR-issued.
	DCRIssued bool `json:"dcr_issued,omitempty"`
	// Reserved is true when the row is a SPIFFE static-client durable
	// placeholder (see staticClientPlaceholder in spiffe_decorator.go) —
	// never a real, authenticatable client. It is checked before GrantTypes/
	// ResponseTypes/Secret/Public are trusted on read: clientFromStored
	// re-wraps a Reserved row in the same inert type the placeholder was
	// built with, regardless of what those other fields happen to contain,
	// so the row cannot become issuable via fosite's own zero-value
	// defaulting (see clientFromStored and inertPlaceholderClient).
	Reserved bool `json:"reserved,omitempty"`
	// JSONWebKeys stores only the inline public keys used by private_key_jwt.
	// JSONWebKeysURI and other assertion material are intentionally not stored.
	JSONWebKeys                       *jose.JSONWebKeySet `json:"jwks,omitempty"`
	TokenEndpointAuthSigningAlgorithm string              `json:"token_endpoint_auth_signing_alg,omitempty"`
}

// publicJSONWebKeySet copies an inline JWKS while removing private and
// symmetric key material before it is persisted.
func publicJSONWebKeySet(jwks *jose.JSONWebKeySet) *jose.JSONWebKeySet {
	if jwks == nil {
		return nil
	}

	publicKeys := make([]jose.JSONWebKey, 0, len(jwks.Keys))
	for _, key := range jwks.Keys {
		publicKey := key
		if !key.IsPublic() {
			publicKey = key.Public()
		}
		if publicKey.Key != nil && publicKey.IsPublic() {
			publicKeys = append(publicKeys, publicKey)
		}
	}
	return &jose.JSONWebKeySet{Keys: publicKeys}
}

// clientFromStored rebuilds a fosite.Client from its persisted form. hasTTL
// reports whether the Redis key currently carries an expiry (TTL >= 0); it is
// the discriminator used to compensate for the legacy public shape below, and
// is meaningless for rows that carry an explicit DCRIssued marker.
//
// The read logic is two-way so that drift between the Public flag and the
// auth method can only ever *add* secret verification, never remove it:
//
//   - A row with no method recorded covers two populations, both rebuilt as a
//     bare *fosite.DefaultClient: a pre-provisioned non-OIDC client
//     (permanent — RegisterClient only populates the column for
//     fosite.OpenIDConnectClient implementations, so a non-OIDC client will
//     never carry one, by design, not by omission) and a row written before
//     confidential-client support existed (transitional). Neither shape was
//     ever assigned a pinned method — the public one no more than the
//     confidential one — so neither should acquire method enforcement on
//     read-back: fosite only enforces token_endpoint_auth_method on
//     fosite.OpenIDConnectClient implementations, and a bare
//     *fosite.DefaultClient does not satisfy that interface. IsPublic still
//     reports correctly because it reads the Public field directly
//     (fosite.DefaultClient.IsPublic() returns c.Public, uninfluenced by the
//     auth method).
//   - A row with a method was written by registration.New (the only path
//     that populates the column), so it is rebuilt as a
//     *fosite.DefaultOpenIDConnectClient — fosite enforces the pinned method
//     at the token endpoint. IsPublic is derived as
//     (Public && method == "none"): a row that somehow carries both
//     Public=true and a secret-based method reads back confidential, forcing
//     secret verification rather than dropping it.
//
// DCR-issued marking: a row is treated as DCR-issued when it was persisted as
// such (stored.DCRIssued — the primary signal, always honored regardless of
// TTL), OR when it has the legacy public shape (no method, Public=true) AND
// the key currently carries a TTL. That second clause exists because rows
// written before the DCRIssued field existed have neither field set, and the
// legacy public shape is ambiguous on its own: it matches both a pre-existing
// DCR-issued public client (which must keep renewing its TTL) and a
// pre-provisioned public client registered as a bare *fosite.DefaultClient
// (which must never acquire one — Redis EXPIRE on a key with no TTL creates
// one). The two are distinguishable by how RegisterClient originally wrote
// the key: DCR-issued rows always got DefaultDCRClientTTL, pre-provisioned
// rows always got none. Gating the legacy-shape compensation on "the key
// currently has a TTL" reproduces that distinction without needing to touch
// the stored JSON. A legacy confidential row (no method, Public=false) is
// never marked regardless of TTL: it predates confidential DCR support
// entirely and cannot be DCR-issued.
func clientFromStored(stored storedClient, hasTTL bool) fosite.Client {
	// A reserved SPIFFE placeholder is re-wrapped in the exact inert type it
	// was built with, ignoring whatever GrantTypes/ResponseTypes/Secret/
	// Public happen to be on the row: a bare *fosite.DefaultClient would
	// otherwise satisfy fosite's own zero-value defaulting for
	// GetGrantTypes/GetResponseTypes (["authorization_code"] / ["code"]) on
	// this exact reconstruction path, undoing the guarantee the placeholder
	// exists to provide. This check must run before the method-based branch
	// below: a reserved row never carries a TokenEndpointAuthMethod, but even
	// if it somehow did, Reserved must still win.
	if stored.Reserved {
		return inertPlaceholderClient{DefaultClient: &fosite.DefaultClient{
			ID:       stored.ID,
			Scopes:   stored.Scopes,
			Audience: stored.Audience,
			Public:   false,
		}}
	}

	method := stored.TokenEndpointAuthMethod
	if method == "" {
		client := &fosite.DefaultClient{
			ID:            stored.ID,
			Secret:        stored.Secret,
			RedirectURIs:  stored.RedirectURIs,
			GrantTypes:    stored.GrantTypes,
			ResponseTypes: stored.ResponseTypes,
			Scopes:        stored.Scopes,
			Audience:      stored.Audience,
			Public:        stored.Public,
		}
		if stored.DCRIssued || (stored.Public && hasTTL) {
			return registration.MarkDCRIssued(client)
		}
		return client
	}
	oidcClient := &fosite.DefaultOpenIDConnectClient{
		DefaultClient: &fosite.DefaultClient{
			ID:            stored.ID,
			Secret:        stored.Secret,
			RedirectURIs:  stored.RedirectURIs,
			GrantTypes:    stored.GrantTypes,
			ResponseTypes: stored.ResponseTypes,
			Scopes:        stored.Scopes,
			Audience:      stored.Audience,
			Public:        stored.Public && method == oauthproto.TokenEndpointAuthMethodNone,
		},
		TokenEndpointAuthMethod:           method,
		JSONWebKeys:                       stored.JSONWebKeys,
		TokenEndpointAuthSigningAlgorithm: stored.TokenEndpointAuthSigningAlgorithm,
	}
	if stored.DCRIssued {
		return registration.MarkDCRIssued(oidcClient)
	}
	return oidcClient
}

// buildStoredClient converts client to its serializable form. Record the
// registered auth method when the client exposes one. Clients that don't
// implement fosite.OpenIDConnectClient (e.g. pre-provisioned confidential
// clients built as bare *fosite.DefaultClient) leave the field empty on
// purpose: Public alone carries the meaning it already carries, and
// clientFromStored treats the empty method as a legacy row. Do NOT substitute
// a "none" fallback here — that would silently reclassify a confidential row
// as public on read-back.
func buildStoredClient(client fosite.Client) storedClient {
	stored := storedClient{
		ID:            client.GetID(),
		Secret:        client.GetHashedSecret(),
		RedirectURIs:  client.GetRedirectURIs(),
		GrantTypes:    client.GetGrantTypes(),
		ResponseTypes: client.GetResponseTypes(),
		Scopes:        client.GetScopes(),
		Audience:      client.GetAudience(),
		Public:        client.IsPublic(),
	}
	if oidcClient, ok := client.(fosite.OpenIDConnectClient); ok {
		stored.TokenEndpointAuthMethod = oidcClient.GetTokenEndpointAuthMethod()
		stored.JSONWebKeys = publicJSONWebKeySet(oidcClient.GetJSONWebKeys())
		stored.TokenEndpointAuthSigningAlgorithm = oidcClient.GetTokenEndpointAuthSigningAlgorithm()
	}
	stored.DCRIssued = registration.DCRIssued(client)
	stored.Reserved = isReservedPlaceholder(client)
	return stored
}

// RegisterClient creates a client in storage. Always create-only via SetNX:
// it atomically returns ErrAlreadyExists when a client with the same ID is
// already registered, regardless of the new client's origin. /oauth/register
// is unauthenticated, so this must never clobber an existing client;
// operator-declared clients reconcile through ReconcileConfiguredClient
// instead.
func (s *RedisStorage) RegisterClient(ctx context.Context, client fosite.Client) error {
	if err := ValidateRegisterableClientID(client.GetID()); err != nil {
		return err
	}

	key := redisKey(s.keyPrefix, KeyTypeClient, client.GetID())
	stored := buildStoredClient(client)

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal client: %w", err)
	}

	// DCR-issued clients (public and confidential) expire to prevent unbounded
	// growth from unauthenticated dynamic registration; RenewClientTTL refreshes
	// this on proven use so actively-used clients are not evicted. The TTL is
	// keyed on the registration.DCRIssued marker, not IsPublic: a confidential
	// client minted via DCR is exactly the registration an attacker would pile
	// up, while a pre-provisioned confidential client (no marker) must never
	// acquire a TTL — Redis EXPIRE on a key with no TTL creates one, so gating
	// on IsPublic would give an existing permanent client an expiry on its
	// first token request after upgrade.
	ttl := time.Duration(0)
	if registration.DCRIssued(client) {
		ttl = DefaultDCRClientTTL
	}

	created, err := s.client.SetNX(ctx, key, data, ttl).Result()
	if err != nil {
		return fmt.Errorf("failed to register client: %w", err)
	}
	if !created {
		return fmt.Errorf("%w: client %q", ErrAlreadyExists, client.GetID())
	}
	return nil
}

// UpsertDCRIssuedClient creates or replaces a DCR-issued client at
// client.GetID(). Unlike RegisterClient (create-only via SetNX), an existing
// row is replaced -- and its TTL refreshed to DefaultDCRClientTTL -- when the
// existing row is itself DCR-issued; it refuses with ErrAlreadyExists when the
// existing row is NOT DCR-issued, protecting a configured/SPIFFE-reconciled
// client from being clobbered. See the ClientRegistry interface doc for the
// full contract.
//
// The read-check-write sequence for an existing key runs inside a Redis
// WATCH/MULTI transaction, mirroring ReconcileConfiguredClient, so a
// concurrent writer cannot interleave between the DCR-issued check and the
// write; see maxConfiguredClientReconcileRetries for why this method retries
// redis.TxFailedErr itself, up to the same bounded count. Unlike
// ReconcileConfiguredClient (ttl=0, permanent), the write here always uses
// DefaultDCRClientTTL: this row is TTL-bounded like any other DCR-issued
// client.
func (s *RedisStorage) UpsertDCRIssuedClient(ctx context.Context, client fosite.Client) error {
	if !registration.DCRIssued(client) {
		return fmt.Errorf("client %q must carry the DCR-issued marker to use UpsertDCRIssuedClient", client.GetID())
	}
	if err := ValidateRegisterableClientID(client.GetID()); err != nil {
		return err
	}

	key := redisKey(s.keyPrefix, KeyTypeClient, client.GetID())
	stored := buildStoredClient(client)
	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal client: %w", err)
	}

	setPipelined := func(tx *redis.Tx) error {
		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, data, DefaultDCRClientTTL)
			return nil
		})
		return err
	}

	txFn := func(tx *redis.Tx) error {
		existingData, getErr := tx.Get(ctx, key).Bytes()
		if errors.Is(getErr, redis.Nil) {
			return setPipelined(tx)
		}
		if getErr != nil {
			return fmt.Errorf("failed to get existing client: %w", getErr)
		}

		ttl, ttlErr := tx.TTL(ctx, key).Result()
		if ttlErr != nil {
			return fmt.Errorf("failed to get existing client TTL: %w", ttlErr)
		}

		var existingStored storedClient
		if unmarshalErr := json.Unmarshal(existingData, &existingStored); unmarshalErr != nil {
			return fmt.Errorf("failed to unmarshal existing client: %w", unmarshalErr)
		}

		if !registration.DCRIssued(clientFromStored(existingStored, ttl >= 0)) {
			return fmt.Errorf("%w: client %q", ErrAlreadyExists, client.GetID())
		}

		return setPipelined(tx)
	}

	var watchErr error
	for attempt := 0; attempt < maxConfiguredClientReconcileRetries; attempt++ {
		watchErr = s.client.Watch(ctx, txFn, key)
		if !errors.Is(watchErr, redis.TxFailedErr) {
			return watchErr
		}
	}
	return watchErr
}

// storedClientFingerprintsEqual mirrors clientFingerprintsEqual but compares
// the raw serialized fields directly rather than through the fosite.Client
// interface returned by clientFromStored. This matters because
// fosite.DefaultClient.GetGrantTypes and GetResponseTypes each silently
// substitute a single-element default (["authorization_code"] / ["code"])
// whenever the underlying field is empty (see clientFromStored) — comparing
// through the reconstructed client would report a false mismatch between
// two rows that both deliberately carry no grant/response types, such as
// the SPIFFE static-client placeholder in spiffe_decorator.go reconciling
// against itself. Comparing the stored bytes directly sidesteps that
// defaulting entirely.
func storedClientFingerprintsEqual(a, b storedClient) bool {
	return sameStringSet(a.Scopes, b.Scopes) &&
		sameStringSet(a.Audience, b.Audience) &&
		sameStringSet(a.GrantTypes, b.GrantTypes) &&
		sameStringSet(a.ResponseTypes, b.ResponseTypes) &&
		a.Public == b.Public
}

// maxConfiguredClientReconcileRetries bounds the retry loop
// ReconcileConfiguredClient runs around its WATCH/MULTI transaction. go-redis
// does not retry Watch internally: a concurrent write to the watched key aborts
// the pipelined EXEC with redis.TxFailedErr, which Watch returns to the caller
// unwrapped (see $GOMODCACHE/github.com/redis/go-redis/v9@*/tx.go). Retrying a
// small, fixed number of times lets a real concurrent write during the exact
// race this feature exists to close (see preflightDurableCollisions in
// spiffe_decorator.go) resolve on its own rather than failing the caller's
// startup path with a spurious error.
const maxConfiguredClientReconcileRetries = 5

// ReconcileConfiguredClient applies an operator-declared client: creates it
// if absent, idempotently replaces a matching-fingerprint configured client
// (the restart-with-unchanged-config case), or refuses with ErrAlreadyExists
// when the existing record is DCR-issued or a different configured client.
// See the ClientRegistry interface doc for the full contract.
//
// The read-check-write sequence for an existing key runs inside a Redis
// WATCH/MULTI transaction so a concurrent writer (e.g. a DCR registration on
// another replica racing this reconciliation) cannot interleave between the
// fingerprint check and the write. Redis aborts the transaction if the
// watched key changed concurrently, and go-redis surfaces that as
// redis.TxFailedErr rather than retrying it — see
// maxConfiguredClientReconcileRetries for why this method retries that error
// itself, up to a bounded count. A configured client is stored with ttl=0
// (operator-declared clients never expire), clearing any TTL the row might
// have inherited.
func (s *RedisStorage) ReconcileConfiguredClient(ctx context.Context, client fosite.Client) error {
	if registration.DCRIssued(client) {
		return fmt.Errorf("configured client %q must not carry the DCR-issued marker", client.GetID())
	}
	if err := ValidateRegisterableClientID(client.GetID()); err != nil {
		return err
	}

	key := redisKey(s.keyPrefix, KeyTypeClient, client.GetID())
	stored := buildStoredClient(client)
	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal client: %w", err)
	}

	setPipelined := func(tx *redis.Tx) error {
		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, data, 0)
			return nil
		})
		return err
	}

	txFn := func(tx *redis.Tx) error {
		existingData, getErr := tx.Get(ctx, key).Bytes()
		if errors.Is(getErr, redis.Nil) {
			return setPipelined(tx)
		}
		if getErr != nil {
			return fmt.Errorf("failed to get existing client: %w", getErr)
		}

		ttl, ttlErr := tx.TTL(ctx, key).Result()
		if ttlErr != nil {
			return fmt.Errorf("failed to get existing client TTL: %w", ttlErr)
		}

		var existingStored storedClient
		if unmarshalErr := json.Unmarshal(existingData, &existingStored); unmarshalErr != nil {
			return fmt.Errorf("failed to unmarshal existing client: %w", unmarshalErr)
		}

		// DCR-issued detection goes through the reconstructed client (not the
		// raw existingStored.DCRIssued field) so it inherits clientFromStored's
		// legacy-row compensation for rows written before the DCRIssued field
		// existed. The fingerprint comparison below deliberately does NOT go
		// through the reconstructed client — see storedClientFingerprintsEqual.
		if registration.DCRIssued(clientFromStored(existingStored, ttl >= 0)) {
			return fmt.Errorf("%w: client %q is DCR-issued, refusing to overwrite with a configured client",
				ErrAlreadyExists, client.GetID())
		}
		if !storedClientFingerprintsEqual(existingStored, stored) {
			return fmt.Errorf("%w: client %q is already registered as a different configured client",
				ErrAlreadyExists, client.GetID())
		}

		return setPipelined(tx)
	}

	var watchErr error
	for attempt := 0; attempt < maxConfiguredClientReconcileRetries; attempt++ {
		watchErr = s.client.Watch(ctx, txFn, key)
		if !errors.Is(watchErr, redis.TxFailedErr) {
			return watchErr
		}
	}
	return watchErr
}

// GetClient loads the client by its ID.
//
// The value and the key's TTL are fetched together in one pipelined round
// trip: clientFromStored needs the TTL to disambiguate the legacy public
// client shape (see DCRIssued and clientFromStored for why), and issuing GET
// then TTL as two separate round trips would leave a window where a
// concurrent RenewClientTTL or expiry changes the answer between them.
func (s *RedisStorage) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	key := redisKey(s.keyPrefix, KeyTypeClient, id)

	pipe := s.client.Pipeline()
	getCmd := pipe.Get(ctx, key)
	ttlCmd := pipe.TTL(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to get client: %w", err)
	}

	data, err := getCmd.Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Client not found"))
		}
		return nil, fmt.Errorf("failed to get client: %w", err)
	}

	// TTL returns -2 for a missing key (already excluded above by getCmd
	// succeeding) and -1 for a key with no expiry ("persist"); any value >= 0
	// means the key currently carries an expiry.
	ttl, err := ttlCmd.Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get client TTL: %w", err)
	}
	hasTTL := ttl >= 0

	var stored storedClient
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal client: %w", err)
	}

	return clientFromStored(stored, hasTTL), nil
}

// RenewClientTTL extends a DCR-issued client's registration TTL to DefaultDCRClientTTL.
//
// Call this on a proven-use signal — a successful token exchange/refresh — not on a
// client read. GetClient is reached from the unauthenticated front-channel
// /oauth/authorize handler before any authentication, and DCR-issued public clients
// have no secret, so renewing there would let any caller who knows a public client_id
// keep its row alive indefinitely and defeat the anti-bloat TTL. Renewing on token
// issuance ties registration survival to actual use.
//
// Only DCR-issued clients carry a TTL; pre-provisioned clients are stored without one
// and are left untouched. The gate is the registration.DCRIssued marker, not IsPublic:
// EXPIRE on a key with no TTL creates one, so keying on IsPublic would hand an
// existing permanent confidential client an expiry on its first token request after
// upgrade. EXPIRE on a missing key is a no-op, so a client whose row has already been
// evicted (or a non-persisted CIMD client) is safely ignored.
func (s *RedisStorage) RenewClientTTL(ctx context.Context, client fosite.Client) error {
	if client == nil || !registration.DCRIssued(client) {
		return nil
	}
	key := redisKey(s.keyPrefix, KeyTypeClient, client.GetID())
	return s.client.Expire(ctx, key, DefaultDCRClientTTL).Err()
}

// ClientAssertionJWTValid atomically reserves jti for a short window via
// Redis SET NX, returning fosite.ErrJTIKnown if it is already reserved or
// was previously consumed and not yet expired. This is the only atomic
// checkpoint in fosite's two-call client-assertion replay protocol (Valid,
// then SetClientAssertionJWT with the real expiry once the rest of the
// assertion has been validated) — an Exists-then-Set pair (the prior
// implementation) lets two concurrent requests presenting the same
// assertion both observe jti as unused before either records it. jti
// length is bounded here too, before it's ever sent to Redis, since fosite
// hands it to us before parsing exp.
func (s *RedisStorage) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	if len(jti) > server.MaxAssertionJTILength {
		return fmt.Errorf("jti exceeds maximum length of %d bytes", server.MaxAssertionJTILength)
	}

	key := redisKey(s.keyPrefix, KeyTypeJWT, jti)
	_, err := s.client.SetArgs(ctx, key, "1", redis.SetArgs{TTL: assertionReservationTTL, Mode: "NX"}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return fosite.ErrJTIKnown
		}
		return fmt.Errorf("failed to reserve JWT: %w", err)
	}
	return nil
}

// SetClientAssertionJWT extends jti's replay marker to its real expiry,
// rejecting an exp further than MaxAssertionLifespan in the future. This
// bounds how long a replay-tracking entry survives regardless of what the
// client's own assertion claims — otherwise a registered caller could set
// exp decades out and give Redis a key with a correspondingly enormous TTL.
// Unconditional overwrite, not a fresh atomic check: only the caller that
// already won ClientAssertionJWTValid's reservation reaches this call. If
// the JWT has already expired (exp is in the past), this deletes the
// reservation instead: there is no need to keep it for replay detection
// since the assertion will be rejected on expiry checks before reaching the
// JTI lookup.
func (s *RedisStorage) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	if time.Until(exp) > server.MaxAssertionLifespan {
		return fmt.Errorf("assertion lifetime exceeds maximum of %s", server.MaxAssertionLifespan)
	}

	key := redisKey(s.keyPrefix, KeyTypeJWT, jti)

	ttl := time.Until(exp)
	if ttl <= 0 {
		slog.Debug("removing reservation for already-expired client assertion JWT",
			"jti", jti, "exp", exp)
		return s.client.Del(ctx, key).Err()
	}

	return s.client.Set(ctx, key, "1", ttl).Err()
}

// ConsumeAssertionJWT atomically records an assertion JWT as consumed until its
// expiry. Reusing an unexpired (purpose, issuer, jti) tuple returns
// fosite.ErrJTIKnown. Redis errors are returned so callers fail closed rather
// than accepting an assertion whose replay status is unknown.
func (s *RedisStorage) ConsumeAssertionJWT(ctx context.Context, purpose, issuer, jti string, exp time.Time) error {
	ttl := time.Until(exp)
	if ttl <= 0 {
		return nil
	}

	key := redisAssertionJWTKey(s.keyPrefix, purpose, issuer, jti)
	_, err := s.client.SetArgs(ctx, key, "1", redis.SetArgs{TTL: ttl, Mode: "NX"}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return fosite.ErrJTIKnown
		}
		return fmt.Errorf("consume assertion JWT replay marker: %w", err)
	}
	return nil
}

// -----------------------
// oauth2.AuthorizeCodeStorage
// -----------------------

// CreateAuthorizeCodeSession stores the authorization request for a given authorization code.
func (s *RedisStorage) CreateAuthorizeCodeSession(ctx context.Context, code string, request fosite.Requester) error {
	if code == "" {
		return fosite.ErrInvalidRequest.WithHint("authorization code cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypeAuthCode, code)
	ttl := getTTLFromRequester(request, fosite.AuthorizeCode, DefaultAuthCodeTTL)

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	return s.client.Set(ctx, key, data, ttl).Err()
}

// GetAuthorizeCodeSession retrieves the authorization request for a given code.
// Matches memory.go's pattern: get the auth code data first, then check if
// invalidated. InvalidateAuthorizeCodeSession extends the auth code TTL to match
// the invalidation marker, so the data is always available when the marker exists.
func (s *RedisStorage) GetAuthorizeCodeSession(ctx context.Context, code string, _ fosite.Session) (fosite.Requester, error) {
	key := redisKey(s.keyPrefix, KeyTypeAuthCode, code)
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Authorization code not found"))
		}
		return nil, fmt.Errorf("failed to get authorization code: %w", err)
	}

	request, err := unmarshalRequester(ctx, data, s)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal request: %w", err)
	}

	// Check if the code has been invalidated.
	invalidatedKey := redisKey(s.keyPrefix, KeyTypeInvalidated, code)
	invalidated, err := s.client.Exists(ctx, invalidatedKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to check invalidation status: %w", err)
	}
	if invalidated > 0 {
		// Must return the request along with the error as per fosite documentation.
		// Fosite needs the Requester to extract the request ID for token revocation
		// during replay attack handling (RFC 6819).
		return request, fosite.ErrInvalidatedAuthorizeCode
	}

	return request, nil
}

// InvalidateAuthorizeCodeSession marks an authorization code as used/invalid.
// It extends the auth code key's TTL to match the invalidation marker, ensuring
// GetAuthorizeCodeSession can always return the Requester alongside
// ErrInvalidatedAuthorizeCode as required by fosite for token revocation.
func (s *RedisStorage) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	key := redisKey(s.keyPrefix, KeyTypeAuthCode, code)

	// Check if the code exists
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to check authorization code: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Authorization code not found"))
	}

	// Atomically: create invalidation marker and extend auth code TTL to match.
	// The auth code data must outlive the invalidation marker so that
	// GetAuthorizeCodeSession can return the Requester for replay detection.
	invalidatedKey := redisKey(s.keyPrefix, KeyTypeInvalidated, code)
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, invalidatedKey, "1", DefaultInvalidatedCodeTTL)
	pipe.Expire(ctx, key, DefaultInvalidatedCodeTTL)
	_, err = pipe.Exec(ctx)
	return err
}

// -----------------------
// oauth2.AccessTokenStorage
// -----------------------

// CreateAccessTokenSession stores the access token session.
func (s *RedisStorage) CreateAccessTokenSession(ctx context.Context, signature string, request fosite.Requester) error {
	if signature == "" {
		return fosite.ErrInvalidRequest.WithHint("access token signature cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypeAccess, signature)
	ttl := getTTLFromRequester(request, fosite.AccessToken, DefaultAccessTokenTTL)

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Store token, add to request ID index, and set index TTL atomically.
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, request.GetID())
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, key, data, ttl)
	pipe.SAdd(ctx, reqIDKey, signature)
	pipe.Expire(ctx, reqIDKey, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// GetAccessTokenSession retrieves the access token session by its signature.
func (s *RedisStorage) GetAccessTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	key := redisKey(s.keyPrefix, KeyTypeAccess, signature)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Access token not found"))
		}
		return nil, fmt.Errorf("failed to get access token: %w", err)
	}

	return unmarshalRequester(ctx, data, s)
}

// DeleteAccessTokenSession removes the access token session.
func (s *RedisStorage) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	key := redisKey(s.keyPrefix, KeyTypeAccess, signature)

	// Get the request first to find the request ID for cleaning up the index
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Access token not found"))
		}
		return fmt.Errorf("failed to get access token: %w", err)
	}

	// Delete the token
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete access token: %w", err)
	}

	// Best-effort secondary index cleanup (see warnOnCleanupErr).
	var stored storedSession
	if err := json.Unmarshal(data, &stored); err == nil && stored.RequestID != "" {
		reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, stored.RequestID)
		warnOnCleanupErr(s.client.SRem(ctx, reqIDKey, signature).Err(), "SRem", reqIDKey)
	}

	return nil
}

// -----------------------
// oauth2.RefreshTokenStorage
// -----------------------

// CreateRefreshTokenSession stores the refresh token session.
func (s *RedisStorage) CreateRefreshTokenSession(
	ctx context.Context, signature string, _ string, request fosite.Requester,
) error {
	if signature == "" {
		return fosite.ErrInvalidRequest.WithHint("refresh token signature cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypeRefresh, signature)
	ttl := getTTLFromRequester(request, fosite.RefreshToken, DefaultRefreshTokenTTL)

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Store token, add to request ID index, and set index TTL atomically.
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, request.GetID())
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, key, data, ttl)
	pipe.SAdd(ctx, reqIDKey, signature)
	pipe.Expire(ctx, reqIDKey, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// GetRefreshTokenSession retrieves the refresh token session by its signature.
func (s *RedisStorage) GetRefreshTokenSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	key := redisKey(s.keyPrefix, KeyTypeRefresh, signature)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Refresh token not found"))
		}
		return nil, fmt.Errorf("failed to get refresh token: %w", err)
	}

	return unmarshalRequester(ctx, data, s)
}

// DeleteRefreshTokenSession removes the refresh token session.
func (s *RedisStorage) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	key := redisKey(s.keyPrefix, KeyTypeRefresh, signature)

	// Get the request first to find the request ID for cleaning up the index
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Refresh token not found"))
		}
		return fmt.Errorf("failed to get refresh token: %w", err)
	}

	// Delete the token
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete refresh token: %w", err)
	}

	// Best-effort secondary index cleanup (see warnOnCleanupErr).
	var stored storedSession
	if err := json.Unmarshal(data, &stored); err == nil && stored.RequestID != "" {
		reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, stored.RequestID)
		warnOnCleanupErr(s.client.SRem(ctx, reqIDKey, signature).Err(), "SRem", reqIDKey)
	}

	return nil
}

// RotateRefreshToken invalidates a refresh token and all its related token data.
// This is a no-op if the token does not exist (returns nil), matching the behavior
// of the in-memory implementation. All cleanup operations are best-effort
// (see warnOnCleanupErr); the new refresh token has already been issued by fosite,
// so partial cleanup is acceptable.
func (s *RedisStorage) RotateRefreshToken(ctx context.Context, requestID string, refreshTokenSignature string) error {
	// Delete the specific refresh token. Del returns the number of keys removed;
	// 0 means the token did not exist (already rotated or never created).
	refreshKey := redisKey(s.keyPrefix, KeyTypeRefresh, refreshTokenSignature)
	deleted, err := s.client.Del(ctx, refreshKey).Result()
	if err != nil {
		warnOnCleanupErr(err, "Del", refreshKey)
	}
	if deleted == 0 {
		slog.Debug("refresh token not found during rotation, treating as no-op",
			"request_id", requestID, "signature", refreshTokenSignature)
	}

	// Remove from the request ID index
	reqIDRefreshKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, requestID)
	warnOnCleanupErr(s.client.SRem(ctx, reqIDRefreshKey, refreshTokenSignature).Err(), "SRem", reqIDRefreshKey)

	// Delete all access tokens associated with this request ID
	reqIDAccessKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, requestID)
	signatures, err := s.client.SMembers(ctx, reqIDAccessKey).Result()
	if err == nil {
		for _, sig := range signatures {
			accessKey := redisKey(s.keyPrefix, KeyTypeAccess, sig)
			warnOnCleanupErr(s.client.Del(ctx, accessKey).Err(), "Del", accessKey)
		}
		warnOnCleanupErr(s.client.Del(ctx, reqIDAccessKey).Err(), "Del", reqIDAccessKey)
	}

	return nil
}

// -----------------------
// oauth2.TokenRevocationStorage
// -----------------------

// RevokeAccessToken marks an access token as revoked by request ID.
func (s *RedisStorage) RevokeAccessToken(ctx context.Context, requestID string) error {
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDAccess, requestID)
	signatures, err := s.client.SMembers(ctx, reqIDKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("failed to get access token signatures: %w", err)
	}

	for _, sig := range signatures {
		accessKey := redisKey(s.keyPrefix, KeyTypeAccess, sig)
		warnOnCleanupErr(s.client.Del(ctx, accessKey).Err(), "Del", accessKey)
	}

	// Clean up the index
	warnOnCleanupErr(s.client.Del(ctx, reqIDKey).Err(), "Del", reqIDKey)

	return nil
}

// RevokeRefreshToken marks a refresh token as revoked by request ID.
func (s *RedisStorage) RevokeRefreshToken(ctx context.Context, requestID string) error {
	reqIDKey := redisSetKey(s.keyPrefix, KeyTypeReqIDRefresh, requestID)
	signatures, err := s.client.SMembers(ctx, reqIDKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("failed to get refresh token signatures: %w", err)
	}

	for _, sig := range signatures {
		refreshKey := redisKey(s.keyPrefix, KeyTypeRefresh, sig)
		warnOnCleanupErr(s.client.Del(ctx, refreshKey).Err(), "Del", refreshKey)
	}

	// Clean up the index
	warnOnCleanupErr(s.client.Del(ctx, reqIDKey).Err(), "Del", reqIDKey)

	return nil
}

// RevokeRefreshTokenMaybeGracePeriod marks a refresh token as revoked, optionally allowing
// a grace period. For this implementation, we revoke immediately.
func (s *RedisStorage) RevokeRefreshTokenMaybeGracePeriod(ctx context.Context, requestID string, _ string) error {
	return s.RevokeRefreshToken(ctx, requestID)
}

// -----------------------
// pkce.PKCERequestStorage
// -----------------------

// CreatePKCERequestSession stores the PKCE request session.
func (s *RedisStorage) CreatePKCERequestSession(ctx context.Context, signature string, request fosite.Requester) error {
	if signature == "" {
		return fosite.ErrInvalidRequest.WithHint("PKCE signature cannot be empty")
	}
	if request == nil {
		return fosite.ErrInvalidRequest.WithHint("request cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypePKCE, signature)
	ttl := getTTLFromRequester(request, fosite.AuthorizeCode, DefaultPKCETTL)

	data, err := marshalRequester(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	return s.client.Set(ctx, key, data, ttl).Err()
}

// GetPKCERequestSession retrieves the PKCE request session by its signature.
func (s *RedisStorage) GetPKCERequestSession(ctx context.Context, signature string, _ fosite.Session) (fosite.Requester, error) {
	key := redisKey(s.keyPrefix, KeyTypePKCE, signature)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("PKCE request not found"))
		}
		return nil, fmt.Errorf("failed to get PKCE request: %w", err)
	}

	return unmarshalRequester(ctx, data, s)
}

// DeletePKCERequestSession removes the PKCE request session.
func (s *RedisStorage) DeletePKCERequestSession(ctx context.Context, signature string) error {
	key := redisKey(s.keyPrefix, KeyTypePKCE, signature)

	result, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to delete PKCE request: %w", err)
	}
	if result == 0 {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("PKCE request not found"))
	}

	return nil
}

// -----------------------
// Upstream Token Storage
// -----------------------

// storedUpstreamTokens is a serializable wrapper for UpstreamTokens.
// Time fields use int64 Unix epoch; 0 is the sentinel meaning "not set".
// Neither time field uses omitempty so that 0 is always present and the
// read path can use a consistent != 0 check for both.
type storedUpstreamTokens struct {
	ProviderID       string `json:"provider_id"`
	AccessToken      string `json:"access_token"`  //nolint:gosec // G117: field legitimately holds sensitive data
	RefreshToken     string `json:"refresh_token"` //nolint:gosec // G117: field legitimately holds sensitive data
	IDToken          string `json:"id_token"`
	ExpiresAt        int64  `json:"expires_at"`
	SessionExpiresAt int64  `json:"session_expires_at"`
	UserID           string `json:"user_id"`
	UpstreamSubject  string `json:"upstream_subject"`
	ClientID         string `json:"client_id"`
}

// toUpstreamTokens converts the stored int64 epoch fields to time.Time and returns
// the populated UpstreamTokens. Zero epoch values are preserved as zero time.Time.
func (s *storedUpstreamTokens) toUpstreamTokens() *UpstreamTokens {
	var expiresAt time.Time
	if s.ExpiresAt != 0 {
		expiresAt = time.Unix(s.ExpiresAt, 0)
	}
	var sessionExpiresAt time.Time
	if s.SessionExpiresAt != 0 {
		sessionExpiresAt = time.Unix(s.SessionExpiresAt, 0)
	}
	return &UpstreamTokens{
		ProviderID:       s.ProviderID,
		AccessToken:      s.AccessToken,
		RefreshToken:     s.RefreshToken,
		IDToken:          s.IDToken,
		ExpiresAt:        expiresAt,
		SessionExpiresAt: sessionExpiresAt,
		UserID:           s.UserID,
		UpstreamSubject:  s.UpstreamSubject,
		ClientID:         s.ClientID,
	}
}

// storeUpstreamTokensScript atomically reads the existing UserID, writes new token
// data, updates the session index set, and updates user reverse-index sets.
// This prevents a race condition where concurrent writes for the same session
// could leave orphaned entries in user sets.
//
// KEYS[1] = per-provider token key (e.g. "thv:auth:{ns:name}:upstream:{sessionID}:{providerName}")
// KEYS[2] = session index set key (e.g. "thv:auth:{ns:name}:upstream:idx:{sessionID}")
// ARGV[1] = new token data (JSON or "null" marker)
// ARGV[2] = TTL in milliseconds
// ARGV[3] = new UserID ("" if no user)
// ARGV[4] = user upstream set key prefix (e.g. "thv:auth:{ns:name}:user:upstream:")
//
// Cluster slot invariant: this script reads oldUserID from KEYS[1] inside the
// script body (atomic with the rest of the work), so the user-set keys are
// constructed dynamically as `ARGV[4] .. userID` rather than being passed as
// declared KEYS. Every dynamically-built key MUST therefore inherit the
// `{ns:name}` hash tag that is baked into ARGV[4] (s.keyPrefix). All callers
// derive ARGV[4] from s.keyPrefix, which DeriveKeyPrefix builds with the
// `{ns:name}` hash tag, so user-set keys land on the same Redis Cluster slot
// as KEYS[1] and KEYS[2]. A future refactor that strips the hash tag — or
// rebuilds setPrefix from raw inputs without the tag — will silently pass on
// standalone Redis and fail with CROSSSLOT under Cluster.
var storeUpstreamTokensScript = redis.NewScript(`
local oldUserID = ""
local existing = redis.call('GET', KEYS[1])
if existing and existing ~= "null" then
    local ok, decoded = pcall(cjson.decode, existing)
    if ok and type(decoded) == "table" and decoded.user_id and decoded.user_id ~= "" then
        oldUserID = decoded.user_id
    end
end

local ttlMs = tonumber(ARGV[2])
if ttlMs > 0 then
    redis.call('SET', KEYS[1], ARGV[1], 'PX', ttlMs)
else
    redis.call('SET', KEYS[1], ARGV[1])
end

-- Maintain the session index set's TTL.
--
-- Invariant: the index set must outlive every per-provider key it points to.
--   * If ANY member is non-expiring (ttlMs == 0), the index set must also be
--     persistent. Otherwise the index evicts and we lose the ability to find
--     (and clean up) the per-provider key, leaking it forever.
--   * If ALL members are expiring, the index TTL must be at least the longest
--     member TTL.
--
-- Known trade-off: an in-place rewrite of the same (sessionID, providerName)
-- slot from non-expiring to expiring leaves the index PERSIST'd, even though
-- the rewritten member was the sole persistence anchor. Detecting this would
-- require tracking per-member TTL state in the index, which adds complexity.
-- We accept the trade-off because DeleteUpstreamTokens and session GC clean
-- the index up anyway. This behaviour is pinned by the test
-- "same provider rewrite from non-expiring to expiring keeps PERSIST'd until
-- rewrite" in redis_test.go — a future maintainer who tightens the rule will
-- see that test fail and can find the rationale here.
--
-- We discriminate two cases that look identical AFTER SADD (both have PTTL == -1):
--   1. A fresh set our SADD just created. We own the TTL decision unconditionally.
--   2. An existing set previously PERSIST'd by a non-expiring member. Must stay
--      persistent — applying a TTL here is the bug this script must avoid.
--
-- The trick: read EXISTS BEFORE SADD. SADD creates the set as a side-effect,
-- so post-SADD any "no TTL" state is ambiguous; pre-SADD it is not.
local idxExisted = redis.call('EXISTS', KEYS[2])
redis.call('SADD', KEYS[2], KEYS[1])

if ttlMs == 0 then
    -- This member never expires. Make the index persistent (no-op if already so).
    redis.call('PERSIST', KEYS[2])
elseif idxExisted == 0 then
    -- Fresh set; our SADD created it. Apply our TTL.
    redis.call('PEXPIRE', KEYS[2], ttlMs)
else
    -- Existing set; its TTL summarises prior members.
    local idxTTL = redis.call('PTTL', KEYS[2])
    if idxTTL == -1 then
        -- A previous non-expiring write PERSIST'd it. Leave it alone —
        -- applying a TTL here is the bug we are fixing.
    elseif idxTTL < ttlMs then
        -- Existing TTL is shorter than this member's. Extend.
        redis.call('PEXPIRE', KEYS[2], ttlMs)
    end
    -- else: idxTTL >= ttlMs, index already outlives this member.
end

local newUserID = ARGV[3]
local setPrefix = ARGV[4]

if oldUserID ~= "" and oldUserID ~= newUserID then
    redis.call('SREM', setPrefix .. oldUserID, KEYS[1])
end

if newUserID ~= "" then
    redis.call('SADD', setPrefix .. newUserID, KEYS[1])
end

return 1
`)

// marshalUpstreamTokensWithTTL marshals tokens and calculates TTL.
func marshalUpstreamTokensWithTTL(tokens *UpstreamTokens) ([]byte, time.Duration, error) {
	if tokens == nil {
		return []byte(nullMarker), DefaultAccessTokenTTL, nil
	}

	// Store 0 for zero time to use as a sentinel meaning "no expiry".
	// time.Time{}.Unix() returns -62135596800 which is not a useful sentinel.
	var expiresAtUnix int64
	if !tokens.ExpiresAt.IsZero() {
		expiresAtUnix = tokens.ExpiresAt.Unix()
	}

	var sessionExpiresAtUnix int64
	if !tokens.SessionExpiresAt.IsZero() {
		sessionExpiresAtUnix = tokens.SessionExpiresAt.Unix()
	}

	stored := storedUpstreamTokens{
		ProviderID:       tokens.ProviderID,
		AccessToken:      tokens.AccessToken,
		RefreshToken:     tokens.RefreshToken,
		IDToken:          tokens.IDToken,
		ExpiresAt:        expiresAtUnix,
		SessionExpiresAt: sessionExpiresAtUnix,
		UserID:           tokens.UserID,
		UpstreamSubject:  tokens.UpstreamSubject,
		ClientID:         tokens.ClientID,
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal upstream tokens: %w", err)
	}

	// Add DefaultRefreshTokenTTL beyond access token expiry so the refresh token
	// survives in storage for transparent token refresh by the middleware.
	// Zero ExpiresAt means the token never expires; return ttl=0 so the Lua script
	// stores the key without a Redis TTL.
	var ttl time.Duration // zero means no Redis TTL (non-expiring token with no known session bound)
	if !tokens.ExpiresAt.IsZero() {
		ttl = time.Until(tokens.ExpiresAt) + DefaultRefreshTokenTTL
		if ttl <= 0 {
			// Access token and its refresh grace period have both passed — evict promptly.
			ttl = time.Second
		}
	} else if !tokens.SessionExpiresAt.IsZero() {
		ttl = time.Until(tokens.SessionExpiresAt) + DefaultRefreshTokenTTL
		if ttl <= 0 {
			// Session bound and its refresh grace period have both passed — evict promptly.
			ttl = time.Second
		}
	}

	return data, ttl, nil
}

// StoreUpstreamTokens stores the upstream IDP tokens for a session and provider.
// Uses a Lua script to atomically write token data, update the session index set,
// and update user reverse-index sets, preventing race conditions on concurrent writes.
func (s *RedisStorage) StoreUpstreamTokens(ctx context.Context, sessionID, providerName string, tokens *UpstreamTokens) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	key := redisUpstreamKey(s.keyPrefix, sessionID, providerName)
	idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, sessionID)

	data, ttl, err := marshalUpstreamTokensWithTTL(tokens)
	if err != nil {
		return err
	}

	newUserID := ""
	if tokens != nil {
		newUserID = tokens.UserID
	}

	userSetKeyPrefix := s.keyPrefix + KeyTypeUserUpstream + ":"

	_, err = storeUpstreamTokensScript.Run(ctx, s.client,
		[]string{key, idxKey},
		string(data),
		ttl.Milliseconds(),
		newUserID,
		userSetKeyPrefix,
	).Result()
	if err != nil {
		return fmt.Errorf("failed to store upstream tokens: %w", err)
	}

	return nil
}

// ResolveUpstreamTokenRowID returns an opaque ID for process-local refresh
// coordination of a logical upstream-token row.
func (s *RedisStorage) ResolveUpstreamTokenRowID(_ context.Context, sessionID, providerName string) (UpstreamTokenRowID, error) {
	if sessionID == "" {
		return "", fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return "", fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}
	return upstreamTokenRowID(redisUpstreamKey(s.keyPrefix, sessionID, providerName)), nil
}

// GetUpstreamTokens retrieves the upstream IDP tokens for a session and provider.
// Returns a new UpstreamTokens struct deserialized from Redis, which acts as
// a defensive copy - callers cannot modify the stored data by mutating the return value.
func (s *RedisStorage) GetUpstreamTokens(ctx context.Context, sessionID, providerName string) (*UpstreamTokens, error) {
	if sessionID == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	key := redisUpstreamKey(s.keyPrefix, sessionID, providerName)
	return s.getUpstreamTokensFromKey(ctx, key)
}

// GetAllUpstreamTokens retrieves all upstream IDP tokens for a session across all providers.
// Uses SMEMBERS on the session index set to find all provider keys, then MGET to fetch them.
// Returns a map of providerName -> tokens. Returns an empty map for unknown sessions.
func (s *RedisStorage) GetAllUpstreamTokens(ctx context.Context, sessionID string) (map[string]*UpstreamTokens, error) {
	idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, sessionID)
	result := make(map[string]*UpstreamTokens)

	// Get all provider keys from the session index set
	providerKeys, err := s.client.SMembers(ctx, idxKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return result, nil
		}
		return nil, fmt.Errorf("failed to get upstream token index: %w", err)
	}

	providerKeys, dropped := filterIndexMembersByPrefix(s.keyPrefix, providerKeys)
	warnDroppedIndexMembers("GetAllUpstreamTokens", idxKey, s.keyPrefix, dropped)

	if len(providerKeys) == 0 {
		return result, nil
	}

	// Fetch all provider tokens in a single MGET
	values, err := s.client.MGet(ctx, providerKeys...).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	// The per-provider key format is "{prefix}upstream:{sessionID}:{providerName}"
	// Extract providerName by stripping the prefix + "upstream:{sessionID}:"
	keyPrefix := fmt.Sprintf("%s%s:%s:", s.keyPrefix, KeyTypeUpstream, sessionID)

	for i, val := range values {
		if val == nil {
			continue
		}

		data, ok := val.(string)
		if !ok {
			slog.Warn("skipping upstream token entry: unexpected type", "key", providerKeys[i])
			continue
		}

		tokens, parseErr := unmarshalUpstreamTokens([]byte(data))
		if parseErr != nil && !errors.Is(parseErr, ErrExpired) {
			slog.Warn("skipping corrupt upstream token entry", "key", providerKeys[i], "error", parseErr)
			continue
		}

		// Extract provider name from the key
		providerName := ""
		if len(providerKeys[i]) > len(keyPrefix) {
			providerName = providerKeys[i][len(keyPrefix):]
		}
		if providerName == "" && tokens != nil {
			providerName = tokens.ProviderID
		}

		if providerName != "" {
			result[providerName] = tokens
		}
	}

	return result, nil
}

// DeleteUpstreamTokens removes all upstream IDP tokens for a session (all providers).
// Uses the session index set to find all provider keys and deletes them atomically.
func (s *RedisStorage) DeleteUpstreamTokens(ctx context.Context, sessionID string) error {
	idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, sessionID)

	// Get all provider keys from the session index set
	providerKeys, err := s.client.SMembers(ctx, idxKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
		}
		return fmt.Errorf("failed to get upstream token index: %w", err)
	}

	providerKeys, dropped := filterIndexMembersByPrefix(s.keyPrefix, providerKeys)
	warnDroppedIndexMembers("DeleteUpstreamTokens", idxKey, s.keyPrefix, dropped)

	if len(providerKeys) == 0 {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}

	// Collect UserIDs for reverse-index cleanup before deleting
	var userIDs []string
	for _, providerKey := range providerKeys {
		data, getErr := s.client.Get(ctx, providerKey).Bytes()
		if getErr != nil {
			continue
		}
		if string(data) != nullMarker {
			var stored storedUpstreamTokens
			if unmarshalErr := json.Unmarshal(data, &stored); unmarshalErr == nil && stored.UserID != "" {
				userIDs = append(userIDs, stored.UserID)
			}
		}
	}

	// Delete all provider keys and the index set
	keysToDelete := append(slices.Clone(providerKeys), idxKey)
	if err := s.client.Del(ctx, keysToDelete...).Err(); err != nil {
		return fmt.Errorf("failed to delete upstream tokens: %w", err)
	}

	// Best-effort secondary index cleanup for user:upstream sets
	for _, userID := range userIDs {
		userUpstreamSetKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, userID)
		for _, providerKey := range providerKeys {
			warnOnCleanupErr(s.client.SRem(ctx, userUpstreamSetKey, providerKey).Err(), "SRem", userUpstreamSetKey)
		}
	}

	return nil
}

// DeleteUpstreamTokensForProvider removes tokens for a single (sessionID, providerName),
// leaving sibling providers' rows intact. Absent row returns nil.
func (s *RedisStorage) DeleteUpstreamTokensForProvider(ctx context.Context, sessionID, providerName string) error {
	if sessionID == "" {
		return fosite.ErrInvalidRequest.WithHint("session ID cannot be empty")
	}
	if providerName == "" {
		return fosite.ErrInvalidRequest.WithHint("provider name cannot be empty")
	}

	key := redisUpstreamKey(s.keyPrefix, sessionID, providerName)
	idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, sessionID)

	// Best-effort: read UserID for reverse-index cleanup before deleting.
	var userID string
	data, err := s.client.Get(ctx, key).Bytes()
	if err == nil && string(data) != nullMarker {
		var stored storedUpstreamTokens
		if unmarshalErr := json.Unmarshal(data, &stored); unmarshalErr == nil {
			userID = stored.UserID
		}
	}
	// redis.Nil (key absent) or any error on GET: skip user-index cleanup gracefully.

	// Del returns count 0 on absent key — treat as nil (not ErrNotFound).
	if delErr := s.client.Del(ctx, key).Err(); delErr != nil {
		return fmt.Errorf("failed to delete upstream tokens for provider: %w", delErr)
	}

	// Best-effort secondary index cleanup — leave sibling members in idxKey intact.
	// If this was the last provider for the session, the set becomes empty and
	// expires by its own TTL; we do not delete the set key itself (intentional,
	// matching the best-effort cleanup posture of DeleteUpstreamTokens).
	warnOnCleanupErr(s.client.SRem(ctx, idxKey, key).Err(), "SRem", idxKey)
	if userID != "" {
		userUpstreamSetKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, userID)
		warnOnCleanupErr(s.client.SRem(ctx, userUpstreamSetKey, key).Err(), "SRem", userUpstreamSetKey)
	}

	return nil
}

// GetLatestUpstreamTokensForUser returns the upstream token row for (userID, providerID)
// with the highest ExpiresAt across all sessions.
//
// Expired tokens (past ExpiresAt) are returned so callers can use the refresh
// token; filtering by access-token expiry is the caller's responsibility.
// See the interface declaration in types.go for the full contract.
func (s *RedisStorage) GetLatestUpstreamTokensForUser(ctx context.Context, userID, providerID string) (*UpstreamTokens, error) {
	if userID == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("user ID cannot be empty")
	}
	if providerID == "" {
		return nil, fosite.ErrInvalidRequest.WithHint("provider ID cannot be empty")
	}

	setKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, userID)

	members, err := s.client.SMembers(ctx, setKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to get user upstream index: %w", err)
	}

	members, dropped := filterIndexMembersByPrefix(s.keyPrefix, members)
	warnDroppedIndexMembers("GetLatestUpstreamTokensForUser", setKey, s.keyPrefix, dropped)

	if len(members) == 0 {
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}

	values, err := s.client.MGet(ctx, members...).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	var winner *storedUpstreamTokens
	for i, val := range values {
		stored, ok := parseUserUpstreamEntry(val, providerID, members[i])
		if !ok {
			continue
		}

		// Tie-breaker: prefer non-expiring rows (ExpiresAt == 0, the no-expiry
		// sentinel for providers like Slack and GitHub OAuth Apps). Among finite
		// expiries, the latest wins. This aligns with the rest of the package
		// treating zero ExpiresAt as "alive forever".
		if winner == nil || compareExpiryInt64(stored.ExpiresAt, winner.ExpiresAt) > 0 {
			winner = stored
		}
	}

	if winner == nil {
		return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
	}

	return winner.toUpstreamTokens(), nil
}

// compareExpiryInt64 is the int64 (Unix-epoch) variant of compareExpiry. Zero
// (no-expiry sentinel) ranks latest; among finite epochs, the larger ranks
// latest. Returns -1/0/+1.
func compareExpiryInt64(a, b int64) int {
	switch {
	case a == 0 && b == 0:
		return 0
	case a == 0:
		return 1
	case b == 0:
		return -1
	case a > b:
		return 1
	case a < b:
		return -1
	}
	return 0
}

// parseUserUpstreamEntry parses one raw Redis value from the user-upstream index
// and returns the decoded storedUpstreamTokens together with a match flag.
// It returns (nil, false) for nil values, type mismatches, deletion tombstones,
// JSON decode errors, and rows whose ProviderID does not match providerID.
// keyName is used only for warning log messages.
func parseUserUpstreamEntry(val any, providerID, keyName string) (*storedUpstreamTokens, bool) {
	if val == nil {
		// Dangling set member: the per-provider key has been TTL-evicted.
		// Skip it; the next write will clean up the index entry (best-effort).
		return nil, false
	}

	data, ok := val.(string)
	if !ok {
		slog.Warn("skipping upstream token entry: unexpected type", "key", keyName)
		return nil, false
	}

	if data == nullMarker {
		// Deletion tombstone — treat as absent.
		return nil, false
	}

	var stored storedUpstreamTokens
	if unmarshalErr := json.Unmarshal([]byte(data), &stored); unmarshalErr != nil {
		slog.Warn("skipping corrupt upstream token entry", "key", keyName, "error", unmarshalErr)
		return nil, false
	}

	if stored.ProviderID != providerID {
		return nil, false
	}

	return &stored, true
}

// getUpstreamTokensFromKey retrieves and deserializes upstream tokens from a specific Redis key.
func (s *RedisStorage) getUpstreamTokensFromKey(ctx context.Context, key string) (*UpstreamTokens, error) {
	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Upstream tokens not found"))
		}
		return nil, fmt.Errorf("failed to get upstream tokens: %w", err)
	}

	return unmarshalUpstreamTokens(data)
}

// unmarshalUpstreamTokens deserializes upstream tokens from JSON bytes.
func unmarshalUpstreamTokens(data []byte) (*UpstreamTokens, error) {
	// Handle null marker
	if string(data) == nullMarker {
		return nil, nil
	}

	var stored storedUpstreamTokens
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal upstream tokens: %w", err)
	}

	tokens := stored.toUpstreamTokens()

	// tokens.ExpiresAt is zero when the stored epoch was 0 (the write path stores
	// 0 for zero time.Time, which toUpstreamTokens leaves as the zero time). Skip
	// the expiry check in that case since Redis TTL handles the actual expiration.
	// Return tokens along with ErrExpired so callers can use the refresh token.
	if !tokens.ExpiresAt.IsZero() && time.Now().After(tokens.ExpiresAt) {
		return tokens, ErrExpired
	}

	return tokens, nil
}

// -----------------------
// DCR Credentials Storage
// -----------------------

// storedDCRCredentials is the on-the-wire JSON representation of DCRCredentials.
// Time fields use int64 Unix epoch; 0 is the sentinel meaning "not set", matching
// the storedUpstreamTokens convention. ClientSecretExpiresAt == 0 specifically
// encodes the RFC 7591 §3.2.1 "client_secret does not expire" semantics, in
// which case StoreDCRCredentialsIfAbsent persists the entry without a Redis TTL.
type storedDCRCredentials struct {
	// Embed the canonical key so a row recovered without its lookup key
	// (e.g. via SCAN during diagnostics) still self-identifies.
	KeyIssuer      string `json:"key_issuer"`
	KeyUpstreamID  string `json:"key_upstream_id"`
	KeyRedirectURI string `json:"key_redirect_uri"`
	KeyScopesHash  string `json:"key_scopes_hash"`

	ProviderName string `json:"provider_name,omitempty"`

	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret,omitempty"` //nolint:gosec // G117: field legitimately holds sensitive data
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method,omitempty"`

	// Bearer token for the RFC 7592 management endpoint.
	//nolint:gosec // G117: field legitimately holds sensitive data
	RegistrationAccessToken string `json:"registration_access_token,omitempty"`
	RegistrationClientURI   string `json:"registration_client_uri,omitempty"`

	AuthorizationEndpoint string `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string `json:"token_endpoint,omitempty"`

	CreatedAt             int64 `json:"created_at"`
	ClientSecretExpiresAt int64 `json:"client_secret_expires_at"`
}

// toDCRCredentials decodes the stored form back into the public type, mirroring
// storedUpstreamTokens.toUpstreamTokens. Zero epoch values become the zero
// time.Time, preserving the "not set" sentinel.
func (s *storedDCRCredentials) toDCRCredentials() *DCRCredentials {
	var createdAt time.Time
	if s.CreatedAt != 0 {
		createdAt = time.Unix(s.CreatedAt, 0)
	}
	var clientSecretExpiresAt time.Time
	if s.ClientSecretExpiresAt != 0 {
		clientSecretExpiresAt = time.Unix(s.ClientSecretExpiresAt, 0)
	}
	return &DCRCredentials{
		Key: DCRKey{
			Issuer:      s.KeyIssuer,
			UpstreamID:  s.KeyUpstreamID,
			RedirectURI: s.KeyRedirectURI,
			ScopesHash:  s.KeyScopesHash,
		},
		ProviderName:            s.ProviderName,
		ClientID:                s.ClientID,
		ClientSecret:            s.ClientSecret,
		TokenEndpointAuthMethod: s.TokenEndpointAuthMethod,
		RegistrationAccessToken: s.RegistrationAccessToken,
		RegistrationClientURI:   s.RegistrationClientURI,
		AuthorizationEndpoint:   s.AuthorizationEndpoint,
		TokenEndpoint:           s.TokenEndpoint,
		CreatedAt:               createdAt,
		ClientSecretExpiresAt:   clientSecretExpiresAt,
	}
}

// StoreDCRCredentialsIfAbsent claims creds.Key for creds via a Redis
// WATCH/MULTI compare-and-set (the same pattern ReconcileConfiguredClient
// uses above), returning the authoritative durable value: creds itself on a
// successful claim, or the existing winner's credentials when the key
// already holds a live (non-expired) entry. Callers MUST use the returned
// value — RFC 7591 dynamic registration mints a unique client_id/client_secret
// on every call, so two replicas racing a cache miss for the same Key
// register genuinely different OAuth clients with the upstream, and only the
// value the durable store agrees to is a credential either replica actually
// holds. Defensive copy is provided implicitly by JSON serialisation —
// caller mutations after the call cannot reach the persisted bytes.
//
// # Expired existing rows are claimable
//
// An existing row whose ClientSecretExpiresAt is non-zero and already in the
// past is treated as absent, mirroring MemoryStorage.StoreDCRCredentialsIfAbsent:
// it is overwritten with creds rather than handed back as if it were still
// the authoritative winner — but only when creds itself is fresher (creds'
// own ClientSecretExpiresAt is zero or still in the future). When both the
// existing row and the incoming creds are already expired — the startup
// case where many replicas independently re-resolve a dead upstream
// registration and each mints equally-stale credentials — the existing row
// is returned as-is instead. Overwriting would gain nothing there, and
// without this guard every concurrent claimant would keep re-observing an
// expired row and re-entering the write path, risking exhausting
// maxDCRClaimRetries under contention instead of converging the way the
// genuinely-absent case does. WATCH still guards the overwrite path: if
// another replica claims or refreshes the same key between our GET and our
// SET, EXEC aborts with redis.TxFailedErr and the loop below retries, so
// this does not reopen the double-registration race the NX-based
// predecessor of this method was built to close — it only changes what
// happens when the existing row is provably expired.
//
// This means a caller CAN receive an already-expired credential back from
// this call — deliberately, per the above, rather than as an oversight. The
// only thing that later re-checks an expiry against ResolveCredentials'
// cache is lookupCachedResolution, which only runs on a *subsequent*
// ResolveCredentials call for the same key; nothing currently re-resolves
// after the one-time boot-time call in embeddedauthserver.go. So a replica
// that receives an expired credential here keeps it for the rest of its
// process lifetime, failing cleanly with an upstream invalid_client/
// invalid_grant on every use until restart. Closing that gap needs
// revisiting credentials after boot, not another guard in this function —
// tracked in #6496.
//
// # TTL
//
// When creds.ClientSecretExpiresAt is non-zero (the upstream advertised an
// RFC 7591 §3.2.1 client_secret_expires_at), the entry is stored with a Redis
// TTL derived from time.Until(ClientSecretExpiresAt) so the row evicts before
// the upstream rejects the secret at the token endpoint. When zero (RFC 7591
// "never"), TTL=0 is used and the entry is long-lived.
//
// If ClientSecretExpiresAt is already in the past at call time, the entry is
// written with the bounded TTL pastExpiryDCRTTL (1 second) rather than rejected
// or stored long-lived. This keeps the store's "already-expired secret" window
// narrow even if the resolver never re-reads the row, and matches the
// fail-loud-but-tolerant posture: the caller's expiry timestamp round-trips so
// a downstream reader can still observe it and trigger re-registration.
//
// Validation is delegated to validateDCRCredentialsForStore so the rejection
// set stays in sync with MemoryStorage and any future backend.
func (s *RedisStorage) StoreDCRCredentialsIfAbsent(ctx context.Context, creds *DCRCredentials) (*DCRCredentials, error) {
	if err := validateDCRCredentialsForStore(creds); err != nil {
		return nil, err
	}

	key := redisDCRKey(s.keyPrefix, creds.Key)

	data, ttl, err := marshalDCRCredentialsForStore(creds)
	if err != nil {
		return nil, err
	}

	// result is set by txFn on every non-error path: either to creds (a
	// successful claim) or to the decoded existing row (a live winner
	// already present). It must not be read until s.client.Watch returns nil.
	var result *DCRCredentials
	txFn := func(tx *redis.Tx) error {
		var txErr error
		result, txErr = dcrClaimOrReturnWinner(ctx, tx, key, data, ttl, creds)
		return txErr
	}

	// Retry bound mirrors ReconcileConfiguredClient's maxConfiguredClientReconcileRetries:
	// go-redis does not retry Watch internally, so a concurrent writer to the
	// watched key aborts EXEC with redis.TxFailedErr, which must be retried
	// here for the race this method exists to close to resolve on its own.
	var watchErr error
	for attempt := 0; attempt < maxDCRClaimRetries; attempt++ {
		watchErr = s.client.Watch(ctx, txFn, key)
		if watchErr == nil {
			return result, nil
		}
		if !errors.Is(watchErr, redis.TxFailedErr) {
			return nil, fmt.Errorf("failed to store dcr credentials: %w", watchErr)
		}
	}
	return nil, fmt.Errorf("failed to claim dcr credentials after %d attempts: %w", maxDCRClaimRetries, watchErr)
}

// marshalDCRCredentialsForStore serializes creds to its Redis wire form and
// derives the TTL to store it with. Split out of StoreDCRCredentialsIfAbsent
// purely to keep that method's cyclomatic complexity down; it has no
// transaction-retry concerns of its own.
//
// Derive Redis TTL from ClientSecretExpiresAt:
//   - Zero (unset)  -> TTL=0 (no expiration) per RFC 7591 §3.2.1 "never".
//   - Future expiry -> TTL = time.Until(expiry).
//   - Past expiry   -> TTL = pastExpiryDCRTTL (bounded eviction window).
//
// See StoreDCRCredentialsIfAbsent's docstring for the past-expiry rationale.
func marshalDCRCredentialsForStore(creds *DCRCredentials) ([]byte, time.Duration, error) {
	stored := storedDCRCredentials{
		KeyIssuer:               creds.Key.Issuer,
		KeyUpstreamID:           creds.Key.UpstreamID,
		KeyRedirectURI:          creds.Key.RedirectURI,
		KeyScopesHash:           creds.Key.ScopesHash,
		ProviderName:            creds.ProviderName,
		ClientID:                creds.ClientID,
		ClientSecret:            creds.ClientSecret,
		TokenEndpointAuthMethod: creds.TokenEndpointAuthMethod,
		RegistrationAccessToken: creds.RegistrationAccessToken,
		RegistrationClientURI:   creds.RegistrationClientURI,
		AuthorizationEndpoint:   creds.AuthorizationEndpoint,
		TokenEndpoint:           creds.TokenEndpoint,
	}
	if !creds.CreatedAt.IsZero() {
		stored.CreatedAt = creds.CreatedAt.Unix()
	}
	if !creds.ClientSecretExpiresAt.IsZero() {
		stored.ClientSecretExpiresAt = creds.ClientSecretExpiresAt.Unix()
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal dcr credentials: %w", err)
	}

	ttl := time.Duration(0)
	if !creds.ClientSecretExpiresAt.IsZero() {
		if until := time.Until(creds.ClientSecretExpiresAt); until > 0 {
			ttl = until
		} else {
			ttl = pastExpiryDCRTTL
		}
	}
	return data, ttl, nil
}

// dcrClaimOrReturnWinner runs the read-check-write body of
// StoreDCRCredentialsIfAbsent's WATCH transaction: an absent row, or an
// existing row that is expired while creds is fresher, is claimed with
// (data, ttl) inside MULTI; a live existing row — or one that is expired but
// no fresher than creds — is decoded and returned as-is, with no write. See
// StoreDCRCredentialsIfAbsent's "Expired existing rows" doc section for why.
// Split out purely to keep StoreDCRCredentialsIfAbsent's cyclomatic
// complexity down.
func dcrClaimOrReturnWinner(
	ctx context.Context, tx *redis.Tx, key string, data []byte, ttl time.Duration, creds *DCRCredentials,
) (*DCRCredentials, error) {
	claim := func() error {
		_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, data, ttl)
			return nil
		})
		return err
	}

	existingData, getErr := tx.Get(ctx, key).Bytes()
	if errors.Is(getErr, redis.Nil) {
		if err := claim(); err != nil {
			return nil, err
		}
		return creds, nil
	}
	if getErr != nil {
		return nil, fmt.Errorf("failed to get existing dcr credentials: %w", getErr)
	}

	var existingStored storedDCRCredentials
	if unmarshalErr := json.Unmarshal(existingData, &existingStored); unmarshalErr != nil {
		return nil, fmt.Errorf("failed to unmarshal existing dcr credentials: %w", unmarshalErr)
	}
	existing := existingStored.toDCRCredentials()

	expired := !existing.ClientSecretExpiresAt.IsZero() && time.Now().After(existing.ClientSecretExpiresAt)
	if !expired {
		return existing, nil
	}

	// The existing row is expired. Only overwrite it when the incoming
	// credentials are actually fresher — a genuine new registration
	// reclaiming a dead slot. If the incoming credentials are ALSO already
	// expired (the startup-reconciliation case: many replicas independently
	// re-resolving a dead upstream registration all mint equally-stale
	// credentials), overwriting gains nothing and, under contention, every
	// concurrent claimant would keep re-observing an expired row and
	// re-entering this write path, burning through maxDCRClaimRetries
	// instead of converging. Returning the stable existing row lets every
	// claimant converge without a write, matching the "genuinely absent"
	// case's self-limiting behaviour.
	incomingExpired := !creds.ClientSecretExpiresAt.IsZero() && time.Now().After(creds.ClientSecretExpiresAt)
	if incomingExpired {
		return existing, nil
	}

	// The existing row is provably expired: treat it as absent and overwrite
	// it, matching MemoryStorage's semantics. WATCH still protects this — see
	// StoreDCRCredentialsIfAbsent's "Expired existing rows" doc section.
	if err := claim(); err != nil {
		return nil, err
	}
	return creds, nil
}

// GetDCRCredentials retrieves the credentials previously persisted under key.
// Returns ErrNotFound (wrapped) when no entry exists. The returned value is a
// fresh struct decoded from JSON, which acts as a defensive copy.
//
// An unpopulated key (empty Issuer, UpstreamID, RedirectURI, or ScopesHash) cannot match
// any stored row because StoreDCRCredentialsIfAbsent rejects such keys, so a Get
// against one is a normal miss — ErrNotFound — matching
// MemoryStorage.GetDCRCredentials and the DCRCredentialStore interface contract.
func (s *RedisStorage) GetDCRCredentials(ctx context.Context, key DCRKey) (*DCRCredentials, error) {
	redisKey := redisDCRKey(s.keyPrefix, key)
	data, err := s.client.Get(ctx, redisKey).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("DCR credentials not found"))
		}
		return nil, fmt.Errorf("failed to get dcr credentials: %w", err)
	}

	var stored storedDCRCredentials
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dcr credentials: %w", err)
	}

	return stored.toDCRCredentials(), nil
}

// -----------------------
// Pending Authorization Storage
// -----------------------

// storedPendingAuthorization is a serializable wrapper for PendingAuthorization.
type storedPendingAuthorization struct {
	ClientID             string   `json:"client_id"`
	RedirectURI          string   `json:"redirect_uri"`
	State                string   `json:"state"`
	PKCEChallenge        string   `json:"pkce_challenge"`
	PKCEMethod           string   `json:"pkce_method"`
	Scopes               []string `json:"scopes"`
	InternalState        string   `json:"internal_state"`
	UpstreamPKCEVerifier string   `json:"upstream_pkce_verifier"`
	UpstreamNonce        string   `json:"upstream_nonce"`
	UpstreamProviderName string   `json:"upstream_provider_name,omitempty"`
	SessionID            string   `json:"session_id,omitempty"`
	ResolvedUserID       string   `json:"resolved_user_id,omitempty"`
	ResolvedUserName     string   `json:"resolved_user_name,omitempty"`
	ResolvedUserEmail    string   `json:"resolved_user_email,omitempty"`
	SingleLeg            bool     `json:"single_leg,omitempty"`
	ChainUpstreams       []string `json:"chain_upstreams,omitempty"`
	CreatedAt            int64    `json:"created_at"`
}

// StorePendingAuthorization stores a pending authorization request.
func (s *RedisStorage) StorePendingAuthorization(ctx context.Context, state string, pending *PendingAuthorization) error {
	if state == "" {
		return fosite.ErrInvalidRequest.WithHint("state cannot be empty")
	}
	if pending == nil {
		return fosite.ErrInvalidRequest.WithHint("pending authorization cannot be nil")
	}

	key := redisKey(s.keyPrefix, KeyTypePending, state)

	stored := storedPendingAuthorization{
		ClientID:             pending.ClientID,
		RedirectURI:          pending.RedirectURI,
		State:                pending.State,
		PKCEChallenge:        pending.PKCEChallenge,
		PKCEMethod:           pending.PKCEMethod,
		Scopes:               slices.Clone(pending.Scopes),
		InternalState:        pending.InternalState,
		UpstreamPKCEVerifier: pending.UpstreamPKCEVerifier,
		UpstreamNonce:        pending.UpstreamNonce,
		UpstreamProviderName: pending.UpstreamProviderName,
		SessionID:            pending.SessionID,
		ResolvedUserID:       pending.ResolvedUserID,
		ResolvedUserName:     pending.ResolvedUserName,
		ResolvedUserEmail:    pending.ResolvedUserEmail,
		SingleLeg:            pending.SingleLeg,
		ChainUpstreams:       slices.Clone(pending.ChainUpstreams),
		CreatedAt:            pending.CreatedAt.Unix(),
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal pending authorization: %w", err)
	}

	return s.client.Set(ctx, key, data, DefaultPendingAuthorizationTTL).Err()
}

// LoadPendingAuthorization retrieves a pending authorization by internal state.
func (s *RedisStorage) LoadPendingAuthorization(ctx context.Context, state string) (*PendingAuthorization, error) {
	key := redisKey(s.keyPrefix, KeyTypePending, state)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Pending authorization not found"))
		}
		return nil, fmt.Errorf("failed to get pending authorization: %w", err)
	}

	var stored storedPendingAuthorization
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pending authorization: %w", err)
	}

	createdAt := time.Unix(stored.CreatedAt, 0)

	// Check if expired (TTL should handle this, but double-check)
	if time.Since(createdAt) > DefaultPendingAuthorizationTTL {
		return nil, ErrExpired
	}

	return &PendingAuthorization{
		ClientID:             stored.ClientID,
		RedirectURI:          stored.RedirectURI,
		State:                stored.State,
		PKCEChallenge:        stored.PKCEChallenge,
		PKCEMethod:           stored.PKCEMethod,
		Scopes:               slices.Clone(stored.Scopes),
		InternalState:        stored.InternalState,
		UpstreamPKCEVerifier: stored.UpstreamPKCEVerifier,
		UpstreamNonce:        stored.UpstreamNonce,
		UpstreamProviderName: stored.UpstreamProviderName,
		SessionID:            stored.SessionID,
		ResolvedUserID:       stored.ResolvedUserID,
		ResolvedUserName:     stored.ResolvedUserName,
		ResolvedUserEmail:    stored.ResolvedUserEmail,
		SingleLeg:            stored.SingleLeg,
		ChainUpstreams:       slices.Clone(stored.ChainUpstreams),
		CreatedAt:            createdAt,
	}, nil
}

// DeletePendingAuthorization removes a pending authorization.
func (s *RedisStorage) DeletePendingAuthorization(ctx context.Context, state string) error {
	key := redisKey(s.keyPrefix, KeyTypePending, state)

	result, err := s.client.Del(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to delete pending authorization: %w", err)
	}
	if result == 0 {
		return fmt.Errorf("%w: %w", ErrNotFound, fosite.ErrNotFound.WithHint("Pending authorization not found"))
	}

	return nil
}

// -----------------------
// User Storage
// -----------------------

// storedUser is a serializable wrapper for User.
type storedUser struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// CreateUser creates a new user account.
func (s *RedisStorage) CreateUser(ctx context.Context, user *User) error {
	if user == nil {
		return fosite.ErrInvalidRequest.WithHint("user cannot be nil")
	}
	if user.ID == "" {
		return fosite.ErrInvalidRequest.WithHint("user ID cannot be empty")
	}

	key := redisKey(s.keyPrefix, KeyTypeUser, user.ID)

	stored := storedUser{
		ID:        user.ID,
		CreatedAt: user.CreatedAt.Unix(),
		UpdatedAt: user.UpdatedAt.Unix(),
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	// Use SetNX for atomic check-and-set to prevent race conditions.
	// Users don't expire (TTL=0).
	result, err := s.client.SetNX(ctx, key, data, 0).Result()
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	if !result {
		return fmt.Errorf("%w: user already exists", ErrAlreadyExists)
	}

	return nil
}

// GetUser retrieves a user by their internal ID.
func (s *RedisStorage) GetUser(ctx context.Context, id string) (*User, error) {
	key := redisKey(s.keyPrefix, KeyTypeUser, id)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: user not found", ErrNotFound)
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	var stored storedUser
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user: %w", err)
	}

	return &User{
		ID:        stored.ID,
		CreatedAt: time.Unix(stored.CreatedAt, 0),
		UpdatedAt: time.Unix(stored.UpdatedAt, 0),
	}, nil
}

// DeleteUser removes a user account and all associated data.
func (s *RedisStorage) DeleteUser(ctx context.Context, id string) error {
	key := redisKey(s.keyPrefix, KeyTypeUser, id)

	// Check if user exists
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to check user existence: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: user not found", ErrNotFound)
	}

	// Delete the user
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	// Best-effort cascade deletion of associated data (see warnOnCleanupErr).
	// The user record is already deleted; orphaned keys will expire via TTL.
	userProviderSetKey := redisSetKey(s.keyPrefix, KeyTypeUserProviders, id)
	providerKeys, err := s.client.SMembers(ctx, userProviderSetKey).Result()
	if err == nil {
		for _, providerKey := range providerKeys {
			warnOnCleanupErr(s.client.Del(ctx, providerKey).Err(), "Del", providerKey)
		}
		warnOnCleanupErr(s.client.Del(ctx, userProviderSetKey).Err(), "Del", userProviderSetKey)
	}

	// Delete associated upstream tokens
	userUpstreamSetKey := redisSetKey(s.keyPrefix, KeyTypeUserUpstream, id)
	upstreamKeys, err := s.client.SMembers(ctx, userUpstreamSetKey).Result()
	if err == nil {
		for _, upstreamKey := range upstreamKeys {
			warnOnCleanupErr(s.client.Del(ctx, upstreamKey).Err(), "Del", upstreamKey)
		}
		warnOnCleanupErr(s.client.Del(ctx, userUpstreamSetKey).Err(), "Del", userUpstreamSetKey)
	}

	return nil
}

// -----------------------
// Provider Identity Storage
// -----------------------

// storedProviderIdentity is a serializable wrapper for ProviderIdentity.
type storedProviderIdentity struct {
	UserID          string `json:"user_id"`
	ProviderID      string `json:"provider_id"`
	ProviderSubject string `json:"provider_subject"`
	LinkedAt        int64  `json:"linked_at"`
	LastUsedAt      int64  `json:"last_used_at"`
}

// CreateProviderIdentity links a provider identity to a user.
func (s *RedisStorage) CreateProviderIdentity(ctx context.Context, identity *ProviderIdentity) error {
	if identity == nil {
		return fosite.ErrInvalidRequest.WithHint("identity cannot be nil")
	}
	if identity.UserID == "" {
		return fosite.ErrInvalidRequest.WithHint("user ID cannot be empty")
	}
	if identity.ProviderID == "" {
		return fosite.ErrInvalidRequest.WithHint("provider ID cannot be empty")
	}
	if identity.ProviderSubject == "" {
		return fosite.ErrInvalidRequest.WithHint("provider subject cannot be empty")
	}

	// Verify user exists
	userKey := redisKey(s.keyPrefix, KeyTypeUser, identity.UserID)
	exists, err := s.client.Exists(ctx, userKey).Result()
	if err != nil {
		return fmt.Errorf("failed to check user existence: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: user not found", ErrNotFound)
	}

	key := redisProviderKey(s.keyPrefix, identity.ProviderID, identity.ProviderSubject)

	stored := storedProviderIdentity{
		UserID:          identity.UserID,
		ProviderID:      identity.ProviderID,
		ProviderSubject: identity.ProviderSubject,
		LinkedAt:        identity.LinkedAt.Unix(),
		LastUsedAt:      identity.LastUsedAt.Unix(),
	}

	data, err := json.Marshal(stored) //nolint:gosec // G117 - internal Redis storage serialization, not exposed to users
	if err != nil {
		return fmt.Errorf("failed to marshal identity: %w", err)
	}

	// Use SetNX for atomic check-and-set to prevent race conditions.
	// Provider identities don't expire (TTL=0).
	result, err := s.client.SetNX(ctx, key, data, 0).Result()
	if err != nil {
		return fmt.Errorf("failed to store identity: %w", err)
	}
	if !result {
		return fmt.Errorf("%w: provider identity already linked", ErrAlreadyExists)
	}

	// Add to user's provider identity set for reverse lookup.
	// Note: This set may contain stale references if identities are deleted independently.
	// Stale entries are cleaned up lazily during GetUserProviderIdentities reads.
	// A future DeleteProviderIdentity method should also clean up this set.
	userProviderSetKey := redisSetKey(s.keyPrefix, KeyTypeUserProviders, identity.UserID)
	return s.client.SAdd(ctx, userProviderSetKey, key).Err()
}

// GetProviderIdentity retrieves a provider identity by provider ID and subject.
func (s *RedisStorage) GetProviderIdentity(ctx context.Context, providerID, providerSubject string) (*ProviderIdentity, error) {
	key := redisProviderKey(s.keyPrefix, providerID, providerSubject)

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("%w: provider identity not found", ErrNotFound)
		}
		return nil, fmt.Errorf("failed to get identity: %w", err)
	}

	var stored storedProviderIdentity
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal identity: %w", err)
	}

	return &ProviderIdentity{
		UserID:          stored.UserID,
		ProviderID:      stored.ProviderID,
		ProviderSubject: stored.ProviderSubject,
		LinkedAt:        time.Unix(stored.LinkedAt, 0),
		LastUsedAt:      time.Unix(stored.LastUsedAt, 0),
	}, nil
}

// updateLastUsedScript is a Lua script that atomically updates the LastUsedAt field
// of a provider identity. This prevents race conditions when multiple requests
// try to update the same identity concurrently.
// Returns 1 on success, 0 if the key doesn't exist.
var updateLastUsedScript = redis.NewScript(`
local data = redis.call('GET', KEYS[1])
if not data then
	return 0
end
local identity = cjson.decode(data)
identity.last_used_at = tonumber(ARGV[1])
redis.call('SET', KEYS[1], cjson.encode(identity))
return 1
`)

// UpdateProviderIdentityLastUsed updates the LastUsedAt timestamp for a provider identity.
// Uses a Lua script to ensure atomicity and prevent race conditions.
func (s *RedisStorage) UpdateProviderIdentityLastUsed(
	ctx context.Context, providerID, providerSubject string, lastUsedAt time.Time,
) error {
	key := redisProviderKey(s.keyPrefix, providerID, providerSubject)

	result, err := updateLastUsedScript.Run(ctx, s.client, []string{key}, lastUsedAt.Unix()).Int()
	if err != nil {
		return fmt.Errorf("failed to update identity: %w", err)
	}

	// Script returns 0 if the key doesn't exist
	if result == 0 {
		return fmt.Errorf("%w: provider identity not found", ErrNotFound)
	}

	return nil
}

// GetUserProviderIdentities returns all provider identities linked to a user.
func (s *RedisStorage) GetUserProviderIdentities(ctx context.Context, userID string) ([]*ProviderIdentity, error) {
	// Verify user exists
	userKey := redisKey(s.keyPrefix, KeyTypeUser, userID)
	exists, err := s.client.Exists(ctx, userKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to check user existence: %w", err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("%w: user not found", ErrNotFound)
	}

	// Get user's provider identity keys
	userProviderSetKey := redisSetKey(s.keyPrefix, KeyTypeUserProviders, userID)
	keys, err := s.client.SMembers(ctx, userProviderSetKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to get provider identity keys: %w", err)
	}

	var identities []*ProviderIdentity
	for _, key := range keys {
		data, err := s.client.Get(ctx, key).Bytes()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				// Stale set entry — the identity key has expired or was deleted.
				// Skip silently; stale entries are cleaned up during write
				// operations (DeleteUser, CreateProviderIdentity) to avoid
				// mutation side effects in this read-only method.
				continue
			}
			return nil, fmt.Errorf("failed to get identity: %w", err)
		}

		var stored storedProviderIdentity
		if err := json.Unmarshal(data, &stored); err != nil {
			return nil, fmt.Errorf("failed to unmarshal identity: %w", err)
		}

		identities = append(identities, &ProviderIdentity{
			UserID:          stored.UserID,
			ProviderID:      stored.ProviderID,
			ProviderSubject: stored.ProviderSubject,
			LinkedAt:        time.Unix(stored.LinkedAt, 0),
			LastUsedAt:      time.Unix(stored.LastUsedAt, 0),
		})
	}

	return identities, nil
}

// -----------------------
// Serialization Helpers
// -----------------------

// marshalRequester serializes a fosite.Requester to JSON.
// The full session is serialized as a JSON blob to preserve all session data
// including JWT claims, headers, and upstream session IDs. This is critical
// for token refresh — fosite's JWT strategy requires the session to implement
// oauth2.JWTSessionContainer, which redisSession did not.
func marshalRequester(request fosite.Requester) ([]byte, error) {
	sessionData, err := json.Marshal(request.GetSession())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session: %w", err)
	}

	// A clientless grant (fosite's CanSkipClientAuth) is expected to attach a
	// synthetic client (see NewSyntheticClient) rather than leave this nil;
	// this fallback only guards against a future clientless grant that
	// forgets to.
	var clientID string
	if client := request.GetClient(); client != nil {
		clientID = client.GetID()
	}

	stored := storedSession{
		ClientID:          clientID,
		RequestedAt:       request.GetRequestedAt(),
		RequestedScopes:   request.GetRequestedScopes(),
		GrantedScopes:     request.GetGrantedScopes(),
		RequestedAudience: request.GetRequestedAudience(),
		GrantedAudience:   request.GetGrantedAudience(),
		Form:              request.GetRequestForm(),
		RequestID:         request.GetID(),
		Session:           sessionData,
	}

	return json.Marshal(stored)
}

// unmarshalRequester deserializes a fosite.Requester from JSON.
// It requires storage access to look up the client and a session factory
// to create the correct session type for deserialization.
func unmarshalRequester(ctx context.Context, data []byte, s *RedisStorage) (fosite.Requester, error) {
	var stored storedSession
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// A synthetic client (see NewSyntheticClient) was never registered via
	// RegisterClient, so its ID alone reconstructs it rather than looking it
	// up through the client registry.
	var client fosite.Client
	if IsSyntheticClientID(stored.ClientID) {
		client = NewSyntheticClient(stored.ClientID)
	} else {
		// This lookup consults only this storage's own client rows: a client
		// that was resolved dynamically at authorize time but never persisted
		// would fail exactly here, surfacing to the client as a bare
		// invalid_grant at the token endpoint — so log the client id and the
		// underlying error once, which otherwise leave no server-side trace
		// (see issue #6187). The returned error deliberately does not repeat
		// the client id: all four call sites propagate it up to fosite, which
		// would otherwise report the same failure to the operator twice.
		fetchedClient, err := s.GetClient(ctx, stored.ClientID)
		if err != nil {
			slog.WarnContext(ctx, "failed to resolve client for stored session",
				"client_id", stored.ClientID, "error", err)
			return nil, fmt.Errorf("failed to get client for session: %w", err)
		}
		client = fetchedClient
	}

	// Create a session prototype via factory, then deserialize the full session
	// blob into it. This preserves JWT claims, headers, and upstream session IDs.
	sess := defaultSessionFactory("", "", "")
	if len(stored.Session) > 0 {
		if err := json.Unmarshal(stored.Session, sess); err != nil {
			return nil, fmt.Errorf("failed to unmarshal session data: %w", err)
		}
	}

	return &fosite.Request{
		ID:                stored.RequestID,
		RequestedAt:       stored.RequestedAt,
		Client:            client,
		RequestedScope:    stored.RequestedScopes,
		GrantedScope:      stored.GrantedScopes,
		RequestedAudience: stored.RequestedAudience,
		GrantedAudience:   stored.GrantedAudience,
		Form:              url.Values(stored.Form),
		Session:           sess,
	}, nil
}

// getTTLFromRequester extracts the TTL from a fosite.Requester session.
func getTTLFromRequester(request fosite.Requester, tokenType fosite.TokenType, defaultTTL time.Duration) time.Duration {
	if request == nil {
		return defaultTTL
	}

	sess := request.GetSession()
	if sess == nil {
		return defaultTTL
	}

	expTime := sess.GetExpiresAt(tokenType)
	if expTime.IsZero() {
		return defaultTTL
	}

	ttl := time.Until(expTime)
	if ttl <= 0 {
		return defaultTTL
	}

	return ttl
}

// Compile-time interface compliance checks
var (
	_ Storage                     = (*RedisStorage)(nil)
	_ PendingAuthorizationStorage = (*RedisStorage)(nil)
	_ ClientRegistry              = (*RedisStorage)(nil)
	_ UpstreamTokenStorage        = (*RedisStorage)(nil)
	_ UserStorage                 = (*RedisStorage)(nil)
	_ DCRCredentialStore          = (*RedisStorage)(nil)
	_ AssertionJWTConsumer        = (*RedisStorage)(nil)
)

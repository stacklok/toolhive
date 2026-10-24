// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
)

var _ ExchangeGrantStorage = (*RedisStorage)(nil)

// Field names of the exchange grant HASH.
const (
	// exchangeGrantFieldData holds the immutable JSON of the record, minus RevokedAt.
	exchangeGrantFieldData = "data"
	// exchangeGrantFieldRevoked holds RevokedAt in Unix milliseconds. It is absent
	// while the grant is live.
	exchangeGrantFieldRevoked = "rev_ms"
)

// exchangeGrantRedisSlack is added to every exchange grant key TTL. Liveness is
// inclusive (a grant is usable at now == ExpiresAt, a marker blocks replay at
// now == SourceExpiresAt + margin). miniredis deletes a key when its TTL
// reaches zero, so without the slack the key would be gone at the boundary
// instant itself and the contract suite could not observe inclusive liveness
// there. On real Redis the slack only extends a key's life by 1 ms. For a
// record, Go decides liveness, so the slack only affects garbage collection.
// For a marker it lengthens the blocking window by at most 1 ms, which is the
// safe direction.
const exchangeGrantRedisSlack = time.Millisecond

// Status codes returned by createExchangeGrantScript.
const (
	exchangeGrantCreated   int64 = 0
	exchangeGrantDuplicate int64 = 1
	exchangeGrantJTIKnown  int64 = 2
)

// exchangeGrantRecord is the JSON stored in the "data" field. It omits
// RevokedAt, which lives in its own field so a revoke is a single write. Decoding
// tolerates missing fields, so members added later do not break old records.
type exchangeGrantRecord struct {
	Signature          string    `json:"signature"`
	ParentSignature    string    `json:"parent_signature,omitempty"`
	ClientID           string    `json:"client_id"`
	Subject            string    `json:"subject"`
	SubjectIdentifier  string    `json:"subject_identifier,omitempty"`
	SourceIssuer       string    `json:"source_issuer,omitempty"`
	SourceJTI          string    `json:"source_jti,omitempty"`
	SourceExpiresAt    time.Time `json:"source_expires_at"`
	IssuableTokenTypes []string  `json:"issuable_token_types"`
	ExpiresAt          time.Time `json:"expires_at"`
	CreatedAt          time.Time `json:"created_at"`
}

// createExchangeGrantScript creates a grant and, for a top-level grant, its
// replay marker. KEYS[1] is the record HASH. KEYS[2], present only for a
// top-level grant whose marker is still live, is the marker key.
// ARGV: record JSON, record TTL in ms, marker value (the grant signature),
// marker TTL in ms.
//
// All checks run before any write, so a failure leaves nothing behind:
// a key of the wrong type is an error, an existing record returns 1
// (duplicate signature), an existing marker returns 2 (reused jti). Otherwise it
// writes the record, sets its TTL, writes the marker, and returns 0. Redis runs
// a script atomically, so concurrent creates for one jti admit exactly one.
//
// Cluster slot invariant: the script touches only its declared KEYS. Both keys
// are built from s.keyPrefix, which DeriveKeyPrefix builds with the `{ns:name}`
// hash tag (a single tag combining namespace and name, not `{ns}:{name}`), so
// they land on the same Redis Cluster slot. A future refactor that strips the
// hash tag, or builds either key without the prefix, passes on standalone Redis
// and fails with CROSSSLOT under Cluster.
var createExchangeGrantScript = redis.NewScript(`
local rtype = redis.call('TYPE', KEYS[1]).ok
if rtype ~= 'none' and rtype ~= 'hash' then
	return redis.error_reply('WRONGTYPE exchange grant key holds ' .. rtype)
end
local hasMarker = #KEYS >= 2
if hasMarker then
	local mtype = redis.call('TYPE', KEYS[2]).ok
	if mtype ~= 'none' and mtype ~= 'string' then
		return redis.error_reply('WRONGTYPE exchange grant marker key holds ' .. mtype)
	end
end
if rtype == 'hash' then
	return 1
end
if hasMarker and redis.call('EXISTS', KEYS[2]) == 1 then
	return 2
end
redis.call('HSET', KEYS[1], 'data', ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
if hasMarker then
	redis.call('SET', KEYS[2], ARGV[3], 'PX', ARGV[4])
end
return 0
`)

// revokeExchangeGrantScript sets rev_ms on an existing record without touching
// its TTL. KEYS[1] is the record HASH, ARGV[1] the revocation time in Unix ms.
// It returns 0 if the record is missing and 1 otherwise. HSETNX keeps the first
// revocation time, so a repeat revoke is a no-op. The existence check is part of
// the script because a bare HSETNX would create a TTL-less HASH for a missing key.
// It touches only its declared KEY, which carries the `{ns:name}` hash tag
// through s.keyPrefix (see createExchangeGrantScript).
var revokeExchangeGrantScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
	return 0
end
redis.call('HSETNX', KEYS[1], 'rev_ms', ARGV[1])
return 1
`)

// maxExchangeGrantTTL bounds every exchange grant key TTL. A deadline further
// away is treated as this far away, so a far-future exp can neither wrap a
// duration negative nor overflow a server that converts milliseconds to
// nanoseconds. A century is longer than any real token lifetime.
const maxExchangeGrantTTL = 100 * 365 * 24 * time.Hour

// exchangeGrantTTLMillis returns the TTL in milliseconds for a key that must
// live until deadline, measured from now: the remaining time rounded up to a
// whole millisecond, capped at maxExchangeGrantTTL, plus exchangeGrantRedisSlack.
// The result is zero or negative when the deadline has passed. The cap is
// applied before the slack is added, in int64 milliseconds. Time.Sub saturates
// at the Duration limits, and adding the slack to a saturated Duration would
// wrap negative, reading a far-future deadline as already passed and dropping a
// replay marker.
func exchangeGrantTTLMillis(deadline, now time.Time) int64 {
	d := deadline.Sub(now)
	ms := int64(d / time.Millisecond)
	if d%time.Millisecond > 0 {
		ms++
	}
	if limit := maxExchangeGrantTTL.Milliseconds(); ms > limit {
		ms = limit
	}
	return ms + int64(exchangeGrantRedisSlack/time.Millisecond)
}

// CreateExchangeGrant stores g and, for a top-level grant, its replay marker in
// one script. See ExchangeGrantStorage for the contract. TTLs and the marker
// liveness cutoff come from the store clock, never the wall clock.
//
// The marker window follows Redis's own TTL: the script checks EXISTS, and Go
// only skips the marker once its window has already passed. The margin absorbs
// clock skew and the time between s.now() and script execution, which can only
// lengthen the window (the safe direction).
//
// Redis treats any existing record hash as a duplicate signature, whereas the
// memory backend ignores a record past its ExpiresAt. The difference is only
// reachable within about 1 ms plus network latency of expiry, or under clock
// skew, and either outcome is safe. A hash with no data field is ErrAlreadyExists
// here but ErrNotFound on Get.
func (s *RedisStorage) CreateExchangeGrant(ctx context.Context, g *ExchangeGrant) error {
	if err := validateExchangeGrantShape(g); err != nil {
		return err
	}

	data, err := json.Marshal(exchangeGrantRecord{
		Signature:          g.Signature,
		ParentSignature:    g.ParentSignature,
		ClientID:           g.ClientID,
		Subject:            g.Subject,
		SubjectIdentifier:  g.SubjectIdentifier,
		SourceIssuer:       g.SourceIssuer,
		SourceJTI:          g.SourceJTI,
		SourceExpiresAt:    g.SourceExpiresAt,
		IssuableTokenTypes: g.IssuableTokenTypes,
		ExpiresAt:          g.ExpiresAt,
		CreatedAt:          g.CreatedAt,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal exchange grant: %w", err)
	}

	now := s.now()
	// An already-expired record still gets a 1 ms TTL, as the memory backend
	// accepts it and Get then reports it not found.
	recordTTL := max(exchangeGrantTTLMillis(g.ExpiresAt, now), 1)

	keys := []string{redisKey(s.keyPrefix, KeyTypeExchangeGrant, g.Signature)}
	var markerTTL int64
	if g.SourceJTI != "" {
		markerTTL = exchangeGrantTTLMillis(g.SourceExpiresAt.Add(ExchangeGrantMarkerMargin), now)
		// A marker whose window has already passed would never block anything,
		// so it is neither checked nor written.
		if markerTTL > 0 {
			keys = append(keys, redisAssertionJWTKey(s.keyPrefix, exchangeGrantAssertionPurpose, g.SourceIssuer, g.SourceJTI))
		}
	}

	status, err := createExchangeGrantScript.Run(ctx, s.client, keys, data, recordTTL, g.Signature, markerTTL).Int64()
	if err != nil {
		return fmt.Errorf("failed to create exchange grant: %w", err)
	}
	switch status {
	case exchangeGrantCreated:
		return nil
	case exchangeGrantDuplicate:
		return ErrAlreadyExists
	case exchangeGrantJTIKnown:
		return fosite.ErrJTIKnown
	default:
		return fmt.Errorf("failed to create exchange grant: unexpected script status %d", status)
	}
}

// GetExchangeGrant returns the grant, including revoked grants. A record past
// ExpiresAt is not found even if Redis has not evicted it, so the store clock
// decides, not the key TTL.
func (s *RedisStorage) GetExchangeGrant(ctx context.Context, signature string) (*ExchangeGrant, error) {
	key := redisKey(s.keyPrefix, KeyTypeExchangeGrant, signature)
	vals, err := s.client.HMGet(ctx, key, exchangeGrantFieldData, exchangeGrantFieldRevoked).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get exchange grant: %w", err)
	}
	if len(vals) != 2 {
		return nil, fmt.Errorf("failed to get exchange grant: unexpected reply with %d fields", len(vals))
	}
	if vals[0] == nil {
		// A hash without a data field (for example only rev_ms) is not a grant.
		return nil, ErrNotFound
	}
	raw, ok := vals[0].(string)
	if !ok {
		return nil, fmt.Errorf("failed to get exchange grant: unexpected data type %T", vals[0])
	}

	var rec exchangeGrantRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal exchange grant: %w", err)
	}
	// The signature comes from the lookup key, not the JSON, so a record cannot
	// claim to be a different grant.
	g := &ExchangeGrant{
		Signature:          signature,
		ParentSignature:    rec.ParentSignature,
		ClientID:           rec.ClientID,
		Subject:            rec.Subject,
		SubjectIdentifier:  rec.SubjectIdentifier,
		SourceIssuer:       rec.SourceIssuer,
		SourceJTI:          rec.SourceJTI,
		SourceExpiresAt:    rec.SourceExpiresAt,
		IssuableTokenTypes: rec.IssuableTokenTypes,
		ExpiresAt:          rec.ExpiresAt,
		CreatedAt:          rec.CreatedAt,
	}
	if vals[1] != nil {
		revStr, ok := vals[1].(string)
		if !ok {
			return nil, fmt.Errorf("failed to get exchange grant: unexpected rev_ms type %T", vals[1])
		}
		ms, err := strconv.ParseInt(revStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse exchange grant rev_ms: %w", err)
		}
		g.RevokedAt = time.UnixMilli(ms).UTC()
	}

	if s.now().After(g.ExpiresAt) {
		return nil, ErrNotFound
	}
	return g, nil
}

// RevokeExchangeGrant records now as the revocation time unless the grant is
// already revoked. It is one HSETNX-style write that leaves the key TTLs alone.
// Revocation is stored at millisecond precision. A missing or expired record
// returns ErrNotFound.
func (s *RedisStorage) RevokeExchangeGrant(ctx context.Context, signature string, now time.Time) error {
	if err := validateExchangeGrantRevokeTime(now); err != nil {
		return err
	}
	// The store clock, not now, decides expiry; now is only the timestamp written.
	if _, err := s.GetExchangeGrant(ctx, signature); err != nil {
		return err
	}

	key := redisKey(s.keyPrefix, KeyTypeExchangeGrant, signature)
	found, err := revokeExchangeGrantScript.Run(ctx, s.client, []string{key}, now.UnixMilli()).Int64()
	if err != nil {
		return fmt.Errorf("failed to revoke exchange grant: %w", err)
	}
	if found == 0 {
		return ErrNotFound
	}
	return nil
}

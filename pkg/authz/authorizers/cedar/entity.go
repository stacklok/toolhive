// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cedar provides authorization utilities using Cedar policies.
package cedar

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/cedar-policy/cedar-go/types"
)

// maxClaimNestingDepth caps how deeply nested maps/arrays in JWT claims are
// converted into Cedar records/sets. Claims can originate from an unverified
// upstream token (see parseUpstreamJWTClaims in core.go, which uses
// jwt.ParseUnverified), so recursion here has no other bound. Matches
// maxSchemaDepth in pkg/vmcp/composer/elicitation_handler.go for consistency.
const maxClaimNestingDepth = 10

const (
	// EntityTypeTHVGroup is the default Cedar entity type representing group membership.
	// It is used when ConfigOptions.GroupEntityType is empty. Principals are added as
	// children of group entities so that Cedar's `in` operator can evaluate
	// group-based policies (e.g. `principal in THVGroup::"engineering"`).
	EntityTypeTHVGroup cedar.EntityType = "THVGroup"
	// EntityTypeBackend represents a logical vMCP backend. Tools are added as
	// children of their originating backend for backend-scoped policies.
	EntityTypeBackend cedar.EntityType = "Backend"
)

// EntityFactory creates Cedar entities for authorization.
type EntityFactory struct {
	// groupEntityType is the Cedar entity type used for Client parent UIDs
	// synthesised from JWT group/role claims. Resolved at construction —
	// defaults to EntityTypeTHVGroup when the caller passes an empty string.
	groupEntityType cedar.EntityType
}

// NewEntityFactory creates a new entity factory. An empty groupEntityType
// falls back to EntityTypeTHVGroup so existing callers stay backward compatible.
func NewEntityFactory(groupEntityType cedar.EntityType) *EntityFactory {
	if groupEntityType == "" {
		groupEntityType = EntityTypeTHVGroup
	}
	return &EntityFactory{groupEntityType: groupEntityType}
}

// CreatePrincipalEntity creates a principal entity with the given ID, attributes,
// and optional parent entity UIDs.
// When no parents are provided, the entity has an empty parent set (backward compatible).
// NOTE: This replaces the previous groups []string parameter from 5c258a11.
// Callers are now responsible for building parent UIDs (see #4768).
func (*EntityFactory) CreatePrincipalEntity(
	principalType, principalID string,
	attributes map[string]interface{},
	parents ...cedar.EntityUID,
) (cedar.EntityUID, cedar.Entity) {
	uid := cedar.NewEntityUID(cedar.EntityType(principalType), cedar.String(principalID))
	attrs := convertMapToCedarRecord(attributes)

	entity := cedar.Entity{
		UID:        uid,
		Parents:    cedar.NewEntityUIDSet(parents...),
		Attributes: attrs,
		Tags:       cedar.NewRecord(cedar.RecordMap{}),
	}

	return uid, entity
}

// CreateActionEntity creates an action entity with the given ID and attributes.
func (*EntityFactory) CreateActionEntity(
	actionType, actionID string,
	attributes map[string]interface{},
) (cedar.EntityUID, cedar.Entity) {
	uid := cedar.NewEntityUID(cedar.EntityType(actionType), cedar.String(actionID))

	// Ensure operation attribute is set
	if attributes == nil {
		attributes = make(map[string]interface{})
	}
	attributes["operation"] = actionID

	attrs := convertMapToCedarRecord(attributes)

	entity := cedar.Entity{
		UID:        uid,
		Parents:    cedar.NewEntityUIDSet(),
		Attributes: attrs,
		Tags:       cedar.NewRecord(cedar.RecordMap{}),
	}

	return uid, entity
}

// CreateResourceEntity creates a resource entity with the given ID, attributes,
// and optional parent entity UIDs.
// When no parents are provided, the entity has an empty parent set (backward compatible).
func (*EntityFactory) CreateResourceEntity(
	resourceType, resourceID string,
	attributes map[string]interface{},
	parents ...cedar.EntityUID,
) (cedar.EntityUID, cedar.Entity) {
	uid := cedar.NewEntityUID(cedar.EntityType(resourceType), cedar.String(resourceID))

	// Ensure name attribute is set — but don't overwrite if the caller
	// already provided one (e.g. authorizeResourceRead sets name to the
	// original URI before sanitization).
	if attributes == nil {
		attributes = make(map[string]interface{})
	}
	if _, exists := attributes["name"]; !exists {
		attributes["name"] = resourceID
	}

	attrs := convertMapToCedarRecord(attributes)

	entity := cedar.Entity{
		UID:        uid,
		Parents:    cedar.NewEntityUIDSet(parents...),
		Attributes: attrs,
		Tags:       cedar.NewRecord(cedar.RecordMap{}),
	}

	return uid, entity
}

// CreateEntitiesForRequest creates entities for a specific authorization request.
// Groups are converted to parent UIDs (using the configured group entity type,
// default "THVGroup") on the principal entity so that Cedar's `in` operator works
// for group-based policies. Unlike the pre-refactor code, no separate group
// entities are inserted into the entity map — those must come from entities_json
// to preserve the role hierarchy.
//
// When serverName is non-empty, resource entities include an MCP parent UID so
// that server-scoped Cedar policies (e.g. `resource in MCP::"github"`) evaluate
// correctly via Cedar's `in` operator. When serverName is empty, resource
// entities have no MCP parent, preserving backward compatibility. Additional
// resource parent UIDs may be supplied for narrower scopes such as vMCP
// backends; their corresponding entities, if needed for hierarchy traversal,
// must come from entities_json.
func (f *EntityFactory) CreateEntitiesForRequest(
	principal, action, resource string,
	claimsMap map[string]interface{},
	attributes map[string]interface{},
	groups []string,
	serverName string,
	extraResourceParents ...cedar.EntityUID,
) (cedar.EntityMap, error) {
	// Parse principal, action, and resource
	principalType, principalID, err := parseCedarEntityID(principal)
	if err != nil {
		return nil, err
	}

	actionType, actionID, err := parseCedarEntityID(action)
	if err != nil {
		return nil, err
	}

	resourceType, resourceID, err := parseCedarEntityID(resource)
	if err != nil {
		return nil, err
	}

	// Create Cedar entities
	entities := make(cedar.EntityMap)

	// Build parent UIDs from groups so the principal's Parents set contains
	// references using the configured group entity type (default "THVGroup"),
	// needed for Cedar's `in` operator. Unlike the pre-refactor code, we do
	// NOT insert separate group entities into the entity map — those come from
	// entities_json and must not be overwritten (see merge-order hazard
	// described in the RFC). #4768 will restructure this further for full role
	// hierarchy support.
	parentUIDs := make([]cedar.EntityUID, 0, len(groups))
	for _, g := range groups {
		parentUIDs = append(parentUIDs, cedar.NewEntityUID(f.groupEntityType, cedar.String(g)))
	}

	principalUID, principalEntity := f.CreatePrincipalEntity(principalType, principalID, claimsMap, parentUIDs...)
	entities[principalUID] = principalEntity

	// Create action entity
	actionUID, actionEntity := f.CreateActionEntity(actionType, actionID, nil)
	entities[actionUID] = actionEntity

	// Build MCP parent for resource entity when serverName is set so that
	// server-scoped policies (e.g. resource in MCP::"github") can match.
	resourceParents := make([]cedar.EntityUID, 0, 1+len(extraResourceParents))
	if serverName != "" {
		resourceParents = append(resourceParents, cedar.NewEntityUID("MCP", cedar.String(serverName)))
	}
	resourceParents = append(resourceParents, extraResourceParents...)

	// Create resource entity
	resourceUID, resourceEntity := f.CreateResourceEntity(resourceType, resourceID, attributes, resourceParents...)
	entities[resourceUID] = resourceEntity

	return entities, nil
}

// convertMapToCedarRecord converts a Go map to a Cedar record. Nesting beyond
// maxClaimNestingDepth becomes an unrepresentableValue (see
// convertToCedarValueAtDepth). Only nil values are left out.
func convertMapToCedarRecord(data map[string]interface{}) cedar.Record {
	return convertMapToCedarRecordAtDepth(data, 0)
}

// convertMapToCedarRecordAtDepth is the depth-tracking implementation behind
// convertMapToCedarRecord.
func convertMapToCedarRecordAtDepth(data map[string]interface{}, depth int) cedar.Record {
	if data == nil {
		return cedar.NewRecord(cedar.RecordMap{})
	}

	recordMap := make(cedar.RecordMap)

	for k, v := range data {
		// Only a nil input yields a nil Cedar value; every other value is
		// either converted or replaced by an unrepresentableValue.
		cedarValue := convertToCedarValueAtDepth(v, depth)
		if cedarValue != nil {
			recordMap[cedar.String(k)] = cedarValue
		}
	}

	return cedar.NewRecord(recordMap)
}

// convertToCedarValueAtDepth converts a Go value to a Cedar value. Maps and
// arrays nested deeper than maxClaimNestingDepth become an
// unrepresentableValue rather than causing unbounded recursion, since claim
// values can originate from an unverified upstream JWT (see
// parseUpstreamJWTClaims in core.go). Conversion recursion stops at the limit;
// the marker's digest json.Marshals the remaining subtree, which is bounded by
// encoding/json's decode nesting limit and linear in its size. depth counts how
// many maps/arrays have already been unwrapped to reach v. It returns nil only
// when v is nil; values of an unsupported type become an unrepresentableValue.
func convertToCedarValueAtDepth(v interface{}, depth int) cedar.Value {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case bool:
		return convertBoolToCedar(val)
	case string:
		return cedar.String(val)
	case int:
		return cedar.Long(val)
	case int64:
		return cedar.Long(val)
	case float64:
		return convertFloatToCedar(val)
	case []interface{}:
		if depth > maxClaimNestingDepth {
			return tooDeepValue(v)
		}
		return convertInterfaceArrayToCedar(val, depth)
	case []string:
		return convertStringArrayToCedar(val)
	case map[string]interface{}:
		if depth > maxClaimNestingDepth {
			return tooDeepValue(v)
		}
		return convertMapToCedarRecordAtDepth(val, depth+1)
	default:
		return unrepresentableOf(unrepresentableUnsupported, v)
	}
}

// tooDeepValue returns the marker for a map or array nested past
// maxClaimNestingDepth and logs a warning. The converter also handles resource
// and context attributes, so the log says "attribute".
func tooDeepValue(v interface{}) cedar.Value {
	slog.Warn("attribute nested too deeply, replacing with marker",
		"max_depth", maxClaimNestingDepth)
	return unrepresentableOf(unrepresentableTooDeep, v)
}

// convertBoolToCedar converts a bool to a Cedar value.
func convertBoolToCedar(val bool) cedar.Value {
	if val {
		return cedar.True
	}
	return cedar.False
}

// convertFloatToCedar converts a JSON number to a Cedar decimal when a decimal
// holds it exactly, working from the shortest text that round-trips the float
// so the result does not depend on float multiplication or on the
// architecture. Any other number becomes a number marker carrying its text.
func convertFloatToCedar(val float64) cedar.Value {
	text := strconv.FormatFloat(val, 'f', -1, 64)
	parseText := func() string {
		if strings.Contains(text, ".") {
			return text
		}
		// types.ParseDecimal requires a decimal point.
		return text + ".0"
	}()
	if d, err := types.ParseDecimal(parseText); err == nil {
		return d
	}
	return numberMarker(text)
}

// convertInterfaceArrayToCedar converts an array of interfaces to a Cedar set.
// Null elements are dropped.
func convertInterfaceArrayToCedar(val []interface{}, depth int) cedar.Value {
	values := make([]cedar.Value, 0, len(val))
	for _, item := range val {
		if cedarItem := convertToCedarValueAtDepth(item, depth+1); cedarItem != nil {
			values = append(values, cedarItem)
		}
	}
	return cedar.NewSet(values...)
}

// convertStringArrayToCedar converts a string array to a Cedar set.
func convertStringArrayToCedar(val []string) cedar.Value {
	values := make([]cedar.Value, 0, len(val))
	for _, item := range val {
		values = append(values, cedar.String(item))
	}
	return cedar.NewSet(values...)
}

// Kinds recorded in an unrepresentableValue.
const (
	unrepresentableNumber      = "number"
	unrepresentableUnsupported = "unsupported"
	unrepresentableTooDeep     = "too_deep"
)

// numberNonce makes every number marker unique.
var numberNonce atomic.Int64

// unrepresentableValue marks a value that has no faithful Cedar form. It is a
// Record so that every typed operation a policy applies to it (decimal
// methods, < <= > >=, like, contains, ., in, is) is a type error, which
// IsAuthorized turns into deny. Reading the marker's own attributes
// (thv_unrepresentable, text, digest, nonce) works; any other attribute is an
// error. A number marker carries a unique nonce, so it never equals anything,
// not even another number marker: two different integers that round to the same
// float64 must not compare equal. Every other marker carries a digest of the
// value instead, so two of them are equal only when the values are, and a
// marker never equals a decimal, string or long. Without the digest,
// `context.arg_a != context.arg_b` on two different values would be false and
// let a forbid be skipped. Never drop the value instead: a missing attribute
// lets a `has`-guarded forbid be skipped.
func unrepresentableValue(kind, digest string) cedar.Value {
	return cedar.NewRecord(cedar.RecordMap{
		"thv_unrepresentable": cedar.String(kind),
		"digest":              cedar.String(digest),
	})
}

// numberMarker builds the marker for a number with no exact Cedar decimal. text
// is kept for debugging.
func numberMarker(text string) cedar.Value {
	return cedar.NewRecord(cedar.RecordMap{
		"thv_unrepresentable": cedar.String(unrepresentableNumber),
		"text":                cedar.String(text),
		"nonce":               cedar.Long(numberNonce.Add(1)),
	})
}

// unrepresentableOf builds the marker for a non-number value v, with a digest
// of its JSON form. Go sorts map keys, so the digest is deterministic. A value
// JSON cannot encode is hashed by its Go type and representation instead.
func unrepresentableOf(kind string, v interface{}) cedar.Value {
	raw, err := json.Marshal(v)
	if err != nil {
		// Pointer-bearing values would hash differently between runs, but
		// request input never contains pointers.
		raw = []byte(fmt.Sprintf("%T:%#v", v, v))
	}
	sum := sha256.Sum256(raw)
	return unrepresentableValue(kind, hex.EncodeToString(sum[:]))
}

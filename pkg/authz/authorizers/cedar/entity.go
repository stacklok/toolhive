// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cedar provides authorization utilities using Cedar policies.
package cedar

import (
	cedar "github.com/cedar-policy/cedar-go"
)

// EntityTypeTHVGroup is the Cedar entity type representing group membership.
// Principals are added as children of THVGroup entities so that Cedar's `in`
// operator can evaluate group-based policies (e.g. `principal in THVGroup::"engineering"`).
const EntityTypeTHVGroup cedar.EntityType = "THVGroup"

// EntityFactory creates Cedar entities for authorization.
type EntityFactory struct{}

// NewEntityFactory creates a new entity factory.
func NewEntityFactory() *EntityFactory {
	return &EntityFactory{}
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
// Groups are converted to THVGroup parent UIDs on the principal entity so that
// Cedar's `in` operator works for group-based policies. Unlike the pre-refactor
// code, no separate THVGroup entities are inserted into the entity map — those
// must come from entities_json to preserve the role hierarchy.
func (f *EntityFactory) CreateEntitiesForRequest(
	principal, action, resource string,
	claimsMap map[string]interface{},
	attributes map[string]interface{},
	groups []string,
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
	// THVGroup references (needed for Cedar's `in` operator). Unlike the
	// pre-refactor code, we do NOT insert separate THVGroup entities into the
	// entity map — those come from entities_json and must not be overwritten
	// (see merge-order hazard described in the RFC). #4768 will restructure
	// this further for full role hierarchy support.
	parentUIDs := make([]cedar.EntityUID, 0, len(groups))
	for _, g := range groups {
		parentUIDs = append(parentUIDs, cedar.NewEntityUID(EntityTypeTHVGroup, cedar.String(g)))
	}

	principalUID, principalEntity := f.CreatePrincipalEntity(principalType, principalID, claimsMap, parentUIDs...)
	entities[principalUID] = principalEntity

	// Create action entity
	actionUID, actionEntity := f.CreateActionEntity(actionType, actionID, nil)
	entities[actionUID] = actionEntity

	// Create resource entity
	resourceUID, resourceEntity := f.CreateResourceEntity(resourceType, resourceID, attributes)
	entities[resourceUID] = resourceEntity

	return entities, nil
}

// convertMapToCedarRecord converts a Go map to a Cedar record.
func convertMapToCedarRecord(data map[string]interface{}) cedar.Record {
	if data == nil {
		return cedar.NewRecord(cedar.RecordMap{})
	}

	recordMap := make(cedar.RecordMap)

	for k, v := range data {
		// Convert Go values to Cedar values
		cedarValue := convertToCedarValue(v)
		if cedarValue != nil {
			recordMap[cedar.String(k)] = cedarValue
		}
	}

	return cedar.NewRecord(recordMap)
}

// convertToCedarValue converts a Go value to a Cedar value.
func convertToCedarValue(v interface{}) cedar.Value {
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
		return convertInterfaceArrayToCedar(val)
	case []string:
		return convertStringArrayToCedar(val)
	default:
		// Skip unsupported types
		return nil
	}
}

// convertBoolToCedar converts a bool to a Cedar value.
func convertBoolToCedar(val bool) cedar.Value {
	if val {
		return cedar.True
	}
	return cedar.False
}

// convertFloatToCedar converts a float64 to a Cedar decimal value.
func convertFloatToCedar(val float64) cedar.Value {
	decimalVal, err := cedar.NewDecimalFromFloat(val)
	if err != nil {
		return nil
	}
	return decimalVal
}

// convertInterfaceArrayToCedar converts an array of interfaces to a Cedar set.
func convertInterfaceArrayToCedar(val []interface{}) cedar.Value {
	values := make([]cedar.Value, 0, len(val))
	for _, item := range val {
		cedarItem := convertArrayItemToCedar(item)
		if cedarItem != nil {
			values = append(values, cedarItem)
		}
	}
	return cedar.NewSet(values...)
}

// convertArrayItemToCedar converts an array item to a Cedar value.
func convertArrayItemToCedar(item interface{}) cedar.Value {
	switch itemVal := item.(type) {
	case string:
		return cedar.String(itemVal)
	case bool:
		return convertBoolToCedar(itemVal)
	case int:
		return cedar.Long(itemVal)
	case int64:
		return cedar.Long(itemVal)
	case float64:
		return convertFloatToCedar(itemVal)
	default:
		return nil
	}
}

// convertStringArrayToCedar converts a string array to a Cedar set.
func convertStringArrayToCedar(val []string) cedar.Value {
	values := make([]cedar.Value, 0, len(val))
	for _, item := range val {
		values = append(values, cedar.String(item))
	}
	return cedar.NewSet(values...)
}

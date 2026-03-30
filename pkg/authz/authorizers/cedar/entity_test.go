// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreatePrincipalEntity_WithGroups tests that CreatePrincipalEntity
// correctly builds group parent entities and populates the Parents set.
func TestCreatePrincipalEntity_WithGroups(t *testing.T) {
	t.Parallel()

	factory := NewEntityFactory()

	tests := []struct {
		name        string
		groups      []string
		wantParents int
		wantGroups  int
	}{
		{
			name:        "no_groups",
			groups:      nil,
			wantParents: 0,
			wantGroups:  0,
		},
		{
			name:        "empty_groups_slice",
			groups:      []string{},
			wantParents: 0,
			wantGroups:  0,
		},
		{
			name:        "single_group",
			groups:      []string{"engineering"},
			wantParents: 1,
			wantGroups:  1,
		},
		{
			name:        "multiple_groups",
			groups:      []string{"engineering", "platform", "security"},
			wantParents: 3,
			wantGroups:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uid, entity, groupEntities := factory.CreatePrincipalEntity(
				"Client", "user1",
				map[string]interface{}{"name": "Test User"},
				tt.groups,
			)

			// UID must be correct.
			assert.Equal(t, "Client", string(uid.Type))
			assert.Equal(t, "user1", string(uid.ID))

			// Entity UID must match.
			assert.Equal(t, uid, entity.UID)

			// Parents set must contain one entry per group.
			assert.Equal(t, tt.wantParents, entity.Parents.Len(),
				"expected %d parent(s) in entity.Parents", tt.wantParents)

			// Returned group-entity slice must have the right length.
			assert.Len(t, groupEntities, tt.wantGroups)

			// Each group entity must be a THVGroup with the correct ID.
			for i, g := range tt.groups {
				ge := groupEntities[i]
				assert.Equal(t, "THVGroup", string(ge.UID.Type))
				assert.Equal(t, g, string(ge.UID.ID))
				// Group entities have no parents of their own.
				assert.Equal(t, 0, ge.Parents.Len())

				// The principal's Parents set must contain this group UID.
				assert.True(t, entity.Parents.Contains(ge.UID),
					"expected parent %v to be in entity.Parents", ge.UID)
			}
		})
	}
}

// TestCreateEntitiesForRequest_WithGroups verifies that group entities are
// added to the entity map when groups are supplied, and that the principal
// entity's Parents set references them.
func TestCreateEntitiesForRequest_WithGroups(t *testing.T) {
	t.Parallel()

	factory := NewEntityFactory()

	tests := []struct {
		name          string
		groups        []string
		wantEntityLen int // principal + action + resource + N groups
		wantGroupUIDs []string
	}{
		{
			name:          "no_groups",
			groups:        nil,
			wantEntityLen: 3,
			wantGroupUIDs: nil,
		},
		{
			name:          "one_group",
			groups:        []string{"engineering"},
			wantEntityLen: 4,
			wantGroupUIDs: []string{"engineering"},
		},
		{
			name:          "two_groups",
			groups:        []string{"engineering", "platform"},
			wantEntityLen: 5,
			wantGroupUIDs: []string{"engineering", "platform"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entities, err := factory.CreateEntitiesForRequest(
				"Client::user1",
				"Action::call_tool",
				"Tool::weather",
				map[string]interface{}{"sub": "user1"},
				map[string]interface{}{"name": "weather"},
				tt.groups,
			)
			require.NoError(t, err)
			require.NotNil(t, entities)

			assert.Len(t, entities, tt.wantEntityLen)

			principalUID := cedar.NewEntityUID("Client", cedar.String("user1"))
			principalEntity, found := entities[principalUID]
			require.True(t, found, "principal entity not found in map")

			// Verify group entities are present and principal has them as parents.
			for _, g := range tt.wantGroupUIDs {
				groupUID := cedar.NewEntityUID("THVGroup", cedar.String(g))

				_, groupFound := entities[groupUID]
				assert.True(t, groupFound, "THVGroup::%q entity not found in map", g)

				assert.True(t, principalEntity.Parents.Contains(groupUID),
					"expected THVGroup::%q in principal.Parents", g)
			}
		})
	}
}

// TestCreateCedarEntities tests the createCedarEntities function.
func TestCreateCedarEntities(t *testing.T) {
	t.Parallel()
	// Test cases
	testCases := []struct {
		name       string
		principal  string
		action     string
		resource   string
		claimsMap  map[string]interface{}
		attributes map[string]interface{}
		expectErr  bool
	}{
		{
			name:      "Valid entities",
			principal: "Client::user123",
			action:    "Action::call_tool",
			resource:  "Tool::weather",
			claimsMap: map[string]interface{}{
				"claim_sub":   "user123",
				"claim_name":  "John Doe",
				"claim_roles": []string{"user", "admin"},
			},
			attributes: map[string]interface{}{
				"name":      "weather",
				"operation": "call",
				"feature":   "tool",
			},
			expectErr: false,
		},
		{
			name:       "Invalid principal format",
			principal:  "user123",
			action:     "Action::call_tool",
			resource:   "Tool::weather",
			claimsMap:  map[string]interface{}{},
			attributes: map[string]interface{}{},
			expectErr:  true,
		},
		{
			name:       "Invalid action format",
			principal:  "Client::user123",
			action:     "call_tool",
			resource:   "Tool::weather",
			claimsMap:  map[string]interface{}{},
			attributes: map[string]interface{}{},
			expectErr:  true,
		},
		{
			name:       "Invalid resource format",
			principal:  "Client::user123",
			action:     "Action::call_tool",
			resource:   "weather",
			claimsMap:  map[string]interface{}{},
			attributes: map[string]interface{}{},
			expectErr:  true,
		},
		{
			name:      "With complex attributes",
			principal: "Client::user123",
			action:    "Action::call_tool",
			resource:  "Tool::calculator",
			claimsMap: map[string]interface{}{
				"claim_sub":             "user123",
				"claim_name":            "John Doe",
				"claim_roles":           []string{"user", "admin"},
				"claim_clearance_level": 5,
			},
			attributes: map[string]interface{}{
				"name":          "calculator",
				"operation":     "call",
				"feature":       "tool",
				"arg_operation": "add",
				"arg_value1":    5,
				"arg_value2":    10,
				"tags":          []string{"math", "utility"},
				"priority":      1,
				"enabled":       true,
			},
			expectErr: false,
		},
	}

	// Run test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create entity factory
			factory := NewEntityFactory()

			// Create Cedar entities (no groups for these test cases)
			entities, err := factory.CreateEntitiesForRequest(tc.principal, tc.action, tc.resource, tc.claimsMap, tc.attributes, nil)

			// Check error expectations
			if tc.expectErr {
				assert.Error(t, err, "Expected an error but got none")
				assert.Nil(t, entities, "Expected nil entities when error occurs")
				return
			}

			assert.NoError(t, err, "Unexpected error: %v", err)
			assert.NotNil(t, entities, "Entities should not be nil")

			// Check that we have three entities (principal, action, resource)
			assert.Len(t, entities, 3, "Expected three entities")

			// Basic validation of entity structure
			for _, entity := range entities {
				// Each entity should have UID, Attributes, and Parents fields
				assert.NotNil(t, entity.UID, "Entity should have a UID field")
				assert.NotNil(t, entity.Attributes, "Entity should have an Attributes field")
				assert.NotNil(t, entity.Parents, "Entity should have a Parents field")
			}

			// Verify that the principal entity has the correct type and ID
			if !tc.expectErr {
				principalType, principalID, err := parseCedarEntityID(tc.principal)
				require.NoError(t, err, "Failed to parse principal ID")
				principalUID := cedar.NewEntityUID(cedar.EntityType(principalType), cedar.String(principalID))
				principalEntity, found := entities[principalUID]
				assert.True(t, found, "Principal entity not found")
				assert.Equal(t, principalType, string(principalEntity.UID.Type), "Principal type does not match")
				assert.Equal(t, principalID, string(principalEntity.UID.ID), "Principal ID does not match")

				// Verify that the action entity has the correct type and ID
				actionType, actionID, err := parseCedarEntityID(tc.action)
				require.NoError(t, err, "Failed to parse action ID")
				actionUID := cedar.NewEntityUID(cedar.EntityType(actionType), cedar.String(actionID))
				actionEntity, found := entities[actionUID]
				assert.True(t, found, "Action entity not found")
				assert.Equal(t, actionType, string(actionEntity.UID.Type), "Action type does not match")
				assert.Equal(t, actionID, string(actionEntity.UID.ID), "Action ID does not match")

				// Verify that the resource entity has the correct type and ID
				resourceType, resourceID, err := parseCedarEntityID(tc.resource)
				require.NoError(t, err, "Failed to parse resource ID")
				resourceUID := cedar.NewEntityUID(cedar.EntityType(resourceType), cedar.String(resourceID))
				resourceEntity, found := entities[resourceUID]
				assert.True(t, found, "Resource entity not found")
				assert.Equal(t, resourceType, string(resourceEntity.UID.Type), "Resource type does not match")
				assert.Equal(t, resourceID, string(resourceEntity.UID.ID), "Resource ID does not match")

				// Verify that the action entity has the operation attribute
				operationValue, found := actionEntity.Attributes.Get(cedar.String("operation"))
				assert.True(t, found, "Operation attribute not found")
				assert.Equal(t, actionID, string(operationValue.(cedar.String)), "Action operation attribute does not match")

				// Verify that the resource entity has the name attribute
				_, found = resourceEntity.Attributes.Get(cedar.String("name"))
				assert.True(t, found, "Resource name attribute not found")

				// Verify that JWT claims are added to the principal entity
				if len(tc.claimsMap) > 0 {
					for k, v := range tc.claimsMap {
						claimValue, found := principalEntity.Attributes.Get(cedar.String(k))
						assert.True(t, found, "Claim %s not found in principal entity", k)

						// For string claims, we can directly compare the values
						if strVal, ok := v.(string); ok {
							assert.Equal(t, strVal, string(claimValue.(cedar.String)), "Claim %s value does not match", k)
						}
					}
				}
			}
		})
	}
}

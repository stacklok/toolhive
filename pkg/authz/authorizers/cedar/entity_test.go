// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreatePrincipalEntity_Parents tests that CreatePrincipalEntity correctly
// populates the Parents set from variadic parent UIDs.
func TestCreatePrincipalEntity_Parents(t *testing.T) {
	t.Parallel()

	factory := NewEntityFactory()

	groupUID := cedar.NewEntityUID(EntityTypeTHVGroup, cedar.String("engineering"))
	roleUID := cedar.NewEntityUID("THVRole", cedar.String("admin"))

	tests := []struct {
		name        string
		parents     []cedar.EntityUID
		wantParents int
	}{
		{
			name:        "no_parents",
			parents:     nil,
			wantParents: 0,
		},
		{
			name:        "single_parent",
			parents:     []cedar.EntityUID{groupUID},
			wantParents: 1,
		},
		{
			name:        "multiple_parents",
			parents:     []cedar.EntityUID{groupUID, roleUID},
			wantParents: 2,
		},
		{
			name:        "duplicate_parents_are_deduplicated",
			parents:     []cedar.EntityUID{groupUID, groupUID},
			wantParents: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uid, entity := factory.CreatePrincipalEntity(
				"Client", "user1",
				map[string]interface{}{"name": "Test User"},
				tt.parents...,
			)

			// UID must be correct.
			assert.Equal(t, "Client", string(uid.Type))
			assert.Equal(t, "user1", string(uid.ID))

			// Entity UID must match.
			assert.Equal(t, uid, entity.UID)

			// Parents set must contain exactly the supplied parents.
			assert.Equal(t, tt.wantParents, entity.Parents.Len(),
				"expected %d parent(s) in entity.Parents", tt.wantParents)

			for _, p := range tt.parents {
				assert.True(t, entity.Parents.Contains(p),
					"expected parent %v to be in entity.Parents", p)
			}
		})
	}
}

// TestCreatePrincipalEntity_NoGroupEntities is a regression test verifying that
// CreatePrincipalEntity does NOT create THVGroup entities internally — callers
// are responsible for constructing parent UIDs (fixes merge-order hazard from 5c258a11).
func TestCreatePrincipalEntity_NoGroupEntities(t *testing.T) {
	t.Parallel()

	factory := NewEntityFactory()

	// Pass a THVGroup parent UID — the function must NOT return extra entities.
	groupUID := cedar.NewEntityUID(EntityTypeTHVGroup, cedar.String("engineering"))
	uid, entity := factory.CreatePrincipalEntity("Client", "user1", nil, groupUID)

	assert.Equal(t, "Client", string(uid.Type))
	assert.Equal(t, 1, entity.Parents.Len())
	assert.True(t, entity.Parents.Contains(groupUID))
	// The function returns only (uid, entity) — no group entity slice.
}

// TestCreateResourceEntity_Parents tests that CreateResourceEntity correctly
// populates the Parents set from variadic parent UIDs.
func TestCreateResourceEntity_Parents(t *testing.T) {
	t.Parallel()

	factory := NewEntityFactory()

	mcpUID := cedar.NewEntityUID("MCP", cedar.String("server-a"))
	orgUID := cedar.NewEntityUID("Org", cedar.String("stacklok"))

	tests := []struct {
		name        string
		parents     []cedar.EntityUID
		wantParents int
	}{
		{
			name:        "no_parents",
			parents:     nil,
			wantParents: 0,
		},
		{
			name:        "single_parent",
			parents:     []cedar.EntityUID{mcpUID},
			wantParents: 1,
		},
		{
			name:        "multiple_parents",
			parents:     []cedar.EntityUID{mcpUID, orgUID},
			wantParents: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uid, entity := factory.CreateResourceEntity(
				"Tool", "weather",
				map[string]interface{}{"description": "Weather tool"},
				tt.parents...,
			)

			// UID must be correct.
			assert.Equal(t, "Tool", string(uid.Type))
			assert.Equal(t, "weather", string(uid.ID))

			// Entity UID must match.
			assert.Equal(t, uid, entity.UID)

			// Parents set must contain exactly the supplied parents.
			assert.Equal(t, tt.wantParents, entity.Parents.Len(),
				"expected %d parent(s) in entity.Parents", tt.wantParents)

			for _, p := range tt.parents {
				assert.True(t, entity.Parents.Contains(p),
					"expected parent %v to be in entity.Parents", p)
			}

			// Name attribute must always be set.
			nameVal, found := entity.Attributes.Get(cedar.String("name"))
			assert.True(t, found, "name attribute must be set")
			assert.Equal(t, "weather", string(nameVal.(cedar.String)))
		})
	}
}

// TestCreateEntitiesForRequest_GroupsAsParents verifies that
// CreateEntitiesForRequest sets THVGroup parent UIDs on the principal but
// does NOT insert separate THVGroup entities into the entity map (fixing
// the merge-order hazard where dynamic group entities overwrote static ones).
func TestCreateEntitiesForRequest_GroupsAsParents(t *testing.T) {
	t.Parallel()

	factory := NewEntityFactory()

	tests := []struct {
		name            string
		groups          []string
		wantEntityCount int // always 3: principal + action + resource
		wantParentCount int
	}{
		{
			name:            "no_groups",
			groups:          nil,
			wantEntityCount: 3,
			wantParentCount: 0,
		},
		{
			name:            "one_group",
			groups:          []string{"engineering"},
			wantEntityCount: 3,
			wantParentCount: 1,
		},
		{
			name:            "two_groups",
			groups:          []string{"engineering", "platform"},
			wantEntityCount: 3,
			wantParentCount: 2,
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

			// Entity map must contain only principal + action + resource (no THVGroup entries).
			assert.Len(t, entities, tt.wantEntityCount)
			for uid := range entities {
				assert.NotEqual(t, string(EntityTypeTHVGroup), string(uid.Type),
					"THVGroup entity should not be in the entity map")
			}

			// Principal's Parents set must reference THVGroup UIDs.
			principalUID := cedar.NewEntityUID("Client", cedar.String("user1"))
			principalEntity, found := entities[principalUID]
			require.True(t, found, "principal entity not found in map")
			assert.Equal(t, tt.wantParentCount, principalEntity.Parents.Len())

			for _, g := range tt.groups {
				groupUID := cedar.NewEntityUID(EntityTypeTHVGroup, cedar.String(g))
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cedar provides authorization utilities using Cedar policies.
package cedar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// ConfigType is the configuration type identifier for Cedar authorization.
const ConfigType = "cedarv1"

func init() {
	// Register the Cedar authorizer factory with the authorizers registry.
	authorizers.Register(ConfigType, &Factory{})
}

// Config represents the complete authorization configuration file structure
// for Cedar authorization. This includes the common version/type fields plus
// the Cedar-specific "cedar" field. This maintains backwards compatibility
// with the v1.0 configuration schema.
type Config struct {
	Version string         `json:"version"`
	Type    string         `json:"type"`
	Options *ConfigOptions `json:"cedar"`
}

// ExtractConfig extracts the Cedar configuration from an authorizers.Config.
// This is useful for tests and other code that needs to inspect the Cedar configuration
// after it has been loaded into the generic Config structure.
// To access the Cedar-specific options (policies, entities), use the returned Config's Cedar field.
func ExtractConfig(authzConfig *authorizers.Config) (*Config, error) {
	if authzConfig == nil {
		return nil, fmt.Errorf("config is nil")
	}
	rawConfig := authzConfig.RawConfig()
	if len(rawConfig) == 0 {
		return nil, fmt.Errorf("config has no raw data")
	}

	var config Config
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	if config.Options == nil {
		return nil, fmt.Errorf("cedar config is nil")
	}
	return &config, nil
}

// Factory implements the authorizers.AuthorizerFactory interface for Cedar.
type Factory struct{}

// ValidateConfig validates the Cedar-specific configuration.
// It receives the full raw config and extracts the Cedar-specific portion.
func (*Factory) ValidateConfig(rawConfig json.RawMessage) error {
	var config Config
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}

	if config.Options == nil {
		return fmt.Errorf("cedar configuration is required (missing 'cedar' field)")
	}

	if len(config.Options.Policies) == 0 {
		return fmt.Errorf("at least one policy is required for Cedar authorization")
	}

	return nil
}

// CreateAuthorizer creates a Cedar Authorizer from the configuration.
// It receives the full raw config and extracts the Cedar-specific portion.
func (*Factory) CreateAuthorizer(rawConfig json.RawMessage, _ string) (authorizers.Authorizer, error) {
	var config Config
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return nil, fmt.Errorf("failed to parse configuration: %w", err)
	}

	if config.Options == nil {
		return nil, fmt.Errorf("cedar configuration is required (missing 'cedar' field)")
	}

	return NewCedarAuthorizer(*config.Options)
}

// Common errors for Cedar authorization
var (
	ErrNoPolicies           = errors.New("no policies loaded")
	ErrInvalidPolicy        = errors.New("invalid policy")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrMissingPrincipal     = errors.New("missing principal")
	ErrMissingAction        = errors.New("missing action")
	ErrMissingResource      = errors.New("missing resource")
	ErrFailedToLoadEntities = errors.New("failed to load entities")
)

// ClientIDContextKey is the key used to store client ID in the context.
type ClientIDContextKey struct{}

// Authorizer authorizes MCP operations using Cedar policies.
type Authorizer struct {
	// Cedar policy set
	policySet *cedar.PolicySet
	// Cedar entities
	entities cedar.EntityMap
	// Entity factory for creating entities
	entityFactory *EntityFactory
	// Mutex for thread safety
	mu sync.RWMutex
}

// ConfigOptions represents the Cedar-specific authorization configuration options.
type ConfigOptions struct {
	// Policies is a list of Cedar policy strings
	Policies []string `json:"policies" yaml:"policies"`

	// EntitiesJSON is the JSON string representing Cedar entities
	EntitiesJSON string `json:"entities_json" yaml:"entities_json"`
}

// NewCedarAuthorizer creates a new Cedar authorizer.
func NewCedarAuthorizer(options ConfigOptions) (authorizers.Authorizer, error) {
	authorizer := &Authorizer{
		policySet:     cedar.NewPolicySet(),
		entities:      cedar.EntityMap{},
		entityFactory: NewEntityFactory(),
	}

	// Load policies
	if len(options.Policies) == 0 {
		return nil, ErrNoPolicies
	}

	for i, policyStr := range options.Policies {
		var policy cedar.Policy
		if err := policy.UnmarshalCedar([]byte(policyStr)); err != nil {
			return nil, fmt.Errorf("failed to parse policy %d: %w", i, err)
		}

		policyID := cedar.PolicyID(fmt.Sprintf("policy%d", i))
		authorizer.policySet.Add(policyID, &policy)
	}

	// Load entities if provided
	if options.EntitiesJSON != "" {
		if err := json.Unmarshal([]byte(options.EntitiesJSON), &authorizer.entities); err != nil {
			return nil, fmt.Errorf("failed to parse entities JSON: %w", err)
		}
	}

	return authorizer, nil
}

// UpdatePolicies updates the Cedar policies.
func (a *Authorizer) UpdatePolicies(policies []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(policies) == 0 {
		return ErrNoPolicies
	}

	newPolicySet := cedar.NewPolicySet()

	for i, policyStr := range policies {
		var policy cedar.Policy
		if err := policy.UnmarshalCedar([]byte(policyStr)); err != nil {
			return fmt.Errorf("failed to parse policy %d: %w", i, err)
		}

		policyID := cedar.PolicyID(fmt.Sprintf("policy%d", i))
		newPolicySet.Add(policyID, &policy)
	}

	a.policySet = newPolicySet
	return nil
}

// UpdateEntities updates the Cedar entities.
func (a *Authorizer) UpdateEntities(entitiesJSON string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var newEntities cedar.EntityMap
	if err := json.Unmarshal([]byte(entitiesJSON), &newEntities); err != nil {
		return fmt.Errorf("failed to parse entities JSON: %w", err)
	}

	a.entities = newEntities
	return nil
}

// AddEntity adds or updates an entity in the authorizer's entity store.
func (a *Authorizer) AddEntity(entity cedar.Entity) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.entities[entity.UID] = entity
}

// RemoveEntity removes an entity from the authorizer's entity store.
func (a *Authorizer) RemoveEntity(uid cedar.EntityUID) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.entities, uid)
}

// GetEntity retrieves an entity from the authorizer's entity store.
func (a *Authorizer) GetEntity(uid cedar.EntityUID) (cedar.Entity, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	entity, found := a.entities[uid]
	return entity, found
}

// GetEntityFactory returns the entity factory associated with this authorizer.
func (a *Authorizer) GetEntityFactory() *EntityFactory {
	return a.entityFactory
}

// IsAuthorized checks if a request is authorized.
// This is the core authorization method that all other authorization methods use.
// It takes:
// - principal: The entity making the request (e.g., "Client::vscode_extension_123")
// - action: The operation being performed (e.g., "Action::call_tool")
// - resource: The object being accessed (e.g., "Tool::weather")
// - context: Additional information about the request
// - entities: Optional Cedar entity map with attributes
func (a *Authorizer) IsAuthorized(
	principal, action, resource string,
	contextMap map[string]interface{},
	entities ...cedar.EntityMap,
) (bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if principal == "" {
		return false, ErrMissingPrincipal
	}

	if action == "" {
		return false, ErrMissingAction
	}

	if resource == "" {
		return false, ErrMissingResource
	}

	// Parse principal, action, and resource
	principalType, principalID, err := parseCedarEntityID(principal)
	if err != nil {
		return false, err
	}

	actionType, actionID, err := parseCedarEntityID(action)
	if err != nil {
		return false, err
	}

	resourceType, resourceID, err := parseCedarEntityID(resource)
	if err != nil {
		return false, err
	}

	// Create context record
	contextRecord := convertMapToCedarRecord(contextMap)

	// Create Cedar request
	req := cedar.Request{
		Principal: cedar.NewEntityUID(cedar.EntityType(principalType), cedar.String(principalID)),
		Action:    cedar.NewEntityUID(cedar.EntityType(actionType), cedar.String(actionID)),
		Resource:  cedar.NewEntityUID(cedar.EntityType(resourceType), cedar.String(resourceID)),
		Context:   contextRecord,
	}

	// Use the provided entities if available, otherwise use the default entities
	entityMap := a.entities
	if len(entities) > 0 && entities[0] != nil {
		// Merge the request entities with the default entities
		// This allows policies to reference both the request-specific entities
		// and any global entities defined in the authorizer
		mergedEntities := make(cedar.EntityMap)
		for k, v := range a.entities {
			mergedEntities[k] = v
		}
		for k, v := range entities[0] {
			mergedEntities[k] = v
		}

		entityMap = mergedEntities
	}

	// Debug logging for authorization
	slog.Debug("Cedar authorization check",
		"principal", req.Principal, "action", req.Action, "resource", req.Resource)
	slog.Debug("Cedar context", "context", req.Context)

	// Check authorization
	decision, diagnostic := cedar.Authorize(a.policySet, entityMap, req)

	// Log the decision
	slog.Debug("Cedar decision", "decision", decision, "diagnostic", diagnostic)

	// Cedar's Authorize returns a Decision and a Diagnostic
	// Check if the Diagnostic contains any errors
	if len(diagnostic.Errors) > 0 {
		return false, fmt.Errorf("authorization error: %v", diagnostic.Errors)
	}
	return decision == cedar.Allow, nil
}

// extractClientIDFromClaims extracts the client ID from JWT claims.
// By default, it uses the "sub" (subject) claim as the client ID.
// This can be customized based on your JWT token structure.
func extractClientIDFromClaims(claims jwt.MapClaims) (string, bool) {
	// Use the GetSubject method to safely extract the "sub" claim
	sub, err := claims.GetSubject()
	if err != nil || sub == "" {
		return "", false
	}

	return sub, true
}

// preprocessClaims adds a "claim_" prefix to all claim keys.
// This makes it clear which values are from the JWT claims.
func preprocessClaims(claims jwt.MapClaims) map[string]interface{} {
	preprocessed := make(map[string]interface{})
	for k, v := range claims {
		claimKey := fmt.Sprintf("claim_%s", k)
		preprocessed[claimKey] = v
	}
	return preprocessed
}

// preprocessArguments adds an "arg_" prefix to all argument keys.
// For complex types, it just notes their presence with an "_present" suffix.
func preprocessArguments(arguments map[string]interface{}) map[string]interface{} {
	if arguments == nil {
		return nil
	}

	preprocessed := make(map[string]interface{})
	for k, v := range arguments {
		argKey := fmt.Sprintf("arg_%s", k)
		switch val := v.(type) {
		case string, bool, int, int64, float64:
			preprocessed[argKey] = val
		default:
			// For complex types, just note their presence
			preprocessed[argKey+"_present"] = true
		}
	}
	return preprocessed
}

// mergeContexts merges multiple context maps into a single map.
// Later maps override earlier maps if there are key conflicts.
func mergeContexts(contextMaps ...map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{})
	for _, ctxMap := range contextMaps {
		if ctxMap == nil {
			continue
		}
		for k, v := range ctxMap {
			merged[k] = v
		}
	}
	return merged
}

// authorizeToolCall authorizes a tool call operation.
// This method is used when a client tries to call a specific tool.
// It checks if the client is authorized to call the tool with the given context.
func (a *Authorizer) authorizeToolCall(
	clientID, toolName string,
	claimsMap map[string]interface{},
	attrsMap map[string]interface{},
) (bool, error) {
	// Extract principal from client ID
	principal := fmt.Sprintf("Client::%s", clientID)

	// Action is to call a tool
	action := "Action::call_tool"

	// Resource is the tool being called
	resource := fmt.Sprintf("Tool::%s", toolName)

	// Create attributes for the entities
	attributes := mergeContexts(map[string]interface{}{
		"name":      toolName,
		"operation": "call",
		"feature":   "tool",
	}, attrsMap)

	// Create Cedar entities
	entities, err := a.entityFactory.CreateEntitiesForRequest(principal, action, resource, claimsMap, attributes)
	if err != nil {
		return false, fmt.Errorf("failed to create Cedar entities: %w", err)
	}

	contextMap := mergeContexts(claimsMap, attrsMap)

	// Check authorization with entities
	return a.IsAuthorized(principal, action, resource, contextMap, entities)
}

// authorizePromptGet authorizes a prompt get operation.
// This method is used when a client tries to get a specific prompt.
// It checks if the client is authorized to access the prompt with the given context.
func (a *Authorizer) authorizePromptGet(
	clientID, promptName string,
	claimsMap map[string]interface{},
	attrsMap map[string]interface{},
) (bool, error) {
	// Extract principal from client ID
	principal := fmt.Sprintf("Client::%s", clientID)

	// Action is to get a prompt
	action := "Action::get_prompt"

	// Resource is the prompt being accessed
	resource := fmt.Sprintf("Prompt::%s", promptName)

	// Create attributes for the entities
	attributes := mergeContexts(map[string]interface{}{
		"name":      promptName,
		"operation": "get",
		"feature":   "prompt",
	}, attrsMap)

	// Create Cedar entities
	entities, err := a.entityFactory.CreateEntitiesForRequest(principal, action, resource, claimsMap, attributes)
	if err != nil {
		return false, fmt.Errorf("failed to create Cedar entities: %w", err)
	}

	contextMap := mergeContexts(claimsMap, attrsMap)

	// Check authorization with entities
	return a.IsAuthorized(principal, action, resource, contextMap, entities)
}

// authorizeResourceRead authorizes a resource read operation.
// This method is used when a client tries to read a specific resource.
// It checks if the client is authorized to read the resource.
func (a *Authorizer) authorizeResourceRead(
	clientID, resourceURI string,
	claimsMap map[string]interface{},
	attrsMap map[string]interface{},
) (bool, error) {
	// Extract principal from client ID
	principal := fmt.Sprintf("Client::%s", clientID)

	// Action is to read a resource
	action := "Action::read_resource"

	// Resource is the resource being accessed
	// Use the URI as the resource ID, but sanitize it for Cedar
	sanitizedURI := sanitizeURIForCedar(resourceURI)
	resource := fmt.Sprintf("Resource::%s", sanitizedURI)

	// Create attributes for the entities
	attributes := mergeContexts(map[string]interface{}{
		"uri":       resourceURI,
		"operation": "read",
		"feature":   "resource",
	}, attrsMap)

	// Create Cedar entities
	entities, err := a.entityFactory.CreateEntitiesForRequest(principal, action, resource, claimsMap, attributes)
	if err != nil {
		return false, fmt.Errorf("failed to create Cedar entities: %w", err)
	}

	contextMap := mergeContexts(claimsMap, attrsMap)

	// Check authorization with entities
	return a.IsAuthorized(principal, action, resource, contextMap, entities)
}

// authorizeFeatureList authorizes a list operation for a feature.
// This method is used when a client tries to list available tools, prompts, or resources.
// It checks if the client is authorized to list the specified feature type.
func (a *Authorizer) authorizeFeatureList(
	clientID string,
	feature authorizers.MCPFeature,
	claimsMap map[string]interface{},
	attrsMap map[string]interface{},
) (bool, error) {
	// Extract principal from client ID
	principal := fmt.Sprintf("Client::%s", clientID)

	// Action is to list a feature
	action := fmt.Sprintf("Action::list_%ss", feature)

	// Resource is the feature type
	resource := fmt.Sprintf("FeatureType::%s", feature)

	// Create attributes for the entities
	attributes := mergeContexts(map[string]interface{}{
		"type":      string(feature),
		"operation": "list",
		"feature":   string(feature),
	}, attrsMap)

	// Create Cedar entities
	entities, err := a.entityFactory.CreateEntitiesForRequest(principal, action, resource, claimsMap, attributes)
	if err != nil {
		return false, fmt.Errorf("failed to create Cedar entities: %w", err)
	}

	contextMap := mergeContexts(claimsMap, attrsMap)

	// Check authorization with entities
	return a.IsAuthorized(principal, action, resource, contextMap, entities)
}

// parseCedarEntityID parses a Cedar entity ID in the format "Type::ID".
// It returns the type and ID parts, or an error if the format is invalid.
func parseCedarEntityID(entityID string) (string, string, error) {
	parts := strings.SplitN(entityID, "::", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid entity ID format: %s", entityID)
	}
	return parts[0], parts[1], nil
}

// sanitizeURIForCedar sanitizes a URI for use in Cedar policies.
// Cedar entity IDs have restrictions on characters, so we need to sanitize the URI.
func sanitizeURIForCedar(uri string) string {
	// Replace characters that are not allowed in Cedar entity IDs
	// This is a simple implementation - you may need to enhance it based on your needs
	replacer := strings.NewReplacer(
		":", "_",
		"/", "_",
		"\\", "_",
		"?", "_",
		"&", "_",
		"=", "_",
		"#", "_",
		" ", "_",
		".", "_",
	)
	return replacer.Replace(uri)
}

// AuthorizeWithJWTClaims demonstrates how to use JWT claims with the Cedar authorization middleware.
// This method:
// 1. Extracts JWT claims from the context
// 2. Extracts the client ID from the claims
// 3. Includes the JWT claims in the Cedar context
// 4. Creates entities with appropriate attributes
// 5. Authorizes the operation using the client ID and claims
func (a *Authorizer) AuthorizeWithJWTClaims(
	ctx context.Context,
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	resourceID string,
	arguments map[string]interface{},
) (bool, error) {
	// Extract Identity from the context
	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return false, ErrMissingPrincipal
	}

	// Extract client ID from Identity claims
	claims := jwt.MapClaims(identity.Claims)
	clientID, ok := extractClientIDFromClaims(claims)
	if !ok {
		return false, ErrMissingPrincipal
	}

	// Preprocess claims and arguments
	processedClaims := preprocessClaims(claims)
	processedArgs := preprocessArguments(arguments)

	// Authorize based on the feature and operation
	switch {
	case feature == authorizers.MCPFeatureTool && operation == authorizers.MCPOperationCall:
		// Use the authorizeToolCall function for tool call operations
		return a.authorizeToolCall(clientID, resourceID, processedClaims, processedArgs)

	case feature == authorizers.MCPFeaturePrompt && operation == authorizers.MCPOperationGet:
		// Use the authorizePromptGet function for prompt get operations
		return a.authorizePromptGet(clientID, resourceID, processedClaims, processedArgs)

	case feature == authorizers.MCPFeatureResource && operation == authorizers.MCPOperationRead:
		// Use the authorizeResourceRead function for resource read operations
		return a.authorizeResourceRead(clientID, resourceID, processedClaims, processedArgs)

	case operation == authorizers.MCPOperationList:
		// Use the authorizeFeatureList function for list operations
		return a.authorizeFeatureList(clientID, feature, processedClaims, processedArgs)

	default:
		return false, fmt.Errorf("unsupported feature/operation combination: %s/%s", feature, operation)
	}
}

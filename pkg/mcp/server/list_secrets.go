package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/toolhive/pkg/secrets"
)

// SecretInfo represents secret information returned by list
type SecretInfo struct {
	Key string `json:"key"`
	// Description is populated by secrets providers that support it (e.g., 1Password
	// provides "Vault :: Item :: Field" descriptions). Will be empty for providers
	// that don't support descriptions (e.g., encrypted provider).
	Description string `json:"description,omitempty"`
}

// ListSecretsResponse represents the response from listing secrets
type ListSecretsResponse struct {
	Secrets []SecretInfo `json:"secrets"`
}

// ListSecrets lists all available secrets.
// The request parameter is required by the MCP tool handler interface but not used
// by this handler since list_secrets takes no arguments.
func (h *Handler) ListSecrets(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Get the configuration to determine the secrets provider
	cfg := h.configProvider.GetConfig()

	// Check if secrets setup has been completed
	if !cfg.Secrets.SetupCompleted {
		return NewToolResultError(
			"Secrets provider not configured. Please run 'thv secret setup' to configure a secrets provider first"), nil
	}

	// Get the provider type
	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return NewToolResultError(fmt.Sprintf("Failed to get secrets provider type: %v", err)), nil
	}

	// Create the secrets provider
	secretsProvider, err := secrets.CreateSecretProvider(providerType)
	if err != nil {
		return NewToolResultError(fmt.Sprintf("Failed to create secrets provider: %v", err)), nil
	}

	// List all secrets
	secretDescriptions, err := secretsProvider.ListSecrets(ctx)
	if err != nil {
		return NewToolResultError(fmt.Sprintf("Failed to list secrets: %v", err)), nil
	}

	// Format results with structured data
	var results []SecretInfo
	for _, desc := range secretDescriptions {
		info := SecretInfo{
			Key:         desc.Key,
			Description: desc.Description,
		}
		results = append(results, info)
	}

	// Create structured response
	response := ListSecretsResponse{
		Secrets: results,
	}

	return NewToolResultStructuredOnly(response), nil
}

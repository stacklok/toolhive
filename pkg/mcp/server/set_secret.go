package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/secrets"
)

// setSecretArgs holds the arguments for setting a secret
type setSecretArgs struct {
	Name     string `json:"name"`
	FilePath string `json:"file_path"`
}

// SetSecretResponse represents the response from setting a secret
type SetSecretResponse struct {
	Status string `json:"status"`
	Name   string `json:"name"`
}

// SetSecret sets a secret by reading its value from a file
func (h *Handler) SetSecret(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := &setSecretArgs{}
	if err := request.BindArguments(args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Validate arguments
	if args.Name == "" {
		return mcp.NewToolResultError("Secret name cannot be empty"), nil
	}
	if args.FilePath == "" {
		return mcp.NewToolResultError("File path cannot be empty"), nil
	}

	// Clean and validate the file path
	cleanPath := filepath.Clean(args.FilePath)

	// Check if file exists and is readable
	fileInfo, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return mcp.NewToolResultError(fmt.Sprintf("File does not exist: %s", cleanPath)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("Cannot access file: %v", err)), nil
	}

	// Check if it's a regular file (not a directory)
	if !fileInfo.Mode().IsRegular() {
		return mcp.NewToolResultError(fmt.Sprintf("Path is not a regular file: %s", cleanPath)), nil
	}

	// Check file size (limit to 1MB for safety)
	const maxFileSize = 1024 * 1024 // 1MB
	if fileInfo.Size() > maxFileSize {
		return mcp.NewToolResultError(fmt.Sprintf("File too large (max %d bytes): %d bytes", maxFileSize, fileInfo.Size())), nil
	}

	// Read the file content
	content, err := os.ReadFile(cleanPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to read file: %v", err)), nil
	}

	// Trim whitespace from the content
	secretValue := strings.TrimSpace(string(content))
	if secretValue == "" {
		return mcp.NewToolResultError("File content is empty or contains only whitespace"), nil
	}

	// Get the configuration to determine the secrets provider
	cfg := h.configProvider.GetConfig()

	// Check if secrets setup has been completed
	if !cfg.Secrets.SetupCompleted {
		return mcp.NewToolResultError(
			"Secrets provider not configured. Please run 'thv secret setup' to configure a secrets provider first"), nil
	}

	// Get the provider type
	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get secrets provider type: %v", err)), nil
	}

	// Create the secrets provider
	secretsProvider, err := secrets.CreateSecretProvider(providerType)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to create secrets provider: %v", err)), nil
	}

	// Check if the provider supports writing
	capabilities := secretsProvider.Capabilities()
	if !capabilities.CanWrite {
		return mcp.NewToolResultError(fmt.Sprintf(
			"Secrets provider '%s' is read-only and does not support setting secrets", providerType)), nil
	}

	// Set the secret
	if err := secretsProvider.SetSecret(ctx, args.Name, secretValue); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to set secret: %v", err)), nil
	}

	// Create success response
	response := SetSecretResponse{
		Status: "success",
		Name:   args.Name,
	}

	return mcp.NewToolResultStructuredOnly(response), nil
}

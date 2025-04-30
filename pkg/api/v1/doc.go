// Package v1 provides version 1 of the ToolHive API.
//
// This package defines the API types and interfaces for interacting with ToolHive.
// It is designed to be stable and backward compatible, allowing clients to depend
// on it without breaking changes within the v1 version.
//
// The API is organized into several components:
//
// Types (types.go):
//   - Server: Represents an MCP server
//   - RegistryServer: Represents an MCP server in the registry
//   - PermissionProfile: Defines security profiles for servers
//   - Various option types for API operations
//
// Interfaces (api.go):
//   - Client: Main entry point for the API
//   - ServerAPI: Methods for managing MCP servers
//   - RegistryAPI: Methods for managing the MCP server registry
//   - SecretAPI: Methods for managing secrets
//   - ConfigAPI: Methods for managing configuration
//   - VersionAPI: Methods for getting version information
//
// Conversion (convert package):
//   - Functions to convert between API types and internal types
//
// Example usage:
//
//	import (
//	    "context"
//	    "github.com/StacklokLabs/toolhive/pkg/api/v1"
//	)
//
//	func main() {
//	    // Create a new client
//	    client, err := v1.NewClient(context.Background())
//	    if err != nil {
//	        panic(err)
//	    }
//	    defer client.Close()
//
//	    // List servers
//	    servers, err := client.Server().List(context.Background(), &v1.ListOptions{})
//	    if err != nil {
//	        panic(err)
//	    }
//
//	    // Print server names
//	    for _, server := range servers.Items {
//	        fmt.Println(server.Name)
//	    }
//	}
package v1

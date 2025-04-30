// Package api provides a programmatic interface to ToolHive functionality.
//
// The API is versioned to allow for evolution without breaking existing clients.
// Each version is contained in its own subpackage (e.g., v1) and provides a
// complete set of types and interfaces for interacting with ToolHive.
//
// Current API versions:
//   - v1: The initial version of the API
//
// API Design Principles:
//
//  1. Versioning:
//     Each API version is contained in its own subpackage and can evolve independently.
//     New versions may be added, but existing versions will remain stable.
//
//  2. Clear Documentation:
//     All types and methods are documented to explain their purpose and usage.
//
//  3. Consistent Naming:
//     The API uses consistent naming conventions across all versions.
//
//  4. Minimal Dependencies:
//     The API minimizes dependencies on implementation details.
//
//  5. Error Handling:
//     The API provides clear error types and handling.
//
//  6. Validation:
//     The API includes validation for inputs.
//
//  7. Extensibility:
//     The API is designed for future extensions.
//
// Example usage:
//
//	import (
//	    "context"
//	    "github.com/StacklokLabs/toolhive/pkg/api/v1"
//	)
//
//	func main() {
//	    // Create a new client using the v1 API
//	    client, err := v1.NewClient(context.Background())
//	    if err != nil {
//	        panic(err)
//	    }
//	    defer client.Close()
//
//	    // Use the client to interact with ToolHive
//	    // ...
//	}
package api

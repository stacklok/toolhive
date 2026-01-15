//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/philippgille/chromem-go"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run inspect-chromem.go <path-to-chromem-db>")
		fmt.Println("Example: go run inspect-chromem.go /tmp/vmcp-optimizer-debug.db")
		os.Exit(1)
	}

	dbPath := os.Args[1]

	// Open the chromem-go database
	db, err := chromem.NewPersistentDB(dbPath, true) // true = read-only
	if err != nil {
		fmt.Printf("Error opening database: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("📊 Inspecting chromem-go database at: %s\n\n", dbPath)

	// List collections
	fmt.Println("📁 Collections:")
	fmt.Println("  - backend_servers")
	fmt.Println("  - backend_tools")
	fmt.Println()

	// Create a dummy embedding function (we're just inspecting, not querying)
	dummyEmbedding := func(ctx context.Context, text string) ([]float32, error) {
		return make([]float32, 384), nil // Placeholder
	}

	// Inspect backend_servers collection
	serversCol := db.GetCollection("backend_servers", dummyEmbedding)
	if serversCol != nil {
		count := serversCol.Count()
		fmt.Printf("🖥️  Backend Servers Collection: %d documents\n", count)
		
		if count > 0 {
			// Query all documents (using a generic query with high limit)
			results, err := serversCol.Query(context.Background(), "", count, nil, nil)
			if err == nil {
				fmt.Println("   Servers:")
				for _, doc := range results {
					fmt.Printf("   - ID: %s\n", doc.ID)
					fmt.Printf("     Content: %s\n", truncate(doc.Content, 80))
					if len(doc.Embedding) > 0 {
						fmt.Printf("     Embedding: %d dimensions\n", len(doc.Embedding))
					}
					fmt.Printf("     Metadata keys: %v\n", getKeys(doc.Metadata))
				}
			}
		}
	} else {
		fmt.Println("🖥️  Backend Servers Collection: not found")
	}
	fmt.Println()

	// Inspect backend_tools collection
	toolsCol := db.GetCollection("backend_tools", dummyEmbedding)
	if toolsCol != nil {
		count := toolsCol.Count()
		fmt.Printf("🔧 Backend Tools Collection: %d documents\n", count)
		
		if count > 0 && count < 20 {
			// Only show details if there aren't too many
			results, err := toolsCol.Query(context.Background(), "", count, nil, nil)
			if err == nil {
				fmt.Println("   Tools:")
				for i, doc := range results {
					if i >= 10 {
						fmt.Printf("   ... and %d more tools\n", count-10)
						break
					}
					fmt.Printf("   - ID: %s\n", doc.ID)
					fmt.Printf("     Content: %s\n", truncate(doc.Content, 80))
					if len(doc.Embedding) > 0 {
						fmt.Printf("     Embedding: %d dimensions\n", len(doc.Embedding))
					}
					fmt.Printf("     Server ID: %s\n", doc.Metadata["server_id"])
				}
			}
		} else if count >= 20 {
			fmt.Printf("   (too many to display, use query commands below)\n")
		}
	} else {
		fmt.Println("🔧 Backend Tools Collection: not found")
	}
	fmt.Println()

	// Show example queries
	fmt.Println("💡 Example Queries:")
	fmt.Println("   To search for tools semantically:")
	fmt.Println("   results, _ := toolsCol.Query(ctx, \"search repositories on GitHub\", 5, nil, nil)")
	fmt.Println()
	fmt.Println("   To filter by server:")
	fmt.Println("   results, _ := toolsCol.Query(ctx, \"list files\", 5, map[string]string{\"server_id\": \"github\"}, nil)")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func getKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

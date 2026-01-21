// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build ignore
// +build ignore

package main

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Document structure from chromem-go
type Document struct {
	ID        string
	Metadata  map[string]string
	Embedding []float32
	Content   string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run view-chromem-tool.go <path-to-chromem-db> [tool-name]")
		fmt.Println("Example: go run view-chromem-tool.go /tmp/vmcp-optimizer-debug.db get_file_contents")
		os.Exit(1)
	}

	dbPath := os.Args[1]
	searchTerm := ""
	if len(os.Args) > 2 {
		searchTerm = os.Args[2]
	}

	// Read all collections
	entries, err := os.ReadDir(dbPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		collectionPath := filepath.Join(dbPath, entry.Name())
		gobFiles, err := filepath.Glob(filepath.Join(collectionPath, "*.gob"))
		if err != nil {
			continue
		}

		for _, gobFile := range gobFiles {
			doc, err := decodeGobFile(gobFile)
			if err != nil {
				continue
			}

			// Skip empty documents
			if doc.ID == "" {
				continue
			}

			// If searching, filter by content
			if searchTerm != "" && !contains(doc.Content, searchTerm) && !contains(doc.ID, searchTerm) {
				continue
			}

			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("Document ID: %s\n", doc.ID)
			fmt.Printf("Content: %s\n", doc.Content)
			fmt.Printf("Embedding Dimensions: %d\n", len(doc.Embedding))
			
			// Show metadata
			fmt.Println("\nMetadata:")
			for key, value := range doc.Metadata {
				if key == "data" {
					// Pretty print JSON
					var jsonData interface{}
					if err := json.Unmarshal([]byte(value), &jsonData); err == nil {
						prettyJSON, _ := json.MarshalIndent(jsonData, "  ", "  ")
						fmt.Printf("  %s: %s\n", key, string(prettyJSON))
					} else {
						fmt.Printf("  %s: %s\n", key, truncate(value, 200))
					}
				} else {
					fmt.Printf("  %s: %s\n", key, value)
				}
			}
			
			// Show first few embedding values
			if len(doc.Embedding) > 0 {
				fmt.Printf("\nEmbedding (first 10): [")
				for i := 0; i < min(10, len(doc.Embedding)); i++ {
					if i > 0 {
						fmt.Print(", ")
					}
					fmt.Printf("%.3f", doc.Embedding[i])
				}
				fmt.Println(", ...]")
			}
			fmt.Println()
		}
	}
}

func decodeGobFile(path string) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := gob.NewDecoder(f)
	var doc Document
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}

	return &doc, nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && 
		(s == substr || 
		 len(s) > len(substr) && 
		 (s[:len(substr)] == substr || 
		  s[len(s)-len(substr):] == substr ||
		  findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

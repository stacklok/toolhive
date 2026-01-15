package main

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
)

// Minimal structures to decode chromem-go documents
type Document struct {
	ID        string
	Metadata  map[string]string
	Embedding []float32
	Content   string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run inspect-chromem-raw.go <path-to-chromem-db>")
		os.Exit(1)
	}

	dbPath := os.Args[1]
	fmt.Printf("📊 Raw inspection of chromem-go database: %s\n\n", dbPath)

	// Read all collection directories
	entries, err := os.ReadDir(dbPath)
	if err != nil {
		fmt.Printf("Error reading directory: %v\n", err)
		os.Exit(1)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		collectionPath := filepath.Join(dbPath, entry.Name())
		fmt.Printf("📁 Collection ID: %s\n", entry.Name())

		// Count gob files
		gobFiles, err := filepath.Glob(filepath.Join(collectionPath, "*.gob"))
		if err != nil {
			fmt.Printf("   Error: %v\n", err)
			continue
		}

		fmt.Printf("   Documents: %d\n", len(gobFiles))

		// Show first few documents
		limit := 5
		if len(gobFiles) > limit {
			fmt.Printf("   (showing first %d)\n", limit)
		}

		for i, gobFile := range gobFiles {
			if i >= limit {
				break
			}

			doc, err := decodeGobFile(gobFile)
			if err != nil {
				fmt.Printf("   - %s (error decoding: %v)\n", filepath.Base(gobFile), err)
				continue
			}

			fmt.Printf("   - Document ID: %s\n", doc.ID)
			fmt.Printf("     Content: %s\n", truncate(doc.Content, 80))
			fmt.Printf("     Embedding: %d dimensions\n", len(doc.Embedding))
			if serverID, ok := doc.Metadata["server_id"]; ok {
				fmt.Printf("     Server ID: %s\n", serverID)
			}
			if docType, ok := doc.Metadata["type"]; ok {
				fmt.Printf("     Type: %s\n", docType)
			}
		}
		fmt.Println()
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

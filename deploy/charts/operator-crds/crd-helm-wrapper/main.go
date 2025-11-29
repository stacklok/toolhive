// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// crd-helm-wrapper is a tool that wraps Kubernetes CRD YAML files with Helm
// template conditionals for conditional installation, skip functionality,
// and resource policy annotations.
package main

import (
	"bufio"
	"bytes"
	"embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed templates/*.tpl
var templateFS embed.FS

// Templates holds the loaded template content
type Templates struct {
	Header         string
	Footer         string
	KeepAnnotation string
}

// CRDMetadata represents the minimal structure needed to extract CRD name
type CRDMetadata struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string            `yaml:"name"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
}

// Config holds the wrapper configuration
type Config struct {
	SourceDir string
	TargetDir string
	Verbose   bool
}

func main() {
	cfg := Config{}

	flag.StringVar(&cfg.SourceDir, "source", "", "Source directory containing raw CRD YAML files")
	flag.StringVar(&cfg.TargetDir, "target", "", "Target directory for wrapped Helm templates")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Enable verbose output")
	flag.Parse()

	if cfg.SourceDir == "" || cfg.TargetDir == "" {
		fmt.Fprintln(os.Stderr, "Usage: crd-helm-wrapper -source <dir> -target <dir>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func loadTemplates() (*Templates, error) {
	header, err := templateFS.ReadFile("templates/header.tpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load header template: %w", err)
	}

	footer, err := templateFS.ReadFile("templates/footer.tpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load footer template: %w", err)
	}

	keepAnnotation, err := templateFS.ReadFile("templates/keep-annotation.tpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load keep-annotation template: %w", err)
	}

	return &Templates{
		Header:         string(header),
		Footer:         string(footer),
		KeepAnnotation: string(keepAnnotation),
	}, nil
}

func run(cfg Config) error {
	// Load templates
	templates, err := loadTemplates()
	if err != nil {
		return err
	}

	// Ensure target directory exists
	if err := os.MkdirAll(cfg.TargetDir, 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}

	// Find all YAML files in source directory
	files, err := filepath.Glob(filepath.Join(cfg.SourceDir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("failed to glob source files: %w", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no YAML files found in %s", cfg.SourceDir)
	}

	fmt.Printf("Found %d CRD files to process\n", len(files))

	for _, file := range files {
		if err := wrapCRDFile(file, cfg, templates); err != nil {
			return fmt.Errorf("failed to wrap %s: %w", file, err)
		}
	}

	fmt.Println("CRD wrapping completed successfully!")
	return nil
}

func wrapCRDFile(sourcePath string, cfg Config, templates *Templates) error {
	filename := filepath.Base(sourcePath)
	targetPath := filepath.Join(cfg.TargetDir, filename)

	fmt.Printf("Processing: %s\n", filename)

	// Read the source file
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Extract CRD name from the YAML
	crdName, err := extractCRDName(content)
	if err != nil {
		return fmt.Errorf("failed to extract CRD name: %w", err)
	}

	if cfg.Verbose {
		fmt.Printf("  CRD name: %s\n", crdName)
	}

	// Wrap the content with Helm template conditionals
	wrapped, err := wrapContent(content, crdName, filename, templates)
	if err != nil {
		return fmt.Errorf("failed to wrap content: %w", err)
	}

	// Write the wrapped content
	if err := os.WriteFile(targetPath, wrapped, 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	fmt.Printf("  Created: %s\n", targetPath)
	return nil
}

func extractCRDName(content []byte) (string, error) {
	var crd CRDMetadata
	if err := yaml.Unmarshal(content, &crd); err != nil {
		return "", fmt.Errorf("failed to parse YAML: %w", err)
	}

	if crd.Kind != "CustomResourceDefinition" {
		return "", fmt.Errorf("expected CustomResourceDefinition, got %s", crd.Kind)
	}

	if crd.Metadata.Name == "" {
		return "", fmt.Errorf("CRD name is empty")
	}

	return crd.Metadata.Name, nil
}

func wrapContent(content []byte, crdName, filename string, templates *Templates) ([]byte, error) {
	var buf bytes.Buffer

	// Write header with conditionals (replace placeholders)
	header := strings.ReplaceAll(templates.Header, "__CRD_NAME__", crdName)
	header = strings.ReplaceAll(header, "__FILENAME__", filename)
	buf.WriteString(header)

	// Process the YAML content line by line to inject the keep annotation
	scanner := bufio.NewScanner(bytes.NewReader(content))
	inAnnotations := false
	annotationsWritten := false
	skipFirstLine := false

	// Check if first line is just "---"
	if bytes.HasPrefix(content, []byte("---\n")) || bytes.HasPrefix(content, []byte("---\r\n")) {
		skipFirstLine = true
	}

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip the first line if it's just "---"
		if lineNum == 1 && skipFirstLine && strings.TrimSpace(line) == "---" {
			continue
		}

		// Detect when we enter the annotations block
		if strings.TrimSpace(line) == "annotations:" && !annotationsWritten {
			buf.WriteString(line + "\n")
			// Inject the keep annotation conditional from template
			buf.WriteString(templates.KeepAnnotation)
			inAnnotations = true
			annotationsWritten = true
			continue
		}

		// If we're past annotations (hit a non-indented line), reset flag
		if inAnnotations && !strings.HasPrefix(line, "    ") && strings.TrimSpace(line) != "" {
			inAnnotations = false
		}

		buf.WriteString(line + "\n")
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan content: %w", err)
	}

	// Write footer from template
	buf.WriteString(templates.Footer)

	return buf.Bytes(), nil
}

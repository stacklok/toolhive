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

// crd-helm-wrapper wraps Kubernetes CRD YAML files with Helm template
// conditionals for feature-flagged installation and resource policy annotations.
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

// crdFeatureFlags maps CRD plural names to their Helm values feature flags.
// CRDs can belong to multiple groups (e.g., mcpexternalauthconfigs is shared).
var crdFeatureFlags = map[string][]string{
	"mcpservers":                         {"server"},
	"mcpremoteproxies":                   {"server"},
	"mcptoolconfigs":                     {"server"},
	"mcpgroups":                          {"server"},
	"mcpregistries":                      {"registry"},
	"virtualmcpservers":                  {"virtualMcp"},
	"virtualmcpcompositetooldefinitions": {"virtualMcp"},
	"mcpexternalauthconfigs":             {"server", "virtualMcp"},
}

func main() {
	sourceDir := flag.String("source", "", "Source directory containing raw CRD YAML files")
	targetDir := flag.String("target", "", "Target directory for wrapped Helm templates")
	verbose := flag.Bool("verbose", false, "Enable verbose output")
	flag.Parse()

	if *sourceDir == "" || *targetDir == "" {
		fmt.Fprintln(os.Stderr, "Usage: crd-helm-wrapper -source <dir> -target <dir>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if err := run(*sourceDir, *targetDir, *verbose); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error: %v\n", err)
		os.Exit(1)
	}
}

func run(sourceDir, targetDir string, verbose bool) error {
	templates, err := loadTemplates()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(targetDir, 0750); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}

	files, err := filepath.Glob(filepath.Join(sourceDir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("failed to glob source files: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no YAML files found in %s", sourceDir)
	}

	fmt.Printf("📦 Found %d CRD files to process\n", len(files))

	for _, file := range files {
		if err := wrapCRDFile(file, sourceDir, targetDir, templates, verbose); err != nil {
			return fmt.Errorf("failed to wrap %s: %w", file, err)
		}
	}

	fmt.Println("✅ CRD wrapping completed successfully!")
	return nil
}

func loadTemplates() (map[string]string, error) {
	names := []string{"header", "footer", "keep-annotation"}
	templates := make(map[string]string, len(names))

	for _, name := range names {
		data, err := templateFS.ReadFile("templates/" + name + ".tpl")
		if err != nil {
			return nil, fmt.Errorf("failed to load %s template: %w", name, err)
		}
		templates[name] = string(data)
	}
	return templates, nil
}

func wrapCRDFile(sourcePath, sourceDir, targetDir string, templates map[string]string, verbose bool) error {
	filename := filepath.Base(sourcePath)
	fmt.Printf("Processing: %s\n", filename)

	// Sanitize path to prevent directory traversal
	cleanPath := filepath.Clean(sourcePath)
	if !strings.HasPrefix(cleanPath, filepath.Clean(sourceDir)) {
		return fmt.Errorf("source path escapes source directory: %s", sourcePath)
	}

	content, err := os.ReadFile(cleanPath) // #nosec G304 - path is sanitized above
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	crdName, err := extractCRDName(content)
	if err != nil {
		return fmt.Errorf("failed to extract CRD name: %w", err)
	}

	if verbose {
		fmt.Printf("  CRD name: %s\n", crdName)
	}

	featureFlags, err := getFeatureFlags(crdName)
	if err != nil {
		return fmt.Errorf("failed to get feature flags: %w", err)
	}
	if verbose {
		fmt.Printf("  Feature flags: %v\n", featureFlags)
	}

	wrapped, err := wrapContent(content, templates, featureFlags)
	if err != nil {
		return fmt.Errorf("failed to wrap content: %w", err)
	}

	targetPath := filepath.Join(targetDir, filename)
	if err := os.WriteFile(targetPath, wrapped, 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	fmt.Printf("  ✅ Created: %s\n", targetPath)
	return nil
}

func extractCRDName(content []byte) (string, error) {
	var crd struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
	}

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

func getFeatureFlags(crdName string) ([]string, error) {
	// CRD names are "plural.group" (e.g., mcpservers.toolhive.stacklok.dev)
	idx := strings.Index(crdName, ".")
	if idx <= 0 {
		return nil, fmt.Errorf("invalid CRD name format: %s", crdName)
	}
	plural := crdName[:idx]

	flags, ok := crdFeatureFlags[plural]
	if !ok {
		return nil, fmt.Errorf("CRD %q not in crdFeatureFlags map - please add it", crdName)
	}
	return flags, nil
}

func buildFeatureCondition(flags []string) string {
	if len(flags) == 0 {
		return ""
	}

	refs := make([]string, len(flags))
	for i, f := range flags {
		refs[i] = ".Values.crds.install." + f
	}

	if len(refs) == 1 {
		return refs[0]
	}
	return "or " + strings.Join(refs, " ")
}

func wrapContent(content []byte, templates map[string]string, featureFlags []string) ([]byte, error) {
	var buf bytes.Buffer

	// Write header with feature flag conditional
	header := strings.ReplaceAll(templates["header"], "__FEATURE_CONDITION__", buildFeatureCondition(featureFlags))
	buf.WriteString(header)

	// Process YAML content line by line
	scanner := bufio.NewScanner(bytes.NewReader(content))
	skipFirstLine := bytes.HasPrefix(content, []byte("---\n")) || bytes.HasPrefix(content, []byte("---\r\n"))
	annotationsWritten := false
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip document separator on first line
		if lineNum == 1 && skipFirstLine && strings.TrimSpace(line) == "---" {
			continue
		}

		// Inject keep annotation after annotations: line
		if strings.TrimSpace(line) == "annotations:" && !annotationsWritten {
			buf.WriteString(line + "\n")
			buf.WriteString(templates["keep-annotation"])
			annotationsWritten = true
			continue
		}

		// Escape Go template syntax in CRD descriptions
		buf.WriteString(escapeTemplateDelimiters(line) + "\n")
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning content: %w", err)
	}

	buf.WriteString(templates["footer"])
	return buf.Bytes(), nil
}

// escapeTemplateDelimiters escapes {{ and }} in CRD content to prevent Helm
// from interpreting documentation examples as template directives.
func escapeTemplateDelimiters(line string) string {
	if !strings.Contains(line, "{{") {
		return line
	}

	// Skip our intentional Helm directives
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "{{-") || (strings.HasPrefix(trimmed, "{{") && strings.Contains(trimmed, ".Values")) {
		return line
	}

	// Escape using Helm's built-in syntax: {{ "{{" }} renders as {{
	line = strings.ReplaceAll(line, "{{", "\x00OPEN\x00")
	line = strings.ReplaceAll(line, "}}", "\x00CLOSE\x00")
	line = strings.ReplaceAll(line, "\x00OPEN\x00", `{{ "{{" }}`)
	line = strings.ReplaceAll(line, "\x00CLOSE\x00", `{{ "}}" }}`)
	return line
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backendregistry_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/backendregistry/internal/exampleembedder"
)

// Referencing the example embedder keeps it part of this test's build, so the
// import-graph assertion below runs against a package that is known to compile
// against the live registry/core/server APIs.
var _ = exampleembedder.BuildAndServe

// exampleEmbedderDir is the source directory of the example embedder, relative to
// this package directory (the test working directory).
const exampleEmbedderDir = "internal/exampleembedder"

// TestExampleEmbedderImportSurface enforces the issue's import-graph acceptance
// criterion: an embedder that uses NewKubernetesBackendRegistry must NOT directly
// import the Kubernetes watch substrate (pkg/vmcp/k8s) or the Kubernetes REST
// package (k8s.io/client-go/rest). Both remain transitive dependencies compiled
// into the binary — this guards only the embedder's direct import surface.
func TestExampleEmbedderImportSurface(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"github.com/stacklok/toolhive/pkg/vmcp/k8s",
		"k8s.io/client-go/rest",
	}

	entries, err := os.ReadDir(exampleEmbedderDir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	parsedFiles := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, filepath.Join(exampleEmbedderDir, name), nil, parser.ImportsOnly)
		require.NoError(t, err)
		parsedFiles++

		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, f := range forbidden {
				assert.NotEqualf(t, f, path, "embedder file %s must not directly import %s", name, f)
			}
		}
	}

	require.Positive(t, parsedFiles, "no source files parsed in %s", exampleEmbedderDir)
}

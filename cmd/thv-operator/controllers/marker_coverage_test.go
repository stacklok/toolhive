// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestV1beta1TypesMarkerCoverage guards against a subtle failure mode in the
// opt-in-label design for storage-version migration: if a new CRD root type is
// added to cmd/thv-operator/api/v1beta1/ without the
//
//	+kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
//
// marker, the StorageVersionMigrator controller silently excludes it from
// reconciliation. The problem surfaces only when a future release tries to
// drop a deprecated version — at which point it is far too late.
//
// The test scans every root type (marker block contains
// +kubebuilder:object:root=true AND +kubebuilder:storageversion) and fails if
// any lacks either the migrate marker or an explicit
// +thv:storage-version-migrator:exclude sibling marker. List types
// (+kubebuilder:object:root=true WITHOUT +kubebuilder:storageversion) are
// intentionally skipped — CRD-level labels are keyed on the root type, not
// the list type.
func TestV1beta1TypesMarkerCoverage(t *testing.T) {
	t.Parallel()

	typesDir := filepath.Join("..", "api", "v1beta1")
	entries, err := os.ReadDir(typesDir)
	require.NoError(t, err, "reading %s", typesDir)

	const (
		rootMarker         = "+kubebuilder:object:root=true"
		storageMarker      = "+kubebuilder:storageversion"
		migrateMarker      = "+kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true"
		excludeMarker      = "+thv:storage-version-migrator:exclude"
		groupversionInfoGo = "groupversion_info.go"
		zzGeneratedPrefix  = "zz_generated"
		suffixTypesGo      = "_types.go"
	)

	type rootType struct {
		file        string
		typeName    string
		markerBlock []string
	}

	var roots []rootType

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, suffixTypesGo) {
			continue
		}
		if name == groupversionInfoGo || strings.HasPrefix(name, zzGeneratedPrefix) {
			continue
		}

		path := filepath.Join(typesDir, name)
		f, err := os.Open(path)
		require.NoError(t, err, "open %s", path)

		scanner := bufio.NewScanner(f)
		// Raise the max token size — some of the *_types.go files have very
		// long comment lines (printcolumn JSONPath expressions).
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)

		var block []string
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())

			switch {
			case strings.HasPrefix(line, "//"):
				// Accumulate every comment/marker line. kubebuilder convention
				// often separates markers from the godoc comment with a blank
				// line, so we keep the block alive across blanks and only
				// reset on a non-comment, non-blank, non-type line.
				block = append(block, strings.TrimSpace(strings.TrimPrefix(line, "//")))
			case strings.HasPrefix(line, "type "):
				// Only root-level types matter (must contain object:root=true
				// AND storageversion markers). List types have only object:root=true.
				if containsMarker(block, rootMarker) && containsMarker(block, storageMarker) {
					typeName := extractTypeName(line)
					if typeName != "" {
						copied := append([]string(nil), block...)
						roots = append(roots, rootType{file: name, typeName: typeName, markerBlock: copied})
					}
				}
				block = nil
			case line == "":
				// Blank line — keep block alive (comment-then-blank-then-type
				// is idiomatic Go + kubebuilder).
			default:
				// Anything else (e.g. struct body, package clause, import) —
				// drop any in-flight comment block.
				block = nil
			}
		}
		require.NoError(t, scanner.Err(), "scan %s", path)
		require.NoError(t, f.Close())
	}

	require.NotEmpty(t, roots,
		"no v1beta1 root types found — scanner likely broken; this test is meaningless without coverage")

	for _, r := range roots {
		hasMigrate := containsMarker(r.markerBlock, migrateMarker)
		hasExclude := containsMarker(r.markerBlock, excludeMarker)
		assert.Truef(t, hasMigrate || hasExclude,
			"v1beta1 root type %s.%s is missing either\n"+
				"  %s\n"+
				"(opt in to storage-version migration) or\n"+
				"  %s\n"+
				"(explicit opt-out). Every root type must declare one. See\n"+
				"cmd/thv-operator/controllers/storageversionmigrator_controller.go for context.",
			r.file, r.typeName, migrateMarker, excludeMarker)
	}
}

// containsMarker returns true if any line in block contains the given
// marker substring.
func containsMarker(block []string, marker string) bool {
	for _, l := range block {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
}

// extractTypeName returns the identifier in a line of the form `type Foo struct {`.
// Returns empty string if the line is not a type declaration we care about.
func extractTypeName(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "type" {
		return ""
	}
	return fields[1]
}

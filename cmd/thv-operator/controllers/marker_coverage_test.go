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

// TestStorageVersionRootMarkerCoverage guards against a subtle failure mode in
// the opt-in-label design for storage-version migration: if a new CRD root
// type is added under cmd/thv-operator/api/ without the
//
//	+kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
//
// marker, the StorageVersionMigrator controller silently excludes it from
// reconciliation. The problem surfaces only when a future release tries to
// drop a deprecated version — at which point it is far too late.
//
// The test walks every cmd/thv-operator/api/v*/ directory and scans the
// *_types.go (and legacy types.go) files inside. For each type whose marker
// block contains both +kubebuilder:object:root=true AND
// +kubebuilder:storageversion (i.e., the current storage version's root
// types), it requires the migrate marker. No escape hatch — every
// storage-version root must opt in.
//
// Why no escape hatch? An "exclude" marker would let a single-version CRD
// declare "don't auto-migrate me", but at graduation time the developer
// must remember to swap exclude→migrate. Forgetting the swap reintroduces
// the silent-skip failure mode the test is meant to prevent. If a CRD ever
// legitimately needs to be excluded, handle it as a one-off PR conversation
// (e.g. a hardcoded allowlist here) rather than a self-serve marker.
//
// Notes on the scope:
//   - List types (+kubebuilder:object:root=true WITHOUT +kubebuilder:storageversion)
//     are intentionally skipped — CRD-level labels are keyed on the root type,
//     not the list type.
//   - Served-but-not-storage versions (e.g. v1alpha1 during a v1alpha1→v1beta1
//     graduation, or v1beta1 during a future v1beta1→v1beta2 graduation) are
//     naturally filtered out by the storageversion-marker requirement.
//   - When a future graduation moves +kubebuilder:storageversion from v1beta1
//     to v1beta2, the test will automatically start scanning v1beta2's root
//     types without code changes here.
func TestStorageVersionRootMarkerCoverage(t *testing.T) {
	t.Parallel()

	apiDir := filepath.Join("..", "api")
	versionDirs, err := os.ReadDir(apiDir)
	require.NoError(t, err, "reading %s", apiDir)

	const (
		rootMarker         = "+kubebuilder:object:root=true"
		storageMarker      = "+kubebuilder:storageversion"
		migrateMarker      = "+kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true"
		groupversionInfoGo = "groupversion_info.go"
		zzGeneratedPrefix  = "zz_generated"
		// Match both the modern one-type-per-file convention (e.g.
		// mcpserver_types.go) and the legacy multi-type single-file
		// convention (api/v1alpha1/types.go) so no root type slips through.
		exactTypesGo  = "types.go"
		suffixTypesGo = "_types.go"
	)

	type rootType struct {
		version     string // e.g. "v1beta1"
		file        string
		typeName    string
		markerBlock []string
	}

	var roots []rootType

	for _, vd := range versionDirs {
		// Only descend into directories that look like an API version
		// (v1alpha1, v1beta1, v1beta2, v1, ...). Anything else under api/ is
		// not our concern.
		if !vd.IsDir() || !strings.HasPrefix(vd.Name(), "v") {
			continue
		}
		version := vd.Name()
		typesDir := filepath.Join(apiDir, version)

		entries, err := os.ReadDir(typesDir)
		require.NoError(t, err, "reading %s", typesDir)

		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if name != exactTypesGo && !strings.HasSuffix(name, suffixTypesGo) {
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
							roots = append(roots, rootType{
								version:     version,
								file:        name,
								typeName:    typeName,
								markerBlock: copied,
							})
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
	}

	require.NotEmpty(t, roots,
		"no storage-version root types found under cmd/thv-operator/api/ — "+
			"scanner likely broken or no version directory carries the "+
			"+kubebuilder:storageversion marker; this test is meaningless without coverage")

	for _, r := range roots {
		assert.Truef(t, containsMarker(r.markerBlock, migrateMarker),
			"root type %s/%s.%s is missing the storage-version migrate marker:\n"+
				"  %s\n"+
				"Every root type carrying +kubebuilder:storageversion must declare it.\n"+
				"See cmd/thv-operator/controllers/storageversionmigrator_controller.go\n"+
				"for context. There is no opt-out marker — if a CRD legitimately\n"+
				"should not be auto-migrated, raise it as a PR review conversation\n"+
				"and update the test's allowlist explicitly.",
			r.version, r.file, r.typeName, migrateMarker)
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

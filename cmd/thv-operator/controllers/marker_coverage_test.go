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
		key := r.version + "/" + r.file + "." + r.typeName
		if _, allowed := excludedRootTypes[key]; allowed {
			continue
		}
		assert.Truef(t, containsMarker(r.markerBlock, migrateMarker),
			"root type %s is missing the storage-version migrate marker:\n"+
				"  %s\n"+
				"Every root type carrying +kubebuilder:storageversion must declare it.\n"+
				"See cmd/thv-operator/controllers/storageversionmigrator_controller.go\n"+
				"for context. There is no opt-out marker — if a CRD legitimately\n"+
				"should not be auto-migrated, raise it as a PR review conversation\n"+
				"and add an entry to excludedRootTypes in this file.",
			key, migrateMarker)
	}
}

// excludedRootTypes is the (intentionally empty) allowlist of storage-version
// root types that are deliberately not auto-migrated. Entries are keyed by
// "<version>/<file>.<TypeName>" (e.g. "v1alpha1/types.go.MCPSomething").
//
// Adding an entry here is a deliberate, reviewed decision — not a self-serve
// escape hatch. If a future CRD legitimately cannot or should not participate
// in storage-version migration:
//
//  1. Open a PR with the entry added here.
//  2. Make the case in the PR description: why is this CRD different?
//     What happens at version-drop time if no migration ever runs?
//  3. The reviewer should push back hard. Excluding a CRD means accepting
//     that it can never have a version dropped without manual intervention.
var excludedRootTypes = map[string]struct{}{
	// Example (do not enable):
	// "v1alpha1/types.go.MCPSomething": {},
}

// containsMarker returns true if any line in block carries the given
// kubebuilder marker as a complete token — the marker must be followed by
// end-of-line or whitespace.
//
// Strict matching matters: the migrate marker ends in "=true". A typo such
// as "=truee" would still be a substring match under strings.Contains, the
// test would pass, the generated CRD YAML would have label value "truee",
// and the controller's runtime check (`labels[key] == "true"`) would
// silently exclude the CRD. The bug would only surface at version-drop time
// months later. Whole-token matching catches the typo at PR time.
func containsMarker(block []string, marker string) bool {
	for _, l := range block {
		idx := strings.Index(l, marker)
		if idx < 0 {
			continue
		}
		end := idx + len(marker)
		if end == len(l) || l[end] == ' ' || l[end] == '\t' {
			return true
		}
		// Marker appeared only as a prefix of a longer token — reject.
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

// TestContainsMarkerStrictBoundary defends the whole-token match contract of
// containsMarker. If someone "improves" it back to strings.Contains semantics,
// the value-typo failure mode reopens silently. Keep this test as the
// regression guard.
func TestContainsMarkerStrictBoundary(t *testing.T) {
	t.Parallel()

	const marker = "+kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true"

	tests := []struct {
		name string
		line string
		want bool
	}{
		{name: "exact match at end of line", line: marker, want: true},
		{name: "marker followed by space", line: marker + " // trailing comment", want: true},
		{name: "marker followed by tab", line: marker + "\t", want: true},
		{name: "value typo =truee is a prefix match — must reject", line: marker + "e", want: false},
		{name: "value typo =trueX is a prefix match — must reject", line: marker + "X", want: false},
		{name: "marker absent", line: "+kubebuilder:object:root=true", want: false},
		{name: "marker followed by alphanumeric — must reject", line: marker + "0", want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := containsMarker([]string{tc.line}, marker)
			assert.Equal(t, tc.want, got)
		})
	}
}

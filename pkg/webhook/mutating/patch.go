// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mutating

import (
	"encoding/json"
	"fmt"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
)

// patchTypeJSONPatch is the patch_type value for RFC 6902 JSON Patch.
const patchTypeJSONPatch = "json_patch"

// mcpRequestPathPrefix is the required prefix for all patch paths.
// Patches are scoped to the mcp_request container only.
const mcpRequestPathPrefix = "/mcp_request/"

// validOps is the set of valid RFC 6902 operations.
var validOps = map[string]bool{
	"add":     true,
	"remove":  true,
	"replace": true,
	"copy":    true,
	"move":    true,
	"test":    true,
}

// JSONPatchOp represents a single RFC 6902 JSON Patch operation.
type JSONPatchOp struct {
	// Op is the patch operation type (add, remove, replace, copy, move, test).
	Op string `json:"op"`
	// Path is the JSON Pointer (RFC 6901) path to apply the operation to.
	Path string `json:"path"`
	// Value is the value to use for add, replace, and test operations.
	Value json.RawMessage `json:"value,omitempty"`
	// From is the source path for copy and move operations.
	From string `json:"from,omitempty"`
}

// ValidatePatch checks that all operations in the patch are well-formed.
// It validates that all operations are supported RFC 6902 types and paths are non-empty.
func ValidatePatch(patch []JSONPatchOp) error {
	for i, op := range patch {
		if !validOps[op.Op] {
			return fmt.Errorf("patch[%d]: unsupported operation %q (valid ops: add, remove, replace, copy, move, test)", i, op.Op)
		}
		if op.Path == "" {
			return fmt.Errorf("patch[%d]: path is required", i)
		}
		// copy and move also require a From field.
		if (op.Op == "copy" || op.Op == "move") && op.From == "" {
			return fmt.Errorf("patch[%d]: %q operation requires a 'from' field", i, op.Op)
		}
	}
	return nil
}

// IsPatchScopedToMCPRequest returns true if all patch operations target paths
// within the mcp_request container. This prevents webhooks from accidentally
// or maliciously modifying principal, context, or other immutable envelope fields.
// The root "/mcp_request" path is intentionally rejected so webhooks must make
// granular changes beneath the MCP request instead of replacing it wholesale.
func IsPatchScopedToMCPRequest(patch []JSONPatchOp) bool {
	for _, op := range patch {
		if !strings.HasPrefix(op.Path, mcpRequestPathPrefix) {
			return false
		}
		// For copy/move, also check the From path.
		if (op.Op == "copy" || op.Op == "move") && op.From != "" {
			if !strings.HasPrefix(op.From, mcpRequestPathPrefix) {
				return false
			}
		}
	}
	return true
}

// ApplyPatch applies a set of RFC 6902 JSON Patch operations to the original JSON document.
// Returns the patched JSON document. The patch operations are applied in order.
func ApplyPatch(original []byte, patch []JSONPatchOp) ([]byte, error) {
	// Marshal the patch ops to JSON so the library can parse them.
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal patch operations: %w", err)
	}

	jp, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JSON patch: %w", err)
	}

	patched, err := jp.Apply(original)
	if err != nil {
		return nil, fmt.Errorf("failed to apply JSON patch: %w", err)
	}

	return patched, nil
}

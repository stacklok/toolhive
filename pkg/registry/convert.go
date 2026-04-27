// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stacklok/toolhive-core/registry/converters"
	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry/legacyhint"
)

// ErrAlreadyUpstream indicates the input was already in upstream MCP registry
// format, so no conversion was performed.
var ErrAlreadyUpstream = errors.New("input is already in upstream format")

// ConvertJSON converts a legacy ToolHive registry JSON document into the
// upstream MCP registry format. ToolHive-specific fields are carried through to
// the publisher-provided extension block on each server. The output is
// validated against the upstream registry schema before being returned, so
// callers writing to disk get either a schema-compliant file or an error.
//
// Returns ErrAlreadyUpstream if the input is already in the upstream format.
func ConvertJSON(input []byte) ([]byte, error) {
	if legacyhint.IsUpstream(input) {
		return nil, ErrAlreadyUpstream
	}

	reg := &types.Registry{}
	if err := json.Unmarshal(input, reg); err != nil {
		return nil, fmt.Errorf("failed to parse legacy registry data: %w", err)
	}

	upstream, err := converters.NewUpstreamRegistryFromToolhiveRegistry(reg)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to upstream format: %w", err)
	}

	out, err := json.MarshalIndent(upstream, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal upstream registry: %w", err)
	}

	if err := types.ValidateUpstreamRegistryBytes(out); err != nil {
		return nil, fmt.Errorf("converted output does not match the upstream registry schema: %w", err)
	}
	return out, nil
}

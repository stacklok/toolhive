// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"errors"
	"fmt"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry/legacyhint"
)

// ErrLegacyFormat is returned when input matches the legacy ToolHive registry
// format instead of the upstream MCP registry format. The error wording
// carries the migration guidance (run `thv registry convert`) so callers can
// surface it directly.
//
// We detect the legacy format up front because Go's json.Unmarshal silently
// produces an empty UpstreamRegistry for legacy input — the legacy
// top-level "servers" field does not match upstream's "data.servers" path,
// so the decoder sees no error and the caller gets a registry with no
// entries.
var ErrLegacyFormat = errors.New(legacyhint.MigrationMessage)

// parseEntries decodes raw JSON in the upstream MCP registry format
// (types.UpstreamRegistry) into a slice of Entry values.
//
// Entry validation (kind/payload consistency, non-empty name) is the
// caller's responsibility — typically performed by NewStore.
//
// Returns ErrLegacyFormat when the input matches the legacy ToolHive
// registry format.
func parseEntries(data []byte) ([]*Entry, error) {
	if !legacyhint.IsUpstream(data) && legacyhint.Looks(data) {
		return nil, ErrLegacyFormat
	}

	var upstream types.UpstreamRegistry
	if err := json.Unmarshal(data, &upstream); err != nil {
		return nil, fmt.Errorf("parse upstream registry: %w", err)
	}

	out := make([]*Entry, 0, len(upstream.Data.Servers)+len(upstream.Data.Skills))

	for i := range upstream.Data.Servers {
		server := &upstream.Data.Servers[i]
		out = append(out, &Entry{
			Kind:   KindServer,
			Name:   server.Name,
			Server: server,
		})
	}

	for i := range upstream.Data.Skills {
		skill := &upstream.Data.Skills[i]
		out = append(out, &Entry{
			Kind:  KindSkill,
			Name:  skillEntryName(skill),
			Skill: skill,
		})
	}

	return out, nil
}

// skillEntryName produces the registry-unique identifier for a skill by
// combining namespace and name. The resulting form (e.g.
// "io.github.user/summarizer") mirrors how upstream ServerJSON names are
// reverse-DNS-qualified, so a single Get(name) lookup works uniformly
// across kinds.
func skillEntryName(s *types.Skill) string {
	if s.Namespace == "" {
		return s.Name
	}
	return s.Namespace + "/" + s.Name
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"github.com/stacklok/toolhive/pkg/auth"
)

// instructionsFor returns the merged backend instructions to advertise to the
// caller of ctx in an initialize or server/discover result. It is the
// WithInstructionsFunc callback of the SDK path (serve.go), reached only when
// aggregation.instructions.forward is enabled.
//
// Identity is read from the request context at this transport boundary and
// passed explicitly to the core, like injectCoreSessionCapabilities does.
// core.Discover applies the admission and health filters, so the text covers
// exactly the backends this identity may use. An error is logged and yields
// no instructions: a handshake must not fail because one backend's capability
// query did, and the client loses only optional guidance.
func (s *Server) instructionsFor(ctx context.Context) string {
	identity, _ := auth.IdentityFromContext(ctx)
	caps, err := s.core.Discover(ctx, identity)
	if err != nil {
		slog.WarnContext(ctx, "backend instructions unavailable for this handshake", "error", err)
		return ""
	}
	return s.renderInstructions(ctx, caps.BackendInstructions)
}

// renderInstructions applies the server's instructions config to the
// per-backend text core.Discover returned: nothing when forwarding is off,
// else mergeInstructions under the configured cap.
func (s *Server) renderInstructions(ctx context.Context, byBackend map[string]string) string {
	if !s.config.Instructions.ForwardEnabled() {
		return ""
	}
	return mergeInstructions(ctx, byBackend, s.config.Instructions.EffectiveMaxLength())
}

// mergeInstructions renders one block per backend, in backend ID order, as
//
//	[<backend id>]
//	<text, leading and trailing whitespace trimmed>
//
// joined by a blank line. The label is the backend ID (the workload name, as
// used in aggregation.tools and, under the prefix strategy, as the tool-name
// prefix), so a client can match guidance to a backend. The label is a
// convention for the client, not a boundary: a backend is a trusted component
// chosen by the operator, and its text is forwarded as is, so a backend could
// write a line that looks like another backend's label. The result is capped
// at maxLength bytes: a block that would exceed the cap is skipped whole and
// logged at WARN (once per handshake), and later, shorter blocks may still
// fit. Backends with empty text produce no block. Returns "" when nothing fits.
func mergeInstructions(ctx context.Context, byBackend map[string]string, maxLength int) string {
	ids := make([]string, 0, len(byBackend))
	for id, text := range byBackend {
		if strings.TrimSpace(text) != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	var b strings.Builder
	for _, id := range ids {
		block := "[" + id + "]\n" + strings.TrimSpace(byBackend[id])
		sep := ""
		if b.Len() > 0 {
			sep = "\n\n"
		}
		if b.Len()+len(sep)+len(block) > maxLength {
			slog.WarnContext(ctx, "skipping backend instructions: merged text would exceed the configured cap",
				"backend", id, "block_bytes", len(block), "merged_bytes", b.Len(), "max_length", maxLength)
			continue
		}
		b.WriteString(sep)
		b.WriteString(block)
	}
	return b.String()
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"log/slog"
)

// TUILogHandler is an end-of-pipeline slog.Handler that sends formatted
// WARN/ERROR records to a channel so the TUI can display them inside the
// dashboard instead of writing to stderr (which would corrupt the alt-screen
// rendering).
//
// Because TUILogHandler is a terminal handler (it formats and dispatches
// records directly rather than delegating to an inner handler), it does not
// support WithAttrs/WithGroup chaining. Callers must not rely on
// slog.Logger.With to attach attributes through this handler; any attributes
// present on a record are inlined in Handle instead.
type TUILogHandler struct {
	ch    chan<- string
	level slog.Level
}

// NewTUILogHandler creates a TUILogHandler that sends records to ch.
func NewTUILogHandler(ch chan<- string, level slog.Level) *TUILogHandler {
	return &TUILogHandler{ch: ch, level: level}
}

// Enabled reports whether the handler handles records at the given level.
func (h *TUILogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle formats and sends a log record to the channel.
func (h *TUILogHandler) Handle(_ context.Context, r slog.Record) error {
	prefix := func() string {
		if r.Level >= slog.LevelError {
			return "ERROR"
		}
		return "WARN"
	}()
	msg := prefix + ": " + r.Message
	r.Attrs(func(a slog.Attr) bool {
		msg += "  " + a.Key + "=" + a.Value.String()
		return true
	})
	select {
	case h.ch <- msg:
	default: // drop if channel is full
	}
	return nil
}

// WithAttrs returns the receiver unchanged. TUILogHandler is an end-of-pipeline
// handler; pre-bound attributes from slog.Logger.With are silently dropped.
// See the type doc comment for details.
func (h *TUILogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }

// WithGroup returns the receiver unchanged. TUILogHandler is an end-of-pipeline
// handler; group scoping from slog.Logger.WithGroup is silently ignored.
// See the type doc comment for details.
func (h *TUILogHandler) WithGroup(_ string) slog.Handler { return h }

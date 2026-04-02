// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// CLIHandler is a slog.Handler that renders WARN/ERROR records as styled
// single-line CLI messages instead of JSON or text key=value output.
// INFO and DEBUG records are silently dropped.
type CLIHandler struct {
	mu    sync.Mutex
	w     io.Writer
	level slog.Level
}

// NewCLIHandler returns a CLIHandler that writes to w and filters by level.
func NewCLIHandler(w io.Writer, level slog.Level) *CLIHandler {
	return &CLIHandler{w: w, level: level}
}

// Enabled reports whether the handler handles records at the given level.
func (h *CLIHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle formats a log record as a styled CLI message.
func (h *CLIHandler) Handle(_ context.Context, r slog.Record) error {
	var icon string
	var msgStyle, dimStyle lipgloss.Style
	switch {
	case r.Level >= slog.LevelError:
		icon = lipgloss.NewStyle().Foreground(ColorRed).Bold(true).Render("✗")
		msgStyle = lipgloss.NewStyle().Foreground(ColorRed)
		dimStyle = lipgloss.NewStyle().Foreground(ColorRed)
	default: // WARN
		icon = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true).Render("⚠")
		msgStyle = lipgloss.NewStyle().Foreground(ColorYellow)
		dimStyle = lipgloss.NewStyle().Foreground(ColorDim2)
	}

	text := msgStyle.Render(r.Message)

	// Append any "reason" attribute so the user sees why.
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "reason" || a.Key == "error" {
			text += dimStyle.Render(": " + a.Value.String())
		}
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = fmt.Fprintf(h.w, "  %s  %s\n", icon, text)
	return nil
}

// WithAttrs returns the same handler (attrs are omitted in CLI output).
func (h *CLIHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }

// WithGroup returns the same handler (groups are omitted in CLI output).
func (h *CLIHandler) WithGroup(_ string) slog.Handler { return h }

// TUILogHandler is a slog.Handler that sends formatted WARN/ERROR records to a
// channel so the TUI can display them inside the dashboard instead of writing
// to stderr (which would corrupt the alt-screen rendering).
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

// WithAttrs returns the same handler (attrs are merged into Handle output).
func (h *TUILogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }

// WithGroup returns the same handler (groups are omitted).
func (h *TUILogHandler) WithGroup(_ string) slog.Handler { return h }

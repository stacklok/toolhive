// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"os"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestMain(m *testing.M) {
	// Force ANSI color output so lipgloss renders escape sequences in tests.
	// Without this, lipgloss detects a non-TTY environment and strips all
	// styling, making it impossible to verify that styled output is produced.
	lipgloss.DefaultRenderer().SetColorProfile(termenv.ANSI256)
	os.Exit(m.Run())
}

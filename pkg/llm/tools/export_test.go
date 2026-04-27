// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// This file exports internal constructors for use in external (_test) packages.
// It is compiled only during `go test`.

package tools

// NewClaudeCodeAdapterWithHome creates a claudeCodeAdapter that uses the
// provided directory as the user home directory instead of os.UserHomeDir.
func NewClaudeCodeAdapterWithHome(home string) Adapter {
	return newClaudeCodeAdapter(func() (string, error) { return home, nil })
}

// NewGeminiCLIAdapterWithHome creates a geminiCLIAdapter that uses the
// provided directory as the user home directory instead of os.UserHomeDir.
func NewGeminiCLIAdapterWithHome(home string) Adapter {
	return newGeminiCLIAdapter(func() (string, error) { return home, nil })
}

// NewCursorAdapterWithHome creates a cursorAdapter that uses the provided
// directory as the user home directory instead of os.UserHomeDir.
func NewCursorAdapterWithHome(home string) Adapter {
	return newCursorAdapter(func() (string, error) { return home, nil })
}

// NewVSCodeAdapterWithHome creates a vscodeAdapter that uses the provided
// directory as the user home directory instead of os.UserHomeDir.
func NewVSCodeAdapterWithHome(home string) Adapter {
	return newVSCodeAdapter(func() (string, error) { return home, nil })
}

// NewXcodeAdapterWithHome creates an xcodeAdapter that uses the provided
// directory as the user home directory instead of os.UserHomeDir.
func NewXcodeAdapterWithHome(home string) Adapter {
	return newXcodeAdapter(func() (string, error) { return home, nil })
}

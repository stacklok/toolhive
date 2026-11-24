// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package instructions defines the configuration that sets the instructions
// an MCP endpoint returns to clients in its initialize and server/discover
// results. MCPServer, MCPRemoteProxy and VirtualMCPServer share it.
//
// +kubebuilder:object:generate=true
// +groupName=toolhive.stacklok.dev
// +versionName=instructions
package instructions

import "fmt"

// Mode says how the configured value combines with the instructions the
// server returns.
type Mode string

const (
	// ModeReplace returns the configured value in place of the server's
	// instructions.
	ModeReplace Mode = "replace"
	// ModeAppend returns the server's instructions, a blank line, and then the
	// configured value.
	ModeAppend Mode = "append"
	// ModePrepend returns the configured value, a blank line, and then the
	// server's instructions.
	ModePrepend Mode = "prepend"
)

// separator joins the configured value and the server's instructions.
const separator = "\n\n"

// Config sets the instructions an MCP endpoint returns to clients in the
// result of a successful initialize or server/discover request. Clients
// commonly add server instructions to the model's context, so this is where
// an operator adds policy or usage guidance without changing the server.
//
// +gendoc
type Config struct {
	// Mode says how value combines with the instructions the server returns.
	// replace returns value alone. append returns the server's instructions,
	// a blank line, and value. prepend returns value, a blank line, and the
	// server's instructions. When the server returns no instructions, every
	// mode returns value alone.
	// +kubebuilder:validation:Enum=replace;append;prepend
	// +kubebuilder:default=append
	// +optional
	Mode Mode `json:"mode,omitempty" yaml:"mode,omitempty"`

	// Value is the text that mode combines with the server's instructions.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Value string `json:"value" yaml:"value"`
}

// Validate reports whether the configuration can be applied. An empty Mode is
// valid and means ModeAppend.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("instructions configuration is required")
	}
	switch c.Mode {
	case "", ModeReplace, ModeAppend, ModePrepend:
	default:
		return fmt.Errorf("invalid instructions mode %q: must be one of %q, %q or %q",
			c.Mode, ModeReplace, ModeAppend, ModePrepend)
	}
	if c.Value == "" {
		return fmt.Errorf("instructions value must not be empty")
	}
	return nil
}

// Apply returns the instructions a client receives when the server returns
// server, which is empty when the server returns none.
func (c *Config) Apply(server string) string {
	if server == "" {
		return c.Value
	}
	switch c.Mode {
	case ModeReplace:
		return c.Value
	case ModePrepend:
		return c.Value + separator + server
	case ModeAppend:
		return server + separator + c.Value
	}
	// An empty Mode means ModeAppend.
	return server + separator + c.Value
}

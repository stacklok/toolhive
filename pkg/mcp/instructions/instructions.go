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

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

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

// asciiSpace is the white space dropped around a value and that a value may
// not consist of: tab, LF, FF, CR and space. It is what \s matches in the RE2
// syntax the Kubernetes API server checks the CRD's \S pattern with, so
// admission and Validate agree on which values are blank. Other white space,
// such as a no-break space, is kept as text.
const asciiSpace = "\t\n\f\r "

// MaxValueLength is the most characters a value may hold. The RunConfig
// ConfigMap holds the value twice, in the typed field and in the middleware
// parameters, and JSON encodes a character in at most six bytes, so both
// copies stay well under the 1 MiB a ConfigMap may hold.
const MaxValueLength = 65536

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
	// Leading and trailing ASCII white space (tab, LF, FF, CR, space) is
	// dropped, and a value that is only such white space is rejected.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=65536
	// +kubebuilder:validation:Pattern=`\S`
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
	if strings.Trim(c.Value, asciiSpace) == "" {
		return fmt.Errorf("instructions value must not be empty or only white space")
	}
	if n := utf8.RuneCountInString(c.Value); n > MaxValueLength {
		return fmt.Errorf("instructions value has %d characters, more than the %d allowed", n, MaxValueLength)
	}
	return nil
}

// Apply returns the instructions a client receives when the server returns
// server, which is empty when the server returns none. The value is used
// without its leading and trailing ASCII white space (see asciiSpace), so a
// file that ends in a newline or a YAML block adds no blank line.
func (c *Config) Apply(server string) string {
	value := strings.Trim(c.Value, asciiSpace)
	if server == "" {
		return value
	}
	switch c.Mode {
	case ModeReplace:
		return value
	case ModePrepend:
		return value + separator + server
	case ModeAppend:
		return server + separator + value
	}
	// An empty Mode means ModeAppend.
	return server + separator + value
}

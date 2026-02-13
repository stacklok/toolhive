// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
)

// TokenCounter estimates the number of tokens a tool definition would consume
// when sent to an LLM. Implementations may use character-based heuristics or
// real tokenizers.
type TokenCounter interface {
	CountTokens(tool mcp.Tool) int
}

// CharDivTokenCounter estimates token count by serialising the full mcp.Tool
// to JSON and dividing the byte length by a configurable divisor.
type CharDivTokenCounter struct {
	Divisor int
}

// CountTokens returns len(json(tool)) / divisor.
// Returns 0 if the divisor is zero or serialisation fails.
func (c CharDivTokenCounter) CountTokens(tool mcp.Tool) int {
	if c.Divisor <= 0 {
		return 0
	}
	data, err := json.Marshal(tool)
	if err != nil {
		return 0
	}
	return len(data) / c.Divisor
}

// DefaultTokenCounter returns a CharDivTokenCounter with a divisor of 4,
// which is a reasonable approximation for most LLM tokenizers.
func DefaultTokenCounter() TokenCounter {
	return CharDivTokenCounter{Divisor: 4}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tokencounter provides token estimation for MCP tool definitions.
package tokencounter

import (
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
)

// Counter estimates the number of tokens a tool definition would consume
// when sent to an LLM. Implementations may use character-based heuristics or
// real tokenizers.
type Counter interface {
	CountTokens(tool mcp.Tool) int
}

// JSONByteDivisionCounter estimates token count by serialising the full mcp.Tool
// to JSON and dividing the byte length by a configurable divisor.
type JSONByteDivisionCounter struct {
	Divisor int
}

// CountTokens returns len(json(tool)) / divisor.
// Returns 0 if the divisor is zero or serialisation fails.
func (c JSONByteDivisionCounter) CountTokens(tool mcp.Tool) int {
	if c.Divisor <= 0 {
		return 0
	}
	data, err := json.Marshal(tool)
	if err != nil {
		return 0
	}
	return len(data) / c.Divisor
}

// NewJSONByteCounter returns a JSONByteDivisionCounter with a divisor of 4,
// which is a reasonable approximation for most LLM tokenizers.
func NewJSONByteCounter() Counter {
	return JSONByteDivisionCounter{Divisor: 4}
}

// TokenMetrics provides information about token usage optimization.
type TokenMetrics struct {
	// BaselineTokens is the estimated tokens if all tools were sent.
	BaselineTokens int `json:"baseline_tokens"`

	// ReturnedTokens is the actual tokens for the returned tools.
	ReturnedTokens int `json:"returned_tokens"`

	// SavingsPercent is the percentage of tokens saved.
	SavingsPercent float64 `json:"savings_percent"`
}

// ComputeTokenMetrics calculates token savings by comparing the precomputed
// baseline (all tools) against only the matched tool names.
func ComputeTokenMetrics(baselineTokens int, tokenCounts map[string]int, matchedToolNames []string) TokenMetrics {
	if baselineTokens == 0 {
		return TokenMetrics{}
	}

	var returnedTokens int
	for _, name := range matchedToolNames {
		returnedTokens += tokenCounts[name]
	}

	savingsPercent := float64(baselineTokens-returnedTokens) / float64(baselineTokens) * 100

	return TokenMetrics{
		BaselineTokens: baselineTokens,
		ReturnedTokens: returnedTokens,
		SavingsPercent: savingsPercent,
	}
}

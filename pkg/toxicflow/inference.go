// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toxicflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ServerProfile is the public, non-sensitive view of a server passed to a
// SourceInference. It deliberately excludes permission profiles, secrets, and
// runtime config: an inference backend (especially a remote LLM) must never
// receive sensitive data.
type ServerProfile struct {
	Name        string
	Description string
	Overview    string
	Tags        []string
	Tools       []string
	Remote      bool
}

// Hint is a non-authoritative, raise-only suggestion from a SourceInference.
// Hints can only raise a role's confidence (never lower it, never force a
// confident "none"), so a wrong hint costs a spurious warning, not a missed
// flow. Inference backends emit at most ConfPossible — the structured
// openWorldHint is the only basis for a stronger source signal.
type Hint struct {
	Role       Role
	Confidence Confidence
	Reason     string
}

// SourceInference derives untrusted-content (and, where possible, private-data)
// hints from a server's public metadata. Implementations must be safe to call
// with partial profiles and must not return findings stronger than ConfPossible.
type SourceInference interface {
	Infer(ctx context.Context, p ServerProfile) ([]Hint, error)
}

// KeywordInference is the default, offline, deterministic strategy: it matches
// tags, tool names, and description text against curated keyword lists.
type KeywordInference struct{}

// NewKeywordInference returns the keyword-based inference strategy.
func NewKeywordInference() KeywordInference { return KeywordInference{} }

// Infer implements SourceInference using keyword heuristics.
func (KeywordInference) Infer(_ context.Context, p ServerProfile) ([]Hint, error) {
	var hints []Hint
	if tag := matchKeyword(p.Tags, untrustedTagKeywords); tag != "" {
		hints = append(hints, Hint{RoleSource, ConfPossible,
			fmt.Sprintf("tag %q suggests untrusted-content ingestion", tag)})
	}
	if tool := matchKeyword(p.Tools, untrustedToolKeywords); tool != "" {
		hints = append(hints, Hint{RoleSource, ConfPossible,
			fmt.Sprintf("tool %q suggests untrusted-content ingestion", tool)})
	}
	if word := matchWord(p.Description+" "+p.Overview, untrustedTextKeywords); word != "" {
		hints = append(hints, Hint{RoleSource, ConfPossible,
			fmt.Sprintf("description mentions %q (possible untrusted-content ingestion)", word)})
	}
	return hints, nil
}

// Completer is the minimal LLM contract LLMInference needs. It is satisfied by
// pkg/llm/client.Client; defining it here keeps toxicflow free of an LLM-client
// dependency and makes LLMInference unit-testable with a stub.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// LLMInference uses a language model to judge, from the public profile, whether
// a server ingests untrusted content or exposes private data. Its output is
// still capped at ConfPossible and folded raise-only, so a hallucination can
// only over-warn — it can never produce a reassuring "no toxic flow".
type LLMInference struct {
	client Completer
}

// NewLLMInference returns an LLM-backed inference strategy.
func NewLLMInference(c Completer) LLMInference { return LLMInference{client: c} }

const sourceSystemPrompt = "You classify Model Context Protocol (MCP) servers for security review.\n" +
	"Given a server's public metadata, decide two things:\n" +
	"- untrusted_content: does the server ingest content an external party could influence " +
	"(web pages, search results, emails, issue/PR bodies, arbitrary documents)? " +
	"Internal-but-user-generated content (wikis, tickets, mailboxes) counts as untrusted.\n" +
	"- private_data: does the server plausibly access private or sensitive data " +
	"(a user's accounts, repositories, files, internal APIs)?\n" +
	"Respond with ONLY a JSON object: " +
	`{"untrusted_content": bool, "private_data": bool, "reason": "<one short sentence>"}.`

type llmVerdict struct {
	UntrustedContent bool   `json:"untrusted_content"`
	PrivateData      bool   `json:"private_data"`
	Reason           string `json:"reason"`
}

// Infer implements SourceInference by prompting the model and parsing its JSON.
func (l LLMInference) Infer(ctx context.Context, p ServerProfile) ([]Hint, error) {
	out, err := l.client.Complete(ctx, sourceSystemPrompt, buildProfilePrompt(p))
	if err != nil {
		return nil, fmt.Errorf("llm inference: %w", err)
	}
	verdict, err := parseLLMVerdict(out)
	if err != nil {
		return nil, err
	}

	var hints []Hint
	if verdict.UntrustedContent {
		hints = append(hints, Hint{RoleSource, ConfPossible, llmReason("untrusted content", verdict.Reason)})
	}
	if verdict.PrivateData {
		hints = append(hints, Hint{RoleData, ConfPossible, llmReason("private data", verdict.Reason)})
	}
	return hints, nil
}

func buildProfilePrompt(p ServerProfile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", p.Name)
	fmt.Fprintf(&b, "remote: %t\n", p.Remote)
	if p.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", p.Description)
	}
	if p.Overview != "" {
		fmt.Fprintf(&b, "overview: %s\n", p.Overview)
	}
	if len(p.Tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(p.Tags, ", "))
	}
	if len(p.Tools) > 0 {
		fmt.Fprintf(&b, "tools: %s\n", strings.Join(p.Tools, ", "))
	}
	return b.String()
}

// parseLLMVerdict extracts the JSON object from the model output, tolerating
// surrounding prose or code fences.
func parseLLMVerdict(out string) (llmVerdict, error) {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return llmVerdict{}, fmt.Errorf("llm response was not JSON: %q", truncate(out, 120))
	}
	var v llmVerdict
	if err := json.Unmarshal([]byte(out[start:end+1]), &v); err != nil {
		return llmVerdict{}, fmt.Errorf("parse llm response: %w", err)
	}
	return v, nil
}

func llmReason(kind, reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "LLM judged this server to involve " + kind
	}
	return "LLM: " + reason
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

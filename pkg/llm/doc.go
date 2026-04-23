// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package llm provides configuration types and public API for the thv llm
// command group, which bridges AI coding tools to OIDC-protected LLM gateways.
//
// Two authentication modes are supported:
//   - Proxy mode: a localhost reverse proxy that injects fresh OIDC tokens for
//     tools that only accept static API keys (e.g. Cursor).
//   - Token helper mode: thv llm token prints a fresh JWT to stdout, suitable
//     for use as apiKeyHelper or auth.command in OIDC-capable tools (e.g. Claude Code).
//
// Configuration is persisted in ToolHive's config.yaml under the llm: key via
// the existing UpdateConfig() mechanism.
package llm

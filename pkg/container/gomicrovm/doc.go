// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package gomicrovm provides an EXPERIMENTAL microVM runtime for ToolHive
// using the go-microvm framework (libkrun). It runs MCP server OCI images
// inside lightweight virtual machines instead of containers, providing
// hardware-level isolation.
//
// Enable with TOOLHIVE_RUNTIME=go-microvm. Requires Linux with KVM support
// and the go-microvm-runner binary.
//
// This runtime supports HTTP-based MCP transports (SSE, streamable-http).
// The stdio transport is not supported.
package gomicrovm

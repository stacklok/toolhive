// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package inspector contains definitions for the inspector command.
package inspector

// Image specifies the image to use for the inspector command.
// TODO: This could probably be a flag with a sensible default
// Pinning to a specific version for stability.
var Image = "ghcr.io/modelcontextprotocol/inspector:0.21.1"

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app_test

import (
	"github.com/stacklok/toolhive/pkg/vmcp/app/internal/exampleembedder"
)

// Referencing the example embedder keeps it in this test binary's build graph, so a
// breaking change to BuildCore, BuildServerConfig, Option, or vmcpserver.Serve that the
// embedder consumes fails `task test` instead of going undetected (nothing else imports
// the internal example package). Mirrors the anchor in
// pkg/vmcp/backendregistry/importgraph_test.go.
var _ = exampleembedder.BuildServer

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package upgrade implements registry-sourced workload upgrade checks for
// ToolHive (RFC THV-0068). It compares the image and configuration of a
// running workload against the latest metadata served by the registry and
// reports whether an upgrade is available, along with any environment-variable
// or posture (transport/network/permission) drift.
//
// # Dependency direction (cycle guard)
//
// This package depends on pkg/workloads (for the apply path in later phases)
// and pkg/runner. To avoid an import cycle, pkg/workloads MUST NEVER import
// this package. Higher-level entry points (CLI, API handlers) wire the two
// together; the manager itself stays unaware of upgrade logic.
//
// # No rollback
//
// The apply path (Phase D) intentionally provides no rollback. It resolves,
// verifies, and pulls the candidate image BEFORE destroying the existing
// workload so that a failure during preparation leaves the running workload
// untouched. Once the workload is deleted and recreated, there is no automatic
// revert to the previous image or configuration; recovery is a forward
// operation (re-running the previous configuration explicitly).
package upgrade

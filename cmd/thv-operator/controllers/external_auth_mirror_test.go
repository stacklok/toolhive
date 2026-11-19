// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

// Arbitrary source Valid=False reason and message used by the Status Condition
// Parity mirror tests. The mirror copies whatever reason the referenced
// MCPExternalAuthConfig carries, so the tests use a value no controller sets
// itself (unlike InvalidConfig, which ownedByLocalValidation treats specially).
const (
	testSourceInvalidReason  = "SourceRejected"
	testSourceInvalidMessage = "the referenced MCPExternalAuthConfig was rejected"
)

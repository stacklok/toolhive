// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

// Shared condition types used across config controllers.
const (
	ConditionTypeValid           = "Valid"
	ConditionTypeDeletionBlocked = "DeletionBlocked"

	// ConditionReasonUnsupportedAuthType indicates that an MCPExternalAuthConfig
	// type is not implemented by the resource that references it.
	ConditionReasonUnsupportedAuthType = "UnsupportedAuthType"

	// ConditionReasonAppliedByVirtualMCPServer indicates that the referenced
	// MCPExternalAuthConfig is valid, but its type is not applied by the
	// resource that references it. It is reported on a True condition: the
	// reference is accepted so that a VirtualMCPServer can discover it on this
	// backend and apply it, and it has no effect on a backend that no
	// VirtualMCPServer discovers.
	ConditionReasonAppliedByVirtualMCPServer = "AppliedByVirtualMCPServer"
)

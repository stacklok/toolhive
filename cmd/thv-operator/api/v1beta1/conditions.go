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
)

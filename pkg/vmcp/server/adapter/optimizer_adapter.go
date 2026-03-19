// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapter

// OptimizerToolNames defines the tool names exposed when optimizer mode is enabled.
// These constants are kept here for backwards compatibility with existing tests and
// callers. The actual tool implementation lives in the optimizerdec session decorator.
const (
	FindToolName = "find_tool"
	CallToolName = "call_tool"
)

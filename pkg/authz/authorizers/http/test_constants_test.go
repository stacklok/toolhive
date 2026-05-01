// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

// Shared constants used across http authorizer test files.
const (
	// Claim key constants (MPE-specific prefixed names).
	testClaimAnnotations  = "annotations"
	testClaimGroups       = "groups"
	testClaimLocation     = "location"
	testClaimMannotations = "mannotations"
	testClaimMclearance   = "mclearance"
	testClaimMgroups      = "mgroups"
	testClaimMroles       = "mroles"
	testClaimRoles        = "roles"
	testClaimScope        = "scope"
	testClaimScopes       = "scopes"
	testClaimSub          = "sub"

	// Claim value constants.
	testClearanceSecret = "SECRET"
	testClearanceTop    = "TOP_SECRET"
	testDeptKey         = "dept"
	testGroupEng        = "engineering"
	testGroupSecurity   = "security"
	testRoleAdmin       = "admin"
	testRoleDeveloper   = "developer"

	// Scope constants.
	testScopeRead      = "read"
	testScopeReadWrite = "read write"
	testScopeWrite     = "write"

	// Test case name constants.
	testNameBasicClaims     = "basic claims"
	testNameMissingPDPField = "missing pdp field"
	testNameNilClaims       = "nil claims"
	testNameValidHTTPConfig = "valid HTTP config"

	// PDP URL and claim mapping constants.
	testClaimMapping  = ClaimMappingMPE
	testClaimStandard = ClaimMappingStandard
	testPDPURL        = "http://localhost:9000"

	// Error message constants.
	testErrMsgPDPRequired = "pdp configuration is required"

	// Operation/resource constants.
	testMRNToolWeather = "mrn:mcp:test:tool:weather"
	testOpToolCall     = "mcp:tool:call"
	testOpToolTest     = "mcp:tool:test"
	testResDataJSON    = "file://data.json"
	testResGreeting    = "greeting"
	testResWeather     = "weather"

	// PORC field key constants (test-visible aliases of production constants).
	testKeyAnnotations = porcKeyAnnotations
	testKeyContext     = porcKeyContext
	testKeyMCP         = porcKeyMCP
	testKeyOperation   = "operation"
	testKeyPrincipal   = "principal"
	testKeyResource    = "resource"

	// Enrichment test annotation keys.
	testAnnoDestructiveHint = "destructiveHint"
	testAnnoReadOnlyHint    = "readOnlyHint"

	// Enrichment test field keys.
	testFieldServerID = "server_id"
	testFieldTool     = "tool"
	testFieldTestSrv  = "test-server"

	// User/subject constants.
	testSubjectUser  = "user@example.com"
	testPrincipalSub = "test"

	// Raw JSON config snippets.
	testRawConfigMissingPDP = `{
				"version": "1.0",
				"type": "httpv1"
			}`

	// Location value constant.
	testLocation = "New York"
)

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package images provides centralized container image references for e2e tests.
// This package serves as a single source of truth for all container images used
// in end-to-end testing, making it easier to maintain versions and enabling
// automated dependency updates through tools like Renovate.
//
// Each image is composed of an imageURL (base path) and imageTag (version).
// The complete Image constant combines the URL and tag for use in tests.
package images

const (
	yardstickServerImageURL = "ghcr.io/stackloklabs/yardstick/yardstick-server"
	yardstickServerImageTag = "1.2.0"
	// YardstickServerImage (go-sdk v1.7) is the yardstick backend used across the
	// operator, vMCP, and dual-era e2e tests, over multiple transport protocols (stdio,
	// SSE, streamable-http) and tenancy modes. A session-mode deployment negotiates down
	// to the Legacy (2025-11-25) data plane and the vMCP classifies it Legacy from
	// server/discover's supportedVersions (#5993), so this single image serves both
	// eras -- the dual-era tests drive it Modern (stateless), the rest drive it Legacy.
	// Note: This image is also referenced in 8 YAML fixture files under
	// test/e2e/chainsaw/operator/. Those files are declarative Kubernetes manifests
	// and cannot import Go constants directly.
	YardstickServerImage = yardstickServerImageURL + ":" + yardstickServerImageTag

	gofetchServerImageURL = "ghcr.io/stackloklabs/gofetch/server"
	gofetchServerImageTag = "1.0.1"
	// GofetchServerImage is used for testing virtual MCP server features, including
	// authentication flows and backend aggregation.
	GofetchServerImage = gofetchServerImageURL + ":" + gofetchServerImageTag

	osvmcpServerImageURL = "ghcr.io/stackloklabs/osv-mcp/server"
	osvmcpServerImageTag = "0.0.7"
	// OSVMCPServerImage is used for testing discovered mode aggregation and telemetry
	// metrics validation.
	OSVMCPServerImage = osvmcpServerImageURL + ":" + osvmcpServerImageTag

	pythonImageURL = "python"
	pythonImageTag = "3.9-slim"
	// PythonImage is used for deploying mock OIDC servers and instrumented backend servers
	// in Kubernetes tests. These run Flask-based Python services for testing authentication flows.
	PythonImage = pythonImageURL + ":" + pythonImageTag

	curlImageURL = "curlimages/curl"
	curlImageTag = "8.17.0"
	// CurlImage is used to query service endpoints and gather statistics during Kubernetes tests.
	CurlImage = curlImageURL + ":" + curlImageTag

	githubMCPServerImageURL = "ghcr.io/github/github-mcp-server"
	githubMCPServerImageTag = "v0.32.0"
	// GitHubMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Note: This server requires a GitHub token for tool execution; tests that include
	// it should only verify tool discovery, not invocation.
	GitHubMCPServerImage = githubMCPServerImageURL + ":" + githubMCPServerImageTag

	textEmbeddingsInferenceImageURL = "ghcr.io/huggingface/text-embeddings-inference"
	textEmbeddingsInferenceImageTag = "cpu-latest"
	// TextEmbeddingsInferenceImage is used for testing EmbeddingServer deployments
	// in optimizer mode tests. Uses the CPU variant for CI environments without GPU.
	TextEmbeddingsInferenceImage = textEmbeddingsInferenceImageURL + ":" + textEmbeddingsInferenceImageTag

	terraformMCPServerImageURL = "docker.io/hashicorp/terraform-mcp-server"
	terraformMCPServerImageTag = "0.4.0"
	// TerraformMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Provides ~78 Terraform-related tools (registry lookup, workspace management, etc.).
	TerraformMCPServerImage = terraformMCPServerImageURL + ":" + terraformMCPServerImageTag

	playwrightMCPServerImageURL = "mcr.microsoft.com/playwright/mcp"
	playwrightMCPServerImageTag = "v0.0.68"
	// PlaywrightMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Provides ~44 browser automation tools (navigate, click, fill, screenshot, etc.).
	PlaywrightMCPServerImage = playwrightMCPServerImageURL + ":" + playwrightMCPServerImageTag

	puppeteerMCPServerImageURL = "docker.io/mcp/puppeteer"
	puppeteerMCPServerImageTag = "latest"
	// PuppeteerMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Provides ~7 browser automation tools (navigate, click, fill, screenshot, etc.).
	PuppeteerMCPServerImage = puppeteerMCPServerImageURL + ":" + puppeteerMCPServerImageTag

	memoryMCPServerImageURL = "docker.io/mcp/memory"
	memoryMCPServerImageTag = "latest"
	// MemoryMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Provides ~18 in-memory knowledge graph tools (create entities, relations, search, etc.).
	MemoryMCPServerImage = memoryMCPServerImageURL + ":" + memoryMCPServerImageTag

	everythingMCPServerImageURL = "docker.io/mcp/everything"
	everythingMCPServerImageTag = "latest"
	// EverythingMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Reference MCP test server providing ~16 diverse example tools.
	EverythingMCPServerImage = everythingMCPServerImageURL + ":" + everythingMCPServerImageTag

	idaProMCPServerImageURL = "ghcr.io/stacklok/dockyard/uvx/ida-pro-mcp"
	idaProMCPServerImageTag = "1.4.0"
	// idaProMCPServerImageDigest pins the 2026-07-27 build of tag 1.4.0.
	//
	// The tag is mutable: dockyard periodically rebuilds it and resolves Python
	// dependencies fresh each time. ida-pro-mcp 1.4.0 declares an unbounded
	// "mcp>=1.16.0" and imports mcp.server.fastmcp, which was removed in the
	// mcp Python SDK 2.0.0 (released 2026-07-28). The 2026-07-31 rebuild picked
	// up mcp 2.0.0, so the container now crashloops on startup with
	// "ModuleNotFoundError: No module named 'mcp.server.fastmcp'".
	//
	// 1.4.0 is the newest ida-pro-mcp release on PyPI, so there is nothing to
	// bump to. Pin the last build that resolved a 1.x mcp (1.28.1) until
	// upstream caps the constraint or adopts the 2.x API. Every other backend
	// in the optimizer test is unaffected -- pagerduty-mcp, the only other
	// dockyard uvx image, caps "mcp[cli]~=1.8".
	idaProMCPServerImageDigest = "sha256:5a596d965fe05052d615a41152213f93ebc72bf46b07304718de948833d73c70"
	// IDAProMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Provides ~47 IDA Pro reverse engineering tools (decompile, disassemble, rename, etc.).
	IDAProMCPServerImage = idaProMCPServerImageURL + ":" + idaProMCPServerImageTag + "@" + idaProMCPServerImageDigest

	pagerdutyMCPServerImageURL = "ghcr.io/stacklok/dockyard/uvx/pagerduty-mcp"
	pagerdutyMCPServerImageTag = "0.12.0"
	// PagerDutyMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Provides ~64 PagerDuty incident management tools (incidents, services, schedules, etc.).
	PagerDutyMCPServerImage = pagerdutyMCPServerImageURL + ":" + pagerdutyMCPServerImageTag

	timeMCPServerImageURL = "ghcr.io/stacklok/dockyard/uvx/mcp-server-time"
	timeMCPServerImageTag = "2026.7.10"
	// timeMCPServerImageDigest pins the 2026-07-27 build of tag 2026.7.10.
	//
	// Same failure mode as idaProMCPServerImageDigest above, different symptom:
	// the tag is mutable, dockyard rebuilds it and resolves Python dependencies
	// fresh, and the 2026-07-31 rebuild picked up the mcp Python SDK 2.0.0
	// released 2026-07-28. mcp-server-time imports McpError from
	// mcp.shared.exceptions, which 2.x moved, so the container now exits on
	// startup with "ImportError: cannot import name 'McpError' from
	// 'mcp.shared.exceptions'". The proxy suites then fail with
	// "timeout waiting for MCP server to be ready".
	//
	// Pin the last build that resolved a 1.x mcp until upstream caps the
	// constraint or adopts the 2.x API. Verified by driving both builds over
	// stdio: the pinned digest answers initialize, tag 2026.7.10 does not.
	timeMCPServerImageDigest = "sha256:5cca77dec3fefbacad35e3008ab1660f8725ef246213afb24231504fc73c999d"
	// TimeMCPServerImage is the stdio backend used by the proxy e2e suites
	// (proxy_stdio_test.go, stdio_proxy_over_streamable_http_mcp_server_test.go).
	// Provides get_current_time / convert_time.
	//
	// These suites reference this constant rather than the "time" registry entry
	// so a mutable upstream tag cannot break CI: the registry resolves "time" to
	// the moving :2026.7.10 tag, which is currently broken. Registry-name
	// resolution itself is still covered by the suites that run "osv".
	TimeMCPServerImage = timeMCPServerImageURL + ":" + timeMCPServerImageTag + "@" + timeMCPServerImageDigest

	redisImageURL = "redis"
	redisImageTag = "7-alpine"
	// RedisImage is used for Redis-backed session storage in scaling tests.
	RedisImage = redisImageURL + ":" + redisImageTag

	dexImageURL = "ghcr.io/dexidp/dex"
	dexImageTag = "v2.42.0"
	// DexImage is used as an in-cluster OIDC provider for E2E tests requiring
	// the embedded auth server OAuth2 flow (upstreamInject authentication).
	DexImage = dexImageURL + ":" + dexImageTag
)

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
	yardstickServerImageTag = "1.1.1"
	// YardstickServerImage is used in operator tests across multiple transport protocols
	// (stdio, SSE, streamable-http) and tenancy modes.
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
	// IDAProMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Provides ~47 IDA Pro reverse engineering tools (decompile, disassemble, rename, etc.).
	IDAProMCPServerImage = idaProMCPServerImageURL + ":" + idaProMCPServerImageTag

	pagerdutyMCPServerImageURL = "ghcr.io/stacklok/dockyard/uvx/pagerduty-mcp"
	pagerdutyMCPServerImageTag = "0.12.0"
	// PagerDutyMCPServerImage is used for testing multi-backend optimizer scenarios.
	// Provides ~64 PagerDuty incident management tools (incidents, services, schedules, etc.).
	PagerDutyMCPServerImage = pagerdutyMCPServerImageURL + ":" + pagerdutyMCPServerImageTag
)

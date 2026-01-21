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
	yardstickServerImageTag = "0.0.2"
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
)

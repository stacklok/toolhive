// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	catalog "github.com/stacklok/toolhive-catalog/pkg/catalog/toolhive"
	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/networking"
)

// Source loads raw registry data from a specific backend (embedded bytes,
// local file, or remote URL). Every Source returns a LoadResult that
// contains the parsed servers and skills without further conversion.
type Source interface {
	Load(ctx context.Context) (*LoadResult, error)
}

// LoadResult holds the raw server and skill data extracted from an
// upstream registry payload.
type LoadResult struct {
	Servers []*v0.ServerJSON
	Skills  []types.Skill
}

// EmbeddedSource reads the embedded catalog data compiled into the binary.
type EmbeddedSource struct{}

// Load returns the embedded upstream catalog servers and skills.
func (*EmbeddedSource) Load(_ context.Context) (*LoadResult, error) {
	return parseUpstreamData(catalog.Upstream())
}

// FileSource reads upstream registry data from a local file.
type FileSource struct {
	Path string
}

// Load reads the file at Path and parses it as an upstream registry.
func (s *FileSource) Load(_ context.Context) (*LoadResult, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read registry file %s: %w", s.Path, err)
	}
	return parseUpstreamData(data)
}

// URLSource fetches upstream registry data from a remote HTTP endpoint.
type URLSource struct {
	URL            string
	AllowPrivateIP bool
}

// Load performs an HTTP GET to the configured URL and parses the response
// as an upstream registry. The HTTP client is built with the same security
// controls used elsewhere in the registry package (private-IP gating, optional
// HTTP for localhost testing).
func (s *URLSource) Load(ctx context.Context) (*LoadResult, error) {
	builder := networking.NewHttpClientBuilder().WithPrivateIPs(s.AllowPrivateIP)
	if s.AllowPrivateIP {
		builder = builder.WithInsecureAllowHTTP(true)
	}
	client, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build HTTP client: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for %s: %w", s.URL, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry data from %s: %w", s.URL, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Debug("failed to close response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry at %s returned status %d", s.URL, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body from %s: %w", s.URL, err)
	}

	return parseUpstreamData(data)
}

// parseUpstreamData unmarshals raw JSON as an UpstreamRegistry and extracts
// the server pointer slice and skills. This is the shared parsing logic used
// by all Source implementations.
func parseUpstreamData(data []byte) (*LoadResult, error) {
	var upstream types.UpstreamRegistry
	if err := json.Unmarshal(data, &upstream); err != nil {
		return nil, fmt.Errorf("failed to parse upstream registry data: %w", err)
	}

	// UpstreamData.Servers is []v0.ServerJSON; build a pointer slice so
	// callers can work with []*v0.ServerJSON consistently.
	servers := make([]*v0.ServerJSON, len(upstream.Data.Servers))
	for i := range upstream.Data.Servers {
		servers[i] = &upstream.Data.Servers[i]
	}

	return &LoadResult{
		Servers: servers,
		Skills:  upstream.Data.Skills,
	}, nil
}

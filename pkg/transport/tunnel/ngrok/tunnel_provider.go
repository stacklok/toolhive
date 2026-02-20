// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ngrok provides an implementation of the TunnelProvider interface using ngrok.
package ngrok

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.ngrok.com/ngrok/v2"
	"gopkg.in/yaml.v3"
)

// TunnelProvider implements the TunnelProvider interface for ngrok.
type TunnelProvider struct {
	config TunnelConfig
}

// TunnelConfig holds configuration options for the ngrok tunnel provider.
type TunnelConfig struct {
	AuthToken      string //nolint:gosec // G117: field legitimately holds sensitive data
	URL            string // Optional: specify custom URL
	TrafficPolicy  string // Optional: specify traffic policy
	PoolingEnabled bool   // Optional: enable pooling
	DryRun         bool
}

// loadTrafficPolicyFile reads a YAML file, ensures it's .yml/.yaml,
// validates its contents, and returns its text.
func loadTrafficPolicyFile(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".yml" && ext != ".yaml" {
		return "", fmt.Errorf("traffic policy file must be .yml or .yaml, got %q", ext)
	}

	cleanPath := filepath.Clean(path)
	b, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", fmt.Errorf("reading traffic policy file: %w", err)
	}

	var tmp any
	if err := yaml.Unmarshal(b, &tmp); err != nil {
		return "", fmt.Errorf("invalid YAML in traffic policy file: %w", err)
	}

	return string(b), nil
}

// ParseConfig parses the configuration for the ngrok tunnel provider from a map.
func (p *TunnelProvider) ParseConfig(raw map[string]any) error {
	token, ok := raw["auth-token"].(string)
	if !ok || token == "" {
		return fmt.Errorf("auth-token is required")
	}

	cfg := TunnelConfig{
		AuthToken: token,
	}

	// optional settings: url, traffic policy, pooling
	if url, ok := raw["url"].(string); ok {
		cfg.URL = url
	}
	if path, ok := raw["traffic-policy-file"].(string); ok && path != "" {
		policyText, err := loadTrafficPolicyFile(path)
		if err != nil {
			return err
		}
		cfg.TrafficPolicy = policyText
	}
	if pooling, ok := raw["pooling"].(bool); ok {
		cfg.PoolingEnabled = pooling
	}

	p.config = cfg

	if dr, ok := raw["dry-run"].(bool); ok {
		p.config.DryRun = dr
	}

	return nil
}

// StartTunnel starts a tunnel using ngrok to the specified target URI.
func (p *TunnelProvider) StartTunnel(ctx context.Context, name, targetURI string) error {
	if p.config.DryRun {
		// behave like an active tunnel that exits on ctx cancel
		<-ctx.Done()
		return nil
	}
	//nolint:gosec // G706: logging tunnel name and target URI from config
	slog.Info("starting ngrok tunnel", "name", name, "target", targetURI)

	agent, err := ngrok.NewAgent(
		ngrok.WithAuthtoken(p.config.AuthToken),
		ngrok.WithEventHandler(func(e ngrok.Event) {
			//nolint:gosec // G706: logging ngrok event details
			slog.Info("ngrok event",
				"type", e.EventType(),
				"timestamp", e.Timestamp(),
			)
		}),
	)

	if err != nil {
		return fmt.Errorf("failed to create ngrok agent: %w", err)
	}

	// Set up only the necessary endpoint options
	endpointOpts := []ngrok.EndpointOption{
		ngrok.WithDescription("tunnel proxy for " + name),
	}
	if p.config.URL != "" {
		endpointOpts = append(endpointOpts, ngrok.WithURL(p.config.URL))
	}
	if p.config.TrafficPolicy != "" {
		endpointOpts = append(endpointOpts, ngrok.WithTrafficPolicy(p.config.TrafficPolicy))
	}
	if p.config.PoolingEnabled {
		endpointOpts = append(endpointOpts, ngrok.WithPoolingEnabled(true))
	}

	forwarder, err := agent.Forward(ctx,
		ngrok.WithUpstream(targetURI),
		endpointOpts...,
	)
	if err != nil {
		return fmt.Errorf("ngrok.Forward error: %w", err)
	}

	//nolint:gosec // G706: logging ngrok forwarder URL from runtime
	slog.Info("ngrok forwarding live", "url", forwarder.URL())

	// Run in background, non-blocking on `.Done()`
	go func() {
		<-forwarder.Done()
		//nolint:gosec // G706: logging ngrok forwarder URL from runtime
		slog.Info("ngrok forwarding stopped", "url", forwarder.URL())
	}()

	// Return immediately
	return nil
}

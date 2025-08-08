// Package ngrok provides an implementation of the TunnelProvider interface using ngrok.
package ngrok

import (
	"context"
	"fmt"

	"golang.ngrok.com/ngrok/v2"

	"github.com/stacklok/toolhive/pkg/logger"
)

// TunnelProvider implements the TunnelProvider interface for ngrok.
type TunnelProvider struct {
	config TunnelConfig
}

// TunnelConfig holds configuration options for the ngrok tunnel provider.
type TunnelConfig struct {
	AuthToken string
	Domain    string // Optional: specify custom domain
	DryRun    bool
}

// ParseConfig parses the configuration for the ngrok tunnel provider from a map.
func (p *TunnelProvider) ParseConfig(raw map[string]any) error {
	token, ok := raw["ngrok-auth-token"].(string)
	if !ok || token == "" {
		return fmt.Errorf("ngrok-auth-token is required")
	}

	cfg := TunnelConfig{
		AuthToken: token,
	}

	if domain, ok := raw["ngrok-domain"].(string); ok {
		cfg.Domain = domain
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
	logger.Infof("[ngrok] Starting tunnel %q → %s", name, targetURI)

	agent, err := ngrok.NewAgent(
		ngrok.WithAuthtoken(p.config.AuthToken),
		ngrok.WithEventHandler(func(e ngrok.Event) {
			logger.Infof("ngrok event: %s at %s", e.EventType(), e.Timestamp())
		}),
	)

	if err != nil {
		return fmt.Errorf("failed to create ngrok agent: %w", err)
	}

	// Set up only the necessary endpoint options
	endpointOpts := []ngrok.EndpointOption{
		ngrok.WithDescription("tunnel proxy for " + name),
	}
	if p.config.Domain != "" {
		endpointOpts = append(endpointOpts, ngrok.WithURL(p.config.Domain))
	}

	forwarder, err := agent.Forward(ctx,
		ngrok.WithUpstream(targetURI),
		endpointOpts...,
	)
	if err != nil {
		return fmt.Errorf("ngrok.Forward error: %w", err)
	}

	logger.Infof("ngrok forwarding live at %s", forwarder.URL())

	// Run in background, non-blocking on `.Done()`
	go func() {
		<-forwarder.Done()
		logger.Infof("ngrok forwarding stopped: %s", forwarder.URL())
	}()

	// Return immediately
	return nil
}

package types

import (
	"context"

	"github.com/stacklok/toolhive/pkg/transport/tunnel/ngrok"
)

// SupportedTunnelProviders maps provider names to their implementations.
var SupportedTunnelProviders = map[string]TunnelProvider{
	"ngrok": &ngrok.TunnelProvider{},
}

// TunnelProvider defines the interface for tunnel providers.
type TunnelProvider interface {
	ParseConfig(config map[string]any) error
	StartTunnel(ctx context.Context, name string, targetURI string) error
}

// GetSupportedProviderNames returns a list of supported tunnel provider names.
func GetSupportedProviderNames() []string {
	names := make([]string, 0, len(SupportedTunnelProviders))
	for name := range SupportedTunnelProviders {
		names = append(names, name)
	}
	return names
}

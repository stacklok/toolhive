package egress

import (
	"context"
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
)

// Factory creates and manages egress proxies
type Factory struct {
	mutex   sync.Mutex
	proxies map[string]Proxy
}

// NewFactory creates a new egress proxy factory
func NewFactory() *Factory {
	return &Factory{
		proxies: make(map[string]Proxy),
	}
}

// GetOrCreateProxy gets an existing proxy or creates a new one
func (f *Factory) GetOrCreateProxy(
	ctx context.Context,
	name string,
	port int,
	host string,
	profile *permissions.Profile,
) (Proxy, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	// Check if proxy already exists
	if proxy, ok := f.proxies[name]; ok {
		return proxy, nil
	}

	// Create config from profile
	config := NewConfig(name, port, host)

	// Configure from profile if available
	if profile != nil && profile.Network != nil && profile.Network.Outbound != nil {
		outbound := profile.Network.Outbound
		config.AllowedHosts = outbound.AllowHost
		config.AllowedPorts = outbound.AllowPort
		config.AllowedTransports = outbound.AllowTransport
	}

	// Create proxy
	proxy := NewHTTPEgressProxy(config)

	// Start proxy
	if err := proxy.Start(ctx); err != nil {
		return nil, err
	}

	// Store proxy
	f.proxies[name] = proxy
	logger.Log.Info("Created and started egress proxy: " + name)

	return proxy, nil
}

// StopProxy stops and removes a proxy
func (f *Factory) StopProxy(ctx context.Context, name string) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	// Check if proxy exists
	proxy, ok := f.proxies[name]
	if !ok {
		return nil
	}

	// Stop proxy
	if err := proxy.Stop(ctx); err != nil {
		return err
	}

	// Remove proxy
	delete(f.proxies, name)
	logger.Log.Info("Stopped and removed egress proxy: " + name)

	return nil
}

// StopAll stops all proxies
func (f *Factory) StopAll(ctx context.Context) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	for name, proxy := range f.proxies {
		if err := proxy.Stop(ctx); err != nil {
			logger.Log.Warn("Failed to stop egress proxy: " + name)
		}
		delete(f.proxies, name)
	}

	return nil
}

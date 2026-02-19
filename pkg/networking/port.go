// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package networking provides utilities for network operations,
// such as finding available ports and checking network connectivity.
package networking

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"strconv"
	"strings"
)

const (
	// MinPort is the minimum port number to use
	MinPort = 10000
	// MaxPort is the maximum port number to use
	MaxPort = 65535
	// MaxAttempts is the maximum number of attempts to find an available port
	MaxAttempts = 10
)

// IsAvailable checks if a port is available
func IsAvailable(port int) bool {
	// Check TCP
	tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}

	tcpListener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return false
	}
	if err := tcpListener.Close(); err != nil {
		// Log the error but continue, as we're just checking if the port is available
		slog.Warn("Failed to close TCP listener", "error", err)
	}

	// Check UDP
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}

	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return false
	}
	if err := udpConn.Close(); err != nil {
		// Log the error but continue, as we're just checking if the port is available
		slog.Warn("Failed to close UDP connection", "error", err)
	}

	return true
}

// IsIPv6Available checks if IPv6 is available on the system
// by looking for IPv6 addresses on network interfaces
func IsIPv6Available() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			// Interface is down
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			if ipNet.IP.To4() == nil && !ipNet.IP.IsLoopback() {
				// This is an IPv6 address and not a loopback
				return true
			}
		}
	}

	return false
}

// FindAvailable finds an available port
func FindAvailable() int {
	for i := 0; i < MaxAttempts; i++ {
		// Generate a cryptographically secure random number
		n, err := rand.Int(rand.Reader, big.NewInt(int64(MaxPort-MinPort)))
		if err != nil {
			// Fall back to sequential search if random generation fails
			break
		}
		port := int(n.Int64()) + MinPort
		if IsAvailable(port) {
			return port
		}
	}

	// If we can't find a random port, try sequential ports
	for port := MinPort; port <= MaxPort; port++ {
		if IsAvailable(port) {
			return port
		}
	}

	// If we still can't find a port, return 0
	return 0
}

// FindOrUsePort checks if the provided port is available or finds an available port if none is provided.
// If port is 0, it will find an available port.
// If port is not 0, it will check if the port is available.
// Returns the selected port and an error if any.
func FindOrUsePort(port int) (int, error) {
	if port == 0 {
		// Find an available port
		port = FindAvailable()
		if port == 0 {
			return 0, fmt.Errorf("could not find an available port")
		}
		return port, nil
	}

	if IsAvailable(port) {
		return port, nil
	}

	// Requested port is busy — find an alternative
	alt := FindAvailable()
	if alt == 0 {
		return 0, fmt.Errorf("failed to find an alternative port after requested port %d was unavailable", port)
	}
	return alt, nil
}

// ValidateCallbackPort validates that the specified callback port is valid and available.
// It checks that the port is within the valid range (1-65535) and, for pre-registered
// clients (with clientID), it returns an error if the port is not available.
func ValidateCallbackPort(callbackPort int, clientID string) error {
	// If port is 0, we'll find an available port later, so no need to validate
	if callbackPort == 0 {
		return nil
	}

	// Validate port range
	if callbackPort < 1024 || callbackPort > 65535 {
		return fmt.Errorf("OAuth callback port must be between 1024 and 65535, got: %d", callbackPort)
	}

	// Check if this is a pre-registered client (has client credentials)
	// For pre-registered clients, we need strict port checking
	isPreRegisteredClient := IsPreRegisteredClient(clientID)

	if isPreRegisteredClient {
		// For pre-registered clients, the port must be available
		// The user likely configured this port in their IdP/app
		if !IsAvailable(callbackPort) {
			return fmt.Errorf("OAuth callback port %d is not available - please choose a different port", callbackPort)
		}
	}

	return nil
}

// IsPreRegisteredClient determines if the OAuth client is pre-registered (has client ID)
func IsPreRegisteredClient(clientID string) bool {
	return clientID != ""
}

// ParsePortSpec parses a port specification string in the format "hostPort:containerPort" or just "containerPort".
// Returns the host port string and container port integer.
// If only a container port is provided, a random available host port is selected.
func ParsePortSpec(portSpec string) (string, int, error) {
	slog.Debug("Parsing port spec", "spec", portSpec)
	// Check if it's in host:container format
	if strings.Contains(portSpec, ":") {
		parts := strings.Split(portSpec, ":")
		if len(parts) != 2 {
			return "", 0, fmt.Errorf("invalid port specification: %s (expected 'hostPort:containerPort')", portSpec)
		}

		hostPortStr := parts[0]
		containerPortStr := parts[1]

		// Verify host port is a valid integer (or empty string if we supported random host port with :, but here we expect explicit)
		if _, err := strconv.Atoi(hostPortStr); err != nil {
			return "", 0, fmt.Errorf("invalid host port in spec '%s': %w", portSpec, err)
		}

		containerPort, err := strconv.Atoi(containerPortStr)
		if err != nil {
			return "", 0, fmt.Errorf("invalid container port in spec '%s': %w", portSpec, err)
		}

		return hostPortStr, containerPort, nil
	}

	// Try parsing as just container port
	containerPort, err := strconv.Atoi(portSpec)
	if err == nil {
		// Find a random available host port
		hostPort := FindAvailable()
		if hostPort == 0 {
			return "", 0, fmt.Errorf("could not find an available port for container port %d", containerPort)
		}
		return fmt.Sprintf("%d", hostPort), containerPort, nil
	}

	return "", 0, fmt.Errorf("invalid port specification: %s (expected 'hostPort:containerPort' or 'containerPort')", portSpec)
}

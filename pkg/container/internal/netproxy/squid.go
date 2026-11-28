// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package netproxy

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/stacklok/toolhive-core/permissions"
)

type proxyDirection int

const (
	proxyIngress proxyDirection = iota
	proxyEgress
)

// BuildEgressSquidConf builds the Squid egress proxy configuration text.
func BuildEgressSquidConf(
	networkPermissions *permissions.NetworkPermissions,
	serverHostname string,
	allowDockerGateway bool,
	gatewayIPs []netip.Addr,
) string {
	var sb strings.Builder

	writeCommonConfig(&sb, serverHostname, proxyEgress)

	// Handle gateway access. MUST precede any http_access allow — Squid is
	// first-match-wins. When AllowDockerGateway is set, emit explicit allow
	// rules so the flag alone grants host access; otherwise block the gateway
	// addresses.
	if allowDockerGateway {
		writeGatewayAllowRules(&sb, gatewayIPs)
	} else {
		writeGatewayDenyRules(&sb, gatewayIPs)
	}

	if networkPermissions == nil || (networkPermissions.Outbound != nil && networkPermissions.Outbound.InsecureAllowAll) {
		sb.WriteString("# Allow all traffic\nhttp_access allow all\n")
	} else {
		writeOutboundACLs(&sb, networkPermissions.Outbound)
		writeHTTPAccessRules(&sb, networkPermissions.Outbound)
	}

	sb.WriteString("http_access deny all\n")

	return sb.String()
}

// CreateTempEgressSquidConf writes a temporary Squid egress configuration file
// readable by the proxy container (0644) and returns its path.
func CreateTempEgressSquidConf(
	networkPermissions *permissions.NetworkPermissions,
	serverHostname string,
	allowDockerGateway bool,
	gatewayIPs []netip.Addr,
) (string, error) {
	return writeSquidTempFile(BuildEgressSquidConf(networkPermissions, serverHostname, allowDockerGateway, gatewayIPs))
}

// BuildIngressSquidConf builds the Squid ingress reverse-proxy configuration text.
func BuildIngressSquidConf(
	serverHostname string,
	upstreamHost string,
	upstreamPort int,
	squidPort int,
	networkPermissions *permissions.NetworkPermissions,
) string {
	var sb strings.Builder

	writeCommonConfig(&sb, serverHostname, proxyIngress)
	writeIngressProxyConfig(&sb, serverHostname, upstreamHost, upstreamPort, squidPort, networkPermissions)
	sb.WriteString("http_access deny all\n")

	return sb.String()
}

// CreateTempIngressSquidConf writes a temporary Squid ingress reverse-proxy
// configuration file readable by the proxy container (0644) and returns its path.
func CreateTempIngressSquidConf(
	serverHostname string,
	upstreamHost string,
	upstreamPort int,
	squidPort int,
	networkPermissions *permissions.NetworkPermissions,
) (string, error) {
	return writeSquidTempFile(BuildIngressSquidConf(serverHostname, upstreamHost, upstreamPort, squidPort, networkPermissions))
}

func writeSquidTempFile(content string) (string, error) {
	tmpFile, err := os.CreateTemp("", "squid-*.conf")
	if err != nil {
		return "", err
	}
	created := tmpFile.Name()
	defer func() {
		if err := tmpFile.Close(); err != nil {
			slog.Warn("failed to close temp file", "error", err)
		}
	}()

	if _, err := tmpFile.WriteString(content); err != nil {
		_ = os.Remove(created)
		return "", fmt.Errorf("failed to write to temporary file: %w", err)
	}

	// Set file permissions to be readable by all users (including squid user in container).
	if err := tmpFile.Chmod(0644); err != nil {
		_ = os.Remove(created)
		return "", fmt.Errorf("failed to set file permissions: %w", err)
	}

	return created, nil
}

func writeGatewayDenyRules(sb *strings.Builder, gatewayIPs []netip.Addr) {
	sb.WriteString(
		"# Block Docker gateway addresses — opt in with --allow-docker-gateway\n" +
			"acl docker_gateway_hosts dstdomain " +
			strings.Join(HostGatewayNames[:], " ") + "\n",
	)
	if len(gatewayIPs) > 0 {
		sb.WriteString("acl docker_gateway_ip dst " + gatewayAddrList(gatewayIPs) + "\n")
	}
	sb.WriteString("http_access deny docker_gateway_hosts\n")
	if len(gatewayIPs) > 0 {
		sb.WriteString("http_access deny docker_gateway_ip\n")
	}
	sb.WriteString("\n")
}

func writeGatewayAllowRules(sb *strings.Builder, gatewayIPs []netip.Addr) {
	sb.WriteString(
		"# Grant Docker gateway access — enabled via --allow-docker-gateway\n" +
			"acl docker_gateway_hosts dstdomain " +
			strings.Join(HostGatewayNames[:], " ") + "\n",
	)
	if len(gatewayIPs) > 0 {
		sb.WriteString("acl docker_gateway_ip dst " + gatewayAddrList(gatewayIPs) + "\n")
	}
	sb.WriteString("http_access allow docker_gateway_hosts\n")
	if len(gatewayIPs) > 0 {
		sb.WriteString("http_access allow docker_gateway_ip\n")
	}
	sb.WriteString("\n")
}

func gatewayAddrList(gateways []netip.Addr) string {
	parts := make([]string, len(gateways))
	for i, gateway := range gateways {
		parts[i] = gateway.String()
	}
	return strings.Join(parts, " ")
}

func writeCommonConfig(sb *strings.Builder, hostnameBase string, direction proxyDirection) {
	var serverHostname string

	if direction == proxyEgress {
		serverHostname = hostnameBase + "-egress"
		sb.WriteString("http_port 3128\n")
	} else {
		serverHostname = hostnameBase + "-ingress"
	}

	sb.WriteString(
		"visible_hostname " + serverHostname + "\n" +
			"access_log stdio:/dev/stdout squid\n" +
			"pid_filename none\n" +
			"# Avoid allocation errors caused by max_filedescriptors inference\n" +
			"max_filedescriptors 1024\n" +
			"# Disable memory and disk caching\n" +
			"cache deny all\n" +
			"cache_mem 0 MB\n" +
			"maximum_object_size 0 KB\n" +
			"maximum_object_size_in_memory 0 KB\n" +
			"# Don't use cache directories\n" +
			"cache_store_log none\n\n")
}

func writeOutboundACLs(sb *strings.Builder, outbound *permissions.OutboundNetworkPermissions) {
	if outbound == nil {
		return
	}
	if len(outbound.AllowPort) > 0 {
		sb.WriteString("# Define allowed ports\nacl allowed_ports port")
		for _, port := range outbound.AllowPort {
			sb.WriteString(" " + strconv.Itoa(port))
		}
		sb.WriteString("\n")
	}

	if len(outbound.AllowHost) > 0 {
		sb.WriteString("# Define allowed destinations\nacl allowed_dsts dstdomain")
		for _, host := range outbound.AllowHost {
			sb.WriteString(" " + host)
		}
		sb.WriteString("\n")
	}
}

func writeHTTPAccessRules(sb *strings.Builder, outbound *permissions.OutboundNetworkPermissions) {
	if outbound == nil {
		return
	}
	var conditions []string
	if len(outbound.AllowPort) > 0 {
		conditions = append(conditions, "allowed_ports")
	}
	if len(outbound.AllowHost) > 0 {
		conditions = append(conditions, "allowed_dsts")
	}
	if len(conditions) > 0 {
		sb.WriteString("\n# Define http_access rules\n")
		sb.WriteString("http_access allow " + strings.Join(conditions, " ") + "\n")
	}
}

func writeIngressProxyConfig(
	sb *strings.Builder,
	serverHostname string,
	upstreamHost string,
	upstreamPort int,
	squidPort int,
	networkPermissions *permissions.NetworkPermissions,
) {
	portNum := strconv.Itoa(upstreamPort)
	squidPortNum := strconv.Itoa(squidPort)
	sb.WriteString(
		"\n# Reverse proxy setup for port " + portNum + "\n" +
			"http_port 0.0.0.0:" + squidPortNum + " accel defaultsite=" + serverHostname + "\n" +
			"cache_peer " + upstreamHost + " parent " + portNum + " 0 no-query originserver name=origin_" +
			portNum + " connect-timeout=5 connect-fail-limit=5 standby=2\n")

	if networkPermissions != nil && networkPermissions.Inbound != nil && len(networkPermissions.Inbound.AllowHost) > 0 {
		sb.WriteString("acl allowed_hosts dstdomain")
		for _, host := range networkPermissions.Inbound.AllowHost {
			sb.WriteString(" " + host)
		}
		sb.WriteString("\n")
		sb.WriteString("http_access allow allowed_hosts\n")
	} else {
		sb.WriteString("acl site_" + portNum + " dstdomain " + serverHostname + "\n" +
			"acl local_dst dst 127.0.0.1\n" +
			"acl local_domain dstdomain localhost\n" +
			"http_access allow site_" + portNum + "\n" +
			"http_access allow local_dst\n" +
			"http_access allow local_domain\n")
	}
}

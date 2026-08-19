// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	thclient "github.com/stacklok/toolhive/pkg/mcp/client"
)

func newMCPCheckCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check MCP initialize readiness",
		Long: `Connect to an MCP server, perform only the initialize handshake, and close the connection.

The command does not list or invoke tools, resources, or prompts, making it
suitable for readiness gates and CI preflight checks. It exits non-zero when
the transport or initialize handshake fails.`,
		Args: cobra.NoArgs,
		RunE: mcpCheckCmdFunc,
	}
	cmd.Flags().StringVar(&mcpServerURL, "server", "", "MCP server URL or name from ToolHive registry (required)")
	cmd.Flags().StringVar(&mcpTransport, "transport", thclient.TransportAuto, "Transport type (auto, sse, streamable-http)")
	cmd.Flags().DurationVar(&mcpTimeout, "timeout", 5*time.Second, "Connection and initialize timeout")
	AddFormatFlag(cmd, &mcpFormat)
	_ = cmd.MarkFlagRequired("server")
	cmd.PreRunE = ValidateFormat(&mcpFormat)
	return cmd
}

func mcpCheckCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), mcpTimeout)
	defer cancel()

	serverURL, err := resolveServerURL(ctx, mcpServerURL)
	if err != nil {
		return err
	}

	started := time.Now()
	result, err := thclient.Probe(ctx, serverURL, mcpTransport, "toolhive-cli")
	if err != nil {
		return fmt.Errorf("MCP readiness check failed: %w", err)
	}

	output := struct {
		Status          string `json:"status"`
		LatencyMS       int64  `json:"latency_ms"`
		Transport       string `json:"transport"`
		ProtocolVersion string `json:"protocol_version"`
		ServerInfo      any    `json:"server_info"`
		Capabilities    any    `json:"capabilities"`
	}{
		Status: "ready", LatencyMS: time.Since(started).Milliseconds(),
		Transport: result.Transport, ProtocolVersion: result.ProtocolVersion,
		ServerInfo: result.ServerInfo, Capabilities: result.Capabilities,
	}

	if mcpFormat == FormatJSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal readiness result: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("Status:\t%s\nTransport:\t%s\nProtocol:\t%s\nLatency:\t%dms\nServer:\t%s %s\n",
		output.Status, output.Transport, output.ProtocolVersion, output.LatencyMS,
		result.ServerInfo.Name, result.ServerInfo.Version)
	return nil
}

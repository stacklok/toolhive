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
	var serverURL string
	var transport string
	var timeout time.Duration
	var format string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check MCP initialize readiness",
		Long: `Connect to an MCP server, perform only the initialize handshake, and close the connection.

The command does not list or invoke tools, resources, or prompts, making it
suitable for readiness gates and CI preflight checks. It exits non-zero when
the transport or initialize handshake fails.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcpCheckCmdFunc(cmd, serverURL, transport, timeout, format, args)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "MCP server URL or name from ToolHive registry (required)")
	cmd.Flags().StringVar(&transport, "transport", thclient.TransportAuto, "Transport type (auto, sse, streamable-http)")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "Connection and initialize timeout")
	AddFormatFlag(cmd, &format)
	_ = cmd.MarkFlagRequired("server")
	cmd.PreRunE = ValidateFormat(&format)
	return cmd
}

func mcpCheckCmdFunc(cmd *cobra.Command, serverURL, transport string, timeout time.Duration, format string, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	serverURL, err := resolveServerURL(ctx, serverURL)
	if err != nil {
		return err
	}

	started := time.Now()
	result, err := thclient.Probe(ctx, serverURL, transport, "toolhive-cli")
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

	if format == FormatJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			return fmt.Errorf("failed to write readiness result: %w", err)
		}
		return nil
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(),
		"Status:\t%s\nTransport:\t%s\nProtocol:\t%s\nLatency:\t%dms\nServer:\t%s %s\n",
		output.Status, output.Transport, output.ProtocolVersion, output.LatencyMS,
		result.ServerInfo.Name, result.ServerInfo.Version); err != nil {
		return fmt.Errorf("failed to write readiness result: %w", err)
	}
	return nil
}

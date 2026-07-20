// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	thclient "github.com/stacklok/toolhive/pkg/mcp/client"
)

var (
	mcpCallArgs            string
	mcpCallArgsFile        string
	mcpCallIgnoreToolError bool
)

func newMCPCallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "call <tool-name>",
		Short: "Invoke a tool on an MCP server",
		Long: `Invoke a tool on an MCP server. The server is connected, initialized,
the tool is called with the supplied arguments, and the result is printed.

Arguments are supplied as a JSON object via --args or --args-file. If neither
flag is set, the tool is called with an empty argument object.

By default, the command exits with a non-zero status when the tool reports an
error (CallToolResult.IsError=true). Use --ignore-tool-error to exit zero in
that case; transport and protocol failures always exit non-zero.`,
		Args: cobra.ExactArgs(1),
		RunE: mcpCallCmdFunc,
	}

	cmd.Flags().StringVar(&mcpServerURL, "server", "",
		"MCP server URL or name from ToolHive registry (required)")
	AddFormatFlag(cmd, &mcpFormat)
	cmd.Flags().DurationVar(&mcpTimeout, "timeout", 30*time.Second, "Connection timeout")
	cmd.Flags().StringVar(&mcpTransport, "transport", "auto", "Transport type (auto, sse, streamable-http)")
	cmd.Flags().StringVar(&mcpCallArgs, "args", "", "Tool arguments as a JSON object literal")
	cmd.Flags().StringVar(&mcpCallArgsFile, "args-file", "",
		"Path to a file containing a JSON object of tool arguments (use '-' to read from stdin)")
	cmd.Flags().BoolVar(&mcpCallIgnoreToolError, "ignore-tool-error", false,
		"Exit zero even when the tool reports an error (default is non-zero)")
	cmd.MarkFlagsMutuallyExclusive("args", "args-file")

	_ = cmd.MarkFlagRequired("server")
	cmd.PreRunE = ValidateFormat(&mcpFormat)

	return cmd
}

func mcpCallCmdFunc(cmd *cobra.Command, posArgs []string) error {
	toolName := posArgs[0]

	args, err := readToolArgs(mcpCallArgs, mcpCallArgsFile, cmd.InOrStdin())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), mcpTimeout)
	defer cancel()

	serverURL, err := resolveServerURL(ctx, mcpServerURL)
	if err != nil {
		return err
	}

	result, err := thclient.CallTool(ctx, serverURL, mcpTransport, "toolhive-cli", toolName, args)
	if err != nil {
		return err
	}

	if err := renderCallResult(result, mcpFormat); err != nil {
		return err
	}

	if result.IsError && !mcpCallIgnoreToolError {
		// SilenceUsage so the cobra help dump doesn't follow a tool-level error;
		// the result has already been rendered above.
		cmd.SilenceUsage = true
		return fmt.Errorf("tool %q reported an error", toolName)
	}
	return nil
}

// readToolArgs returns the parsed JSON object of tool arguments. An empty
// argString and empty argFile yields nil (no arguments).
func readToolArgs(argString, argFile string, stdin io.Reader) (map[string]any, error) {
	var raw []byte
	switch {
	case argString != "":
		raw = []byte(argString)
	case argFile == "-":
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("failed to read args from stdin: %w", err)
		}
		raw = b
	case argFile != "":
		// #nosec G304 -- argFile is a user-supplied path passed via --args-file.
		b, err := os.ReadFile(argFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read args file: %w", err)
		}
		raw = b
	default:
		return nil, nil
	}

	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse tool arguments as JSON: %w", err)
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool arguments must be a JSON object, got %T", parsed)
	}
	return obj, nil
}

func renderCallResult(result *mcp.CallToolResult, format string) error {
	if format == FormatJSON {
		out, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal result: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}
	return renderCallResultText(result)
}

func renderCallResultText(result *mcp.CallToolResult) error {
	if result.IsError {
		_, _ = fmt.Fprintln(os.Stderr, "Error:")
	}
	for _, content := range result.Content {
		fmt.Println(formatContent(content))
	}
	if result.StructuredContent != nil {
		b, err := json.MarshalIndent(result.StructuredContent, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal structured content: %w", err)
		}
		fmt.Println("Structured content:")
		fmt.Println(string(b))
	}
	return nil
}

// formatContent renders a single Content item for text output. Non-text
// payloads are stubbed (e.g. binary data is shown as a size summary rather
// than dumped to the terminal).
func formatContent(content mcp.Content) string {
	switch c := content.(type) {
	case mcp.TextContent:
		return c.Text
	case mcp.ImageContent:
		return formatBinaryContent("image", c.MIMEType, c.Data)
	case mcp.AudioContent:
		return formatBinaryContent("audio", c.MIMEType, c.Data)
	case mcp.ResourceLink:
		return formatResourceLink(c)
	case mcp.EmbeddedResource:
		return "[embedded resource]"
	default:
		return fmt.Sprintf("[unknown content type %T]", content)
	}
}

func formatResourceLink(c mcp.ResourceLink) string {
	mimeType := c.MIMEType
	if mimeType == "" {
		mimeType = "unknown"
	}
	name := c.Name
	if name == "" {
		name = c.URI
	}
	return fmt.Sprintf("[resource link: %s (%s, %s)]", name, c.URI, mimeType)
}

func formatBinaryContent(kind, mimeType, b64data string) string {
	// Report decoded byte length when possible; fall back to encoded length.
	size := len(b64data)
	if decoded, err := base64.StdEncoding.DecodeString(b64data); err == nil {
		size = len(decoded)
	}
	if mimeType == "" {
		mimeType = "unknown"
	}
	return fmt.Sprintf("[%s: %s, %d bytes]", kind, mimeType, size)
}

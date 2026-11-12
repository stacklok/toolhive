// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestMCPCheckKeepsFiveSecondDefault(t *testing.T) {
	t.Parallel()

	mcpCmd := newMCPCommand()
	checkCmd, _, err := mcpCmd.Find([]string{"check"})
	require.NoError(t, err)

	timeout, err := checkCmd.Flags().GetDuration("timeout")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, timeout)
}

func TestMCPCheckJSONUsesCommandWriter(t *testing.T) {
	t.Parallel()

	mcpServer := server.NewMCPServer("check-test", "1.2.3")
	streamableServer := server.NewStreamableHTTPServer(mcpServer, server.WithEndpointPath("/mcp"))
	testServer := httptest.NewServer(streamableServer)
	t.Cleanup(testServer.Close)

	cmd := newMCPCheckCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{
		"--server", testServer.URL + "/mcp",
		"--transport", string(types.TransportTypeStreamableHTTP),
		"--format", FormatJSON,
	})
	require.NoError(t, cmd.Execute())

	var result map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	assert.Equal(t, "ready", result["status"])
	assert.Equal(t, string(types.TransportTypeStreamableHTTP), result["transport"])
}

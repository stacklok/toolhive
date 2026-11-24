// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/mcp/instructions"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestHTTPSSEProxy_InstructionsOnTheEventStream verifies the legacy SSE
// transport end to end: the initialize request is POSTed to the messages
// endpoint while its result arrives on the event stream a GET opened, and the
// instructions middleware sets the instructions of that result there.
func TestHTTPSSEProxy_InstructionsOnTheEventStream(t *testing.T) {
	t.Parallel()

	middleware, err := mcp.NewInstructionsMiddleware(instructions.Config{Mode: instructions.ModePrepend, Value: "policy"})
	require.NoError(t, err)
	proxy := NewHTTPSSEProxy("localhost", 0, false, nil, []types.NamedMiddleware{
		{Name: mcp.ParserMiddlewareType, Function: mcp.ParsingMiddleware},
		{Name: mcp.InstructionsMiddlewareType, Function: middleware},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})

	// Fake backend: answer initialize with a result that carries instructions.
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || req.Method != "initialize" {
					continue
				}
				resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "s", "version": "1"},
					"instructions":    "server text",
				}, nil)
				_ = proxy.ForwardResponseToClients(ctx, resp)
			case <-ctx.Done():
				return
			}
		}
	}()

	baseURL := fmt.Sprintf("http://%s", proxy.server.Addr)
	var stream *http.Response
	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/sse", nil)
		if err != nil {
			return false
		}
		stream, err = http.DefaultClient.Do(req)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "server did not become ready")
	t.Cleanup(func() { _ = stream.Body.Close() })
	events := bufio.NewReader(stream.Body)

	endpoint := readSSEData(t, events)
	require.Contains(t, endpoint, "session_id=")

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+endpoint[strings.Index(endpoint, "/messages"):], bytes.NewReader([]byte(initialize)))
	require.NoError(t, err)
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(postReq)
	require.NoError(t, err)
	_ = postResp.Body.Close()
	require.Less(t, postResp.StatusCode, http.StatusMultipleChoices)

	var message struct {
		Result map[string]any `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(readSSEData(t, events)), &message))
	assert.Equal(t, "policy\n\nserver text", message.Result["instructions"])
}

// readSSEData returns the data of the next event on the stream.
func readSSEData(t *testing.T, events *bufio.Reader) string {
	t.Helper()
	var data []string
	for {
		line, err := events.ReadString('\n')
		require.NoError(t, err)
		line = strings.TrimRight(line, "\r\n")
		if line == "" && len(data) > 0 {
			return strings.Join(data, "\n")
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
}

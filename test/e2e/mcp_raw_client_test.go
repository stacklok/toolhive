// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// wireOf marshals req and decodes it into a generic map for field assertions.
func wireOf(t *testing.T, req *RawRequest) map[string]any {
	t.Helper()
	b, err := req.marshal()
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func TestRawClientBuilders(t *testing.T) {
	t.Parallel()

	t.Run("ModernRequest sets header and default meta keys, clientInfo omitted", func(t *testing.T) {
		t.Parallel()
		req, err := NewModernRequest("tools/list", nil)
		require.NoError(t, err)

		require.Equal(t, MCPVersionModern, req.headers[HeaderMCPProtocolVersion])

		wire := wireOf(t, req)
		meta := wire["params"].(map[string]any)["_meta"].(map[string]any)
		require.Equal(t, MCPVersionModern, meta[MetaKeyProtocolVersion])
		require.Equal(t, map[string]any{}, meta[MetaKeyClientCapabilities])
		require.NotContains(t, meta, MetaKeyClientInfo)
	})

	t.Run("ModernRequest fields are independently overridable", func(t *testing.T) {
		t.Parallel()
		req, err := NewModernRequest("tools/list", nil)
		require.NoError(t, err)

		req.SetMeta(MetaKeyProtocolVersion, "bogus-version").
			SetMeta(MetaKeyClientCapabilities, "not-an-object"). // non-object value, to trigger error paths
			WithClientInfo("probe", "1.0").
			DeleteHeader(HeaderMCPProtocolVersion)

		_, hasHeader := req.headers[HeaderMCPProtocolVersion]
		require.False(t, hasHeader, "header should be omittable")

		wire := wireOf(t, req)
		meta := wire["params"].(map[string]any)["_meta"].(map[string]any)
		require.Equal(t, "bogus-version", meta[MetaKeyProtocolVersion])
		require.Equal(t, "not-an-object", meta[MetaKeyClientCapabilities])
		require.Equal(t, map[string]any{"name": "probe", "version": "1.0"}, meta[MetaKeyClientInfo])

		req.DeleteMeta(MetaKeyClientInfo)
		wire = wireOf(t, req)
		meta = wire["params"].(map[string]any)["_meta"].(map[string]any)
		require.NotContains(t, meta, MetaKeyClientInfo)
	})

	t.Run("LegacyInitializeRequest carries no _meta and the legacy protocol version", func(t *testing.T) {
		t.Parallel()
		req := NewLegacyInitializeRequest("probe", "1.0")
		wire := wireOf(t, req)

		require.Equal(t, "initialize", wire["method"])
		params := wire["params"].(map[string]any)
		require.Equal(t, "2025-11-25", params["protocolVersion"])
		require.NotContains(t, params, "_meta")
	})

	t.Run("LegacyRequest WithSessionID sets the session header, including a spoofed one", func(t *testing.T) {
		t.Parallel()
		req, err := NewLegacyRequest("tools/call", map[string]any{"name": "echo"})
		require.NoError(t, err)
		req.WithSessionID("not-my-session")

		require.Equal(t, "not-my-session", req.headers[HeaderMCPSessionID])
	})

	t.Run("NewLegacyRequest rejects an empty method", func(t *testing.T) {
		t.Parallel()
		_, err := NewLegacyRequest("", nil)
		require.Error(t, err)
	})

	t.Run("WithID preserves a large int64 and allows colliding ids", func(t *testing.T) {
		t.Parallel()
		const big = int64(9223372036854775807) // math.MaxInt64; beyond float64's exact integer range

		reqA, err := NewLegacyRequest("ping", nil)
		require.NoError(t, err)
		reqA.WithID(big)
		reqB, err := NewLegacyRequest("ping", nil)
		require.NoError(t, err)
		reqB.WithID(big) // deliberate id collision with reqA

		for _, req := range []*RawRequest{reqA, reqB} {
			b, err := req.marshal()
			require.NoError(t, err)
			require.Contains(t, string(b), `"id":9223372036854775807`)
		}
	})

	t.Run("WithRawBody sends the body verbatim, ignoring method and params", func(t *testing.T) {
		t.Parallel()
		req, err := NewLegacyRequest("tools/list", nil)
		require.NoError(t, err)
		garbage := []byte(`{not even json`)
		req.WithRawBody(garbage)

		b, err := req.marshal()
		require.NoError(t, err)
		require.Equal(t, garbage, b)

		// Mutating the caller's slice afterward must not affect the request.
		garbage[0] = 'X'
		b, err = req.marshal()
		require.NoError(t, err)
		require.Equal(t, []byte(`{not even json`), b)
	})

	t.Run("NewBatchBody produces a JSON array of the wire bodies in order", func(t *testing.T) {
		t.Parallel()
		reqA, err := NewLegacyRequest("ping", nil)
		require.NoError(t, err)
		reqA.WithID(int64(1))
		reqB, err := NewLegacyRequest("tools/list", nil)
		require.NoError(t, err)
		reqB.WithID(int64(2))

		batch, err := NewBatchBody(reqA, reqB)
		require.NoError(t, err)

		var arr []map[string]any
		require.NoError(t, json.Unmarshal(batch, &arr))
		require.Len(t, arr, 2)
		require.Equal(t, "ping", arr[0]["method"])
		require.InDelta(t, float64(1), arr[0]["id"], 0)
		require.Equal(t, "tools/list", arr[1]["method"])
		require.InDelta(t, float64(2), arr[1]["id"], 0)
	})

	t.Run("params map passed by the caller is not mutated", func(t *testing.T) {
		t.Parallel()
		callerParams := map[string]any{"name": "echo"}
		req, err := NewModernRequest("tools/call", callerParams)
		require.NoError(t, err)
		req.SetMeta("extra", "value")
		_, err = req.marshal()
		require.NoError(t, err)

		require.Equal(t, map[string]any{"name": "echo"}, callerParams, "caller's map must be untouched")
	})
}

// capturedRequest snapshots what the sender actually transmitted.
type capturedRequest struct {
	headers http.Header
	body    []byte
}

// newCapturingServer starts an httptest.Server that records the last request
// it received (via the returned *capturedRequest pointer, refreshed on every
// call) and replies with responseBody.
func newCapturingServer(t *testing.T, responseBody string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		captured.headers = r.Header.Clone()
		captured.body = body

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(HeaderMCPSessionID, "server-assigned-session")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func TestRawClientSend(t *testing.T) {
	t.Parallel()

	client, err := NewRawMCPClient(5 * time.Second)
	require.NoError(t, err)

	t.Run("transmits headers and body, and parses result/id/response headers", func(t *testing.T) {
		t.Parallel()
		server, captured := newCapturingServer(t, `{"jsonrpc":"2.0","id":42,"result":{"ok":true}}`)

		req := NewLegacyInitializeRequest("probe", "1.0").WithSessionID("client-picked-session")
		req.WithID(int64(42))

		resp, err := client.Send(context.Background(), server.URL, req)
		require.NoError(t, err)

		require.Equal(t, "client-picked-session", captured.headers.Get(HeaderMCPSessionID),
			"the client-set session header must reach the server verbatim")

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "server-assigned-session", resp.Headers.Get(HeaderMCPSessionID),
			"response headers must be surfaced so tests can assert session-id echoing")
		require.Equal(t, json.Number("42"), resp.ID)
		require.JSONEq(t, `{"ok":true}`, string(resp.Result))
		require.Nil(t, resp.Error)
	})

	t.Run("parses error.code, error.data and a large-int64 id without precision loss", func(t *testing.T) {
		t.Parallel()
		const bigID = "9223372036854775807"
		server, _ := newCapturingServer(t,
			`{"jsonrpc":"2.0","id":`+bigID+`,"error":{"code":-32020,"message":"mismatch","data":{"header":"2025-11-25"}}}`)

		req, err := NewModernRequest("tools/list", nil)
		require.NoError(t, err)
		resp, err := client.Send(context.Background(), server.URL, req)
		require.NoError(t, err)

		require.Equal(t, json.Number(bigID), resp.ID)
		id, err := resp.ID.(json.Number).Int64()
		require.NoError(t, err)
		require.Equal(t, int64(math.MaxInt64), id)

		require.NotNil(t, resp.Error)
		require.Equal(t, int64(-32020), resp.Error.Code)
		require.Equal(t, "mismatch", resp.Error.Message)
		require.JSONEq(t, `{"header":"2025-11-25"}`, string(resp.Error.Data))
	})

	t.Run("SendRaw transmits a batch body and leaves envelope fields zero on a non-object response", func(t *testing.T) {
		t.Parallel()
		server, captured := newCapturingServer(t, `[{"jsonrpc":"2.0","id":1,"result":{}},{"jsonrpc":"2.0","id":2,"result":{}}]`)

		reqA, err := NewLegacyRequest("ping", nil)
		require.NoError(t, err)
		reqA.WithID(int64(1))
		reqB, err := NewLegacyRequest("tools/list", nil)
		require.NoError(t, err)
		reqB.WithID(int64(2))
		batch, err := NewBatchBody(reqA, reqB)
		require.NoError(t, err)

		resp, err := client.SendRaw(context.Background(), server.URL, nil, batch)
		require.NoError(t, err)

		var sentArr []map[string]any
		require.NoError(t, json.Unmarshal(captured.body, &sentArr))
		require.Len(t, sentArr, 2, "the server must receive a two-element JSON array")

		require.Nil(t, resp.ID, "a batch (array) response is not a single envelope")
		require.Nil(t, resp.Error)
		require.JSONEq(t, `[{"jsonrpc":"2.0","id":1,"result":{}},{"jsonrpc":"2.0","id":2,"result":{}}]`, string(resp.Body))
	})

	t.Run("garbage body is sent verbatim and a non-JSON response leaves the envelope zero but Body populated", func(t *testing.T) {
		t.Parallel()
		server, captured := newCapturingServer(t, `not json at all`)

		resp, err := client.SendRaw(context.Background(), server.URL, map[string]string{"X-Test": "1"}, []byte(`{truncated`))
		require.NoError(t, err)

		require.Equal(t, []byte(`{truncated`), captured.body)
		require.Equal(t, "1", captured.headers.Get("X-Test"))

		require.Nil(t, resp.ID)
		require.Nil(t, resp.Error)
		require.Equal(t, []byte(`not json at all`), resp.Body)
	})
}

func TestNewRawMCPClientRejectsNonPositiveTimeout(t *testing.T) {
	t.Parallel()
	_, err := NewRawMCPClient(0)
	require.Error(t, err)
	_, err = NewRawMCPClient(-time.Second)
	require.Error(t, err)
}

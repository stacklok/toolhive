// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassificationError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		requestID any
		err       error
		wantCode  int64
		wantData  bool
	}{
		{
			name:      "header mismatch",
			requestID: "req-1",
			err:       &HeaderMismatchError{Header: "2026-07-28", Body: "2025-11-25"},
			wantCode:  CodeHeaderMismatch,
			wantData:  true,
		},
		{
			name:      "unsupported version",
			requestID: float64(42),
			err:       &UnsupportedVersionError{Requested: "1999-01-01", Supported: []string{MCPVersionModern}},
			wantCode:  CodeUnsupportedProtocolVersion,
			wantData:  true,
		},
		{
			name:      "missing client capability",
			requestID: "req-3",
			err:       &MissingClientCapabilityError{RequiredCapabilities: map[string]any{}},
			wantCode:  CodeMissingClientCapability,
			wantData:  true,
		},
		{
			name:      "missing modern metadata",
			requestID: "req-4",
			err:       &MissingModernMetadataError{},
			wantCode:  CodeInvalidParams,
			wantData:  false, // Data() returns an empty (non-nil) map, so len(data) == 0
		},
		{
			name:      "nil request id",
			requestID: nil,
			err:       &HeaderMismatchError{Header: "a", Body: "b"},
			wantCode:  CodeHeaderMismatch,
			wantData:  true,
		},
		{
			name:      "plain non-coded error falls back to invalid params",
			requestID: "req-6",
			err:       errors.New("boom"),
			wantCode:  CodeInvalidParams,
			wantData:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			WriteClassificationError(rec, tt.requestID, tt.err)

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			resp := ClassificationErrorResponse(req, tt.requestID, tt.err)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("WriteClassificationError: status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("ClassificationErrorResponse: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("WriteClassificationError: Content-Type = %q, want application/json", ct)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("ClassificationErrorResponse: Content-Type = %q, want application/json", ct)
			}
			if resp.Request != req {
				t.Fatalf("ClassificationErrorResponse: Request not attached")
			}
			if resp.Body == nil {
				t.Fatalf("ClassificationErrorResponse: Body is nil")
			}

			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading response body: %v", err)
			}
			if resp.ContentLength != int64(len(respBody)) {
				t.Fatalf("ContentLength = %d, want %d", resp.ContentLength, len(respBody))
			}

			wireBody := rec.Body.Bytes()
			if string(wireBody) != string(respBody) {
				t.Fatalf("WriteClassificationError and ClassificationErrorResponse bodies differ:\n%s\nvs\n%s", wireBody, respBody)
			}

			var decoded struct {
				JSONRPC string `json:"jsonrpc"`
				Error   struct {
					Code    int64          `json:"code"`
					Message string         `json:"message"`
					Data    map[string]any `json:"data"`
				} `json:"error"`
				ID any `json:"id"`
			}
			if err := json.Unmarshal(wireBody, &decoded); err != nil {
				t.Fatalf("unmarshaling response body: %v", err)
			}

			if decoded.JSONRPC != "2.0" {
				t.Errorf("jsonrpc = %q, want \"2.0\"", decoded.JSONRPC)
			}
			if decoded.Error.Code != tt.wantCode {
				t.Errorf("code = %d, want %d", decoded.Error.Code, tt.wantCode)
			}
			if decoded.Error.Message != tt.err.Error() {
				t.Errorf("message = %q, want %q", decoded.Error.Message, tt.err.Error())
			}
			if tt.wantData && len(decoded.Error.Data) == 0 {
				t.Errorf("expected non-empty data, got %v", decoded.Error.Data)
			}
			if !tt.wantData && len(decoded.Error.Data) != 0 {
				t.Errorf("expected no data, got %v", decoded.Error.Data)
			}

			gotID, wantID := decoded.ID, tt.requestID
			if wantID == nil {
				if gotID != nil {
					t.Errorf("id = %v, want nil", gotID)
				}
			} else if gotID != wantID {
				t.Errorf("id = %v (%T), want %v (%T)", gotID, gotID, wantID, wantID)
			}
		})
	}
}

// TestClassificationErrorBodyMarshalFallback verifies the defensive fallback:
// if the response fails to marshal (unreachable in production, where requestID
// is always a json.RawMessage), classificationErrorBody still returns a valid
// JSON-RPC error rather than an empty body. A channel is not JSON-marshalable,
// so it forces json.Marshal to fail.
func TestClassificationErrorBodyMarshalFallback(t *testing.T) {
	t.Parallel()

	const want = `{"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params"},"id":null}`
	got := classificationErrorBody(make(chan int), errors.New("boom"))
	if string(got) != want {
		t.Fatalf("fallback body = %s, want %s", got, want)
	}
}

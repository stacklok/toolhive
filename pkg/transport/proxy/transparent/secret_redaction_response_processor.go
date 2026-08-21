// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/stacklok/toolhive/pkg/mcp/secretscan"
)

// maxSecretScanBodyBytes bounds how much of a non-streaming response body
// this processor buffers to scan. A response larger than this is forwarded
// unscanned rather than risking unbounded memory growth on a hostile or
// oversized upstream body.
const maxSecretScanBodyBytes = 8 << 20 // 8 MB, matches bodylimit.DefaultMaxRequestBodySize

// SecretRedactionResponseProcessor scans MCP responses emitted by the
// (untrusted) backend this proxy fronts for credential-shaped content in a
// tools/call result (see pkg/mcp/secretscan) and redacts matches before the
// response reaches the client. Opt-in; see WithSecretRedaction. Constructed
// only when enabled, so unlike other processors it carries no enabled flag
// of its own.
//
// Handles the two response shapes a streamable-HTTP MCP server may emit for
// a single request: a plain application/json body, or a text/event-stream
// response carrying one or more "data:" JSON-RPC frames (MCP allows a server
// to reply to a POST with either shape).
type SecretRedactionResponseProcessor struct{}

// NewSecretRedactionResponseProcessor creates a new secret-redaction response
// processor.
func NewSecretRedactionResponseProcessor() *SecretRedactionResponseProcessor {
	return &SecretRedactionResponseProcessor{}
}

// ShouldProcess returns true for JSON and SSE response bodies -- the two
// shapes a streamable-HTTP MCP response can take.
func (*SecretRedactionResponseProcessor) ShouldProcess(resp *http.Response) bool {
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mediaType == "application/json" || mediaType == "text/event-stream"
}

// ProcessResponse redacts credential-shaped content from the response body.
func (p *SecretRedactionResponseProcessor) ProcessResponse(resp *http.Response) error {
	if !p.ShouldProcess(resp) {
		return nil
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		p.processSSE(resp)
		return nil
	}
	return p.processJSON(resp)
}

// chainedBody re-splices a size-limited read prefix back onto the reader it
// came from, so a body too large to safely buffer is still forwarded intact
// (unscanned) rather than truncated. Close defers to the original body so
// the underlying connection is still released once the client finishes
// reading.
type chainedBody struct {
	io.Reader
	closer io.Closer
}

func (c chainedBody) Close() error { return c.closer.Close() }

// processJSON scans a complete (non-streaming) JSON response body.
func (*SecretRedactionResponseProcessor) processJSON(resp *http.Response) error {
	original := resp.Body
	limited := io.LimitReader(original, maxSecretScanBodyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("reading response body for secret scan: %w", err)
	}
	if int64(len(data)) > maxSecretScanBodyBytes {
		slog.Debug("response body exceeds secret-scan size limit; forwarding unscanned",
			"limit_bytes", maxSecretScanBodyBytes)
		resp.Body = chainedBody{Reader: io.MultiReader(bytes.NewReader(data), original), closer: original}
		return nil
	}
	if err := original.Close(); err != nil {
		slog.Debug("failed to close upstream response body", "error", err)
	}

	redacted, changed, scanErr := redactJSONRPCBody(data)
	if scanErr != nil || !changed {
		// Not a recognizable JSON-RPC envelope, or nothing matched -- forward
		// the original bytes unchanged (fail open; a scan miss is not a
		// proxy error).
		resp.Body = io.NopCloser(bytes.NewReader(data))
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(redacted))
	resp.ContentLength = int64(len(redacted))
	resp.Header.Set("Content-Length", strconv.Itoa(len(redacted)))
	return nil
}

// processSSE streams an SSE response line by line, redacting each "data:"
// frame's JSON-RPC payload as it is forwarded, so a long-lived stream is
// never fully buffered.
func (*SecretRedactionResponseProcessor) processSSE(resp *http.Response) {
	original := resp.Body
	pr, pw := io.Pipe()
	resp.Body = pr

	go func() {
		defer func() {
			if err := pw.Close(); err != nil {
				slog.Debug("failed to close pipe writer", "error", err)
			}
		}()
		defer func() {
			if err := original.Close(); err != nil {
				slog.Debug("failed to close upstream response body", "error", err)
			}
		}()

		scanner := bufio.NewScanner(original)
		// Matches sse_response_processor.go's buffer size rationale: the
		// default 64KB token limit is too small for a data line carrying a
		// sizeable tool result.
		scanner.Buffer(make([]byte, 0, 1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				dataContent := strings.TrimSpace(after)
				if redacted, changed, err := redactJSONRPCBody([]byte(dataContent)); err == nil && changed {
					line = "data: " + string(redacted)
				}
			}
			if _, err := pw.Write([]byte(line + "\n")); err != nil {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			slog.Error("failed to scan SSE response body for secret scan", "error", err)
		}
	}()
}

// redactJSONRPCBody decodes data as a JSON object, redacts its "result"
// field via secretscan (if present), and returns the re-encoded body. changed
// is false whenever nothing needed redacting -- including when data doesn't
// decode as a JSON-RPC response object at all (e.g. a request, a batch, or a
// malformed frame), which is reported via a non-nil err so callers can fail
// open without treating it as a real error.
func redactJSONRPCBody(data []byte) (redacted []byte, changed bool, err error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, false, fmt.Errorf("decoding JSON-RPC envelope: %w", err)
	}
	result, ok := envelope["result"]
	if !ok || len(result) == 0 {
		return nil, false, nil
	}
	scan, err := secretscan.ScanAndRedactToolCallResult(result)
	if err != nil {
		return nil, false, err
	}
	if !scan.Matched {
		return nil, false, nil
	}
	envelope["result"] = scan.Redacted
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, false, fmt.Errorf("re-encoding JSON-RPC envelope: %w", err)
	}
	return out, true, nil
}

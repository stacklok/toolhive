// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// WriteClassificationError writes an HTTP 400 response with a JSON-RPC error
// body for an mcp.ClassifyRevision failure. Use this with http.ResponseWriter
// in the streamable HTTP proxy.
func WriteClassificationError(w http.ResponseWriter, requestID any, err error) {
	body := classificationErrorBody(requestID, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	//nolint:gosec // G104: writing a JSON-RPC error response to an HTTP client
	_, _ = w.Write(body)
}

// ClassificationErrorResponse constructs an *http.Response with HTTP 400 and
// a JSON-RPC error body for an mcp.ClassifyRevision failure. Use this in
// httputil.ReverseProxy.ModifyResponse/RoundTrip (transparent proxy) where no
// http.ResponseWriter is available.
//
// req is attached as resp.Request, matching session.NotFoundResponse: a
// RoundTripper-produced *http.Response is generally expected to carry the
// request it answers, and httputil.ReverseProxy relies on that field.
func ClassificationErrorResponse(req *http.Request, requestID any, err error) *http.Response {
	body := classificationErrorBody(requestID, err)
	hdr := make(http.Header)
	hdr.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode:    http.StatusBadRequest,
		Status:        fmt.Sprintf("%d %s", http.StatusBadRequest, http.StatusText(http.StatusBadRequest)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        hdr,
		ContentLength: int64(len(body)),
		Body:          io.NopCloser(bytes.NewReader(body)),
		Request:       req,
	}
}

// classificationErrorBody renders an mcp.ClassifyRevision error as a JSON-RPC
// error body, modeled on session.NotFoundBody: the body is marshaled first
// (with a hand-crafted fallback on marshal failure) so callers only write
// headers/status once a valid body is ready.
//
// It uses the error's Code(), Error() message, and Data() (when non-empty) if
// the error implements CodedError, falling back to the standard JSON-RPC
// Invalid Params code otherwise -- a fallback that is currently unreachable,
// since every error ClassifyRevision returns implements CodedError.
func classificationErrorBody(requestID any, err error) []byte {
	code := CodeInvalidParams
	var coded CodedError
	var data map[string]any
	if errors.As(err, &coded) {
		code = coded.Code()
		data = coded.Data()
	}

	errBody := map[string]any{
		"code":    code,
		"message": err.Error(),
	}
	if len(data) > 0 {
		errBody["data"] = data
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"error":   errBody,
		"id":      requestID,
	}

	body, marshalErr := json.Marshal(resp)
	if marshalErr != nil {
		// This should never happen with simple map types, but return a
		// hand-crafted fallback to guarantee a valid JSON-RPC error.
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32602,"message":"Invalid params"},"id":null}`)
	}
	return body
}

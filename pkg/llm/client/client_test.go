// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"valid", Config{Model: "m", BaseURL: "https://x/v1", APIKey: "k"}, false},
		{"key is optional (proxy/local model)", Config{Model: "m", BaseURL: "https://x/v1"}, false},
		{"missing model", Config{BaseURL: "https://x/v1", APIKey: "k"}, true},
		{"missing base url", Config{Model: "m", APIKey: "k"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestComplete(t *testing.T) {
	t.Parallel()

	var gotPath, gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		gotModel, _ = req["model"].(string)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello world"}}]}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{Model: "gpt-test", BaseURL: srv.URL, APIKey: "secret", HTTPClient: srv.Client()})
	require.NoError(t, err)

	out, err := c.Complete(t.Context(), "system", "user")
	require.NoError(t, err)
	assert.Equal(t, "hello world", out)
	assert.Equal(t, "/chat/completions", gotPath)
	assert.Equal(t, "Bearer secret", gotAuth)
	assert.Equal(t, "gpt-test", gotModel)
}

func TestCompleteNoKeyOmitsAuthHeader(t *testing.T) {
	t.Parallel()

	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{Model: "m", BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Complete(t.Context(), "s", "u")
	require.NoError(t, err)
	assert.False(t, hadAuth, "no Authorization header should be sent without an API key")
}

func TestCompleteErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{Model: "m", BaseURL: srv.URL, APIKey: "k", HTTPClient: srv.Client()})
	require.NoError(t, err)

	_, err = c.Complete(t.Context(), "s", "u")
	require.Error(t, err)
}

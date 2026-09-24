// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientBehavior(t *testing.T) {
	t.Parallel()
	t.Run("caller supplied client decodes responses", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/api/v1beta/clients", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]any{{"name": "cursor", "groups": []string{"default"}}}))
		}))
		t.Cleanup(server.Close)

		api, err := NewClient(server.URL, WithClient(server.Client()))
		require.NoError(t, err)
		clients, err := api.ListClients(context.Background())
		require.NoError(t, err)
		require.Len(t, clients, 1)
		require.Equal(t, []string{"default"}, clients[0].Groups)
	})

	t.Run("generated operation preserves transport errors", func(t *testing.T) {
		t.Parallel()
		transportError := errors.New("transport unavailable")
		api, err := NewClient("https://example.invalid", WithClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportError
		})}))
		require.NoError(t, err)

		clients, err := api.ListClients(context.Background())
		require.Nil(t, clients)
		require.ErrorIs(t, err, transportError)
	})

	t.Run("typed and malformed non-2xx responses", func(t *testing.T) {
		t.Parallel()
		for name, body := range map[string]string{"typed": `"registration rejected"`, "malformed": `not JSON`} {
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, http.MethodPost, r.Method)
					requestBody, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.JSONEq(t, `{"name":"cursor","groups":["default"]}`, string(requestBody))
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, err = w.Write([]byte(body))
					require.NoError(t, err)
				}))
				t.Cleanup(server.Close)

				api, err := NewClient(server.URL, WithClient(server.Client()))
				require.NoError(t, err)
				response, err := api.RegisterClient(context.Background(), &CreateClientRequest{Name: ClientClientAppCursor, Groups: []string{"default"}})
				if name == "typed" {
					require.NoError(t, err)
					_, ok := response.(*RegisterClientBadRequestApplicationJSON)
					require.True(t, ok)
					return
				}
				require.Nil(t, response)
				require.Error(t, err)
			})
		}
	})

	t.Run("path and query values are escaped and non-JSON responses are rejected", func(t *testing.T) {
		t.Parallel()
		client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/api/v1beta/plugins/a/b":
				require.Equal(t, "/api/v1beta/plugins/a%2Fb", request.URL.EscapedPath())
				require.Equal(t, "/tmp/a b", request.URL.Query().Get("project_root"))
				return response(http.StatusNotFound, "application/json", `"missing"`), nil
			case "/health":
				return response(http.StatusNoContent, "text/plain", "healthy"), nil
			default:
				return nil, errors.New("unexpected request")
			}
		})}
		api, err := NewClient("https://example.invalid", WithClient(client))
		require.NoError(t, err)
		_, err = api.GetPlugin(context.Background(), GetPluginParams{Name: "a/b", ProjectRoot: NewOptString("/tmp/a b")})
		require.NoError(t, err)
		health, err := api.GetHealth(context.Background())
		require.Error(t, err)
		require.Empty(t, health)
	})
}

func TestNewClientPolicy(t *testing.T) {
	t.Parallel()
	t.Run("rejects unsafe URLs", func(t *testing.T) {
		t.Parallel()
		for _, serverURL := range []string{"", "example.com", "ftp://example.com", "https:///path", "https://user@example.com", "https://example.com/#fragment"} {
			_, err := NewClient(serverURL)
			require.ErrorIs(t, err, ErrInvalidServerURL, serverURL)
		}

		_, err := NewClient("https://user:super-secret@example.com/%zz")
		require.ErrorIs(t, err, ErrInvalidServerURL)
		require.NotContains(t, err.Error(), "super-secret")
	})

	t.Run("preserves caller transport and applies default deadline", func(t *testing.T) {
		t.Parallel()
		authTransport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			request.Header.Set("Authorization", "Bearer token")
			require.Equal(t, "Bearer token", request.Header.Get("Authorization"))
			_, hasDeadline := request.Context().Deadline()
			require.True(t, hasDeadline)
			return response(http.StatusNoContent, "application/json", `"healthy"`), nil
		})
		api, err := NewClient("https://example.invalid", WithClient(&http.Client{Transport: authTransport}))
		require.NoError(t, err)
		_, ok := api.cfg.Client.(boundedClient)
		require.True(t, ok)
		_, err = api.GetHealth(context.Background())
		require.NoError(t, err)
	})

	t.Run("honors caller timeout", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(100 * time.Millisecond) }))
		t.Cleanup(server.Close)
		api, err := NewClient(server.URL, WithClient(&http.Client{Timeout: 10 * time.Millisecond}))
		require.NoError(t, err)
		_, err = api.GetHealth(context.Background())
		require.Error(t, err)
	})

	t.Run("enforces exact response limit boundary", func(t *testing.T) {
		t.Parallel()
		for name, size := range map[string]int64{"at limit": DefaultMaxResponseBodyBytes, "over limit": DefaultMaxResponseBodyBytes + 1} {
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, err := w.Write([]byte(`{"spec":"` + strings.Repeat("x", int(size)-11) + `"}`))
					require.NoError(t, err)
				}))
				t.Cleanup(server.Close)
				api, err := NewClient(server.URL)
				require.NoError(t, err)
				_, err = api.GetOpenAPISpecification(context.Background())
				if size == DefaultMaxResponseBodyBytes {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrResponseBodyTooLarge)
				}
			})
		}
	})

	t.Run("enforces configured response limit", func(t *testing.T) {
		t.Parallel()
		const maxResponseBodyBytes int64 = 64
		for name, size := range map[string]int64{"at limit": maxResponseBodyBytes, "over limit": maxResponseBodyBytes + 1} {
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, err := w.Write([]byte(`{"spec":"` + strings.Repeat("x", int(size)-11) + `"}`))
					require.NoError(t, err)
				}))
				t.Cleanup(server.Close)
				api, err := NewClient(server.URL, WithMaxResponseBodyBytes(maxResponseBodyBytes))
				require.NoError(t, err)
				_, err = api.GetOpenAPISpecification(context.Background())
				if size == maxResponseBodyBytes {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrResponseBodyTooLarge)
				}
			})
		}
	})

	t.Run("rejects non-positive configured response limit", func(t *testing.T) {
		t.Parallel()
		_, err := NewClient("https://example.invalid", WithMaxResponseBodyBytes(0))
		require.ErrorIs(t, err, ErrInvalidMaxResponseBodyBytes)
	})
}

func TestPublisherProvidedNaming(t *testing.T) {
	t.Parallel()

	metadata := reflect.TypeOf(V0ServerMeta{})
	field, found := metadata.FieldByName("PublisherProvided")
	require.True(t, found)
	require.Equal(t, "io.modelcontextprotocol.registry/publisher-provided", field.Tag.Get("json"))
	_, found = metadata.FieldByName("IoDotModelcontextprotocolDotRegistrySlashPublisherMinusProvided")
	require.False(t, found)
}

func TestGeneratedInvokerMatchesOpenAPIOperations(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile(filepath.Join("..", "openapi.json"))
	require.NoError(t, err)
	var document struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(content, &document))
	require.Len(t, document.Components.Schemas, 197, "update this deliberate contract count when publishing a new schema")
	for name := range document.Components.Schemas {
		require.NotContains(t, name, ".", "schema names are public SDK contract names")
	}

	operations := make(map[string]struct{})
	for _, pathItem := range document.Paths {
		for method, operation := range pathItem {
			if !isHTTPMethod(method) {
				continue
			}
			var metadata struct {
				OperationID string `json:"operationId"`
			}
			require.NoError(t, json.Unmarshal(operation, &metadata))
			require.NotEmpty(t, metadata.OperationID)
			_, duplicate := operations[metadata.OperationID]
			require.False(t, duplicate, "operation IDs are public SDK contract names")
			operations[metadata.OperationID] = struct{}{}
		}
	}
	require.Len(t, operations, 77, "update this deliberate contract count when publishing a new operation")

	registryOperation := document.Paths["/api/v1beta/registry"]["post"]
	var addRegistry struct {
		Description string          `json:"description"`
		RequestBody json.RawMessage `json:"requestBody"`
		Responses   map[string]any  `json:"responses"`
	}
	require.NoError(t, json.Unmarshal(registryOperation, &addRegistry))
	require.Contains(t, addRegistry.Description, "always returns 501")
	require.Empty(t, addRegistry.RequestBody, "the unavailable operation accepts no request body")
	require.Contains(t, addRegistry.Responses, "501")

	invoker := reflect.TypeOf((*Invoker)(nil)).Elem()
	methods := make(map[string]struct{}, invoker.NumMethod())
	for index := range invoker.NumMethod() {
		methods[invoker.Method(index).Name] = struct{}{}
	}
	require.Equal(t, operations, methods, "every OpenAPI operation must have exactly one generated client method")
	addRegistryMethod, found := invoker.MethodByName("AddRegistry")
	require.True(t, found)
	require.Equal(t, 1, addRegistryMethod.Type.NumIn(), "the unavailable operation accepts only context")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func isHTTPMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodPatch, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

// TestConsentNativeBrowserForm exercises the real consent page in Chrome. It is
// optional on builders without Chrome; ordinary HTTP clients cannot check the
// browser's Origin serialization or CSP redirect enforcement.
//
//nolint:paralleltest,tparallel // one headless Chrome is shared by the subtests and closed by this function's deferred cleanup, so they cannot outlive it
func TestConsentNativeBrowserForm(t *testing.T) {
	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(chrome); err != nil {
		var lookupErr error
		chrome, lookupErr = exec.LookPath("google-chrome")
		if lookupErr != nil {
			t.Skip("Chrome not installed")
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "upstream arrived")
	}))
	t.Cleanup(upstream.Close)
	clientRedirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "client arrived: "+r.URL.Query().Get("error"))
	}))
	t.Cleanup(clientRedirect.Close)

	const prefix = "/proxy"
	auth := httptest.NewUnstartedServer(nil)
	t.Cleanup(auth.Close)
	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL)
	cfg.Issuer = "http://" + auth.Listener.Addr().String()
	cfg.AuthorizationEndpointBaseURL = cfg.Issuer + prefix
	server := helpers.NewEmbeddedAuthServer(context.Background(), t, cfg)
	mux := http.NewServeMux()
	mux.Handle(prefix+"/", http.StripPrefix(prefix, server.Handler()))
	auth.Config.Handler = mux
	auth.Start()

	client := helpers.NewOAuthClient(auth.URL + prefix)
	registered, status, err := client.RegisterClient(map[string]interface{}{
		"redirect_uris": []string{clientRedirect.URL + "/callback"},
		"grant_types":   []string{"authorization_code"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status)
	clientID, ok := registered["client_id"].(string)
	require.True(t, ok)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	profile := t.TempDir()
	processCtx, stopProcess := context.WithCancel(context.Background())
	defer stopProcess()
	cmd := exec.CommandContext(processCtx, chrome, "--headless=new", "--no-first-run", "--no-default-browser-check",
		"--disable-background-networking", "--remote-debugging-port=0", "--user-data-dir="+profile, "about:blank")
	require.NoError(t, cmd.Start())
	var browserConn *websocket.Conn
	defer func() {
		// Let Chrome stop its helpers before t.TempDir removes the profile.
		// Fall back to killing only this process if CDP is unavailable or hangs.
		killTimer := time.AfterFunc(5*time.Second, stopProcess)
		defer killTimer.Stop()
		if browserConn == nil {
			stopProcess()
		} else {
			_ = browserConn.SetWriteDeadline(time.Now().Add(time.Second))
			if err := browserConn.WriteJSON(map[string]any{"id": 1, "method": "Browser.close"}); err != nil {
				stopProcess()
			}
			defer browserConn.Close()
		}
		// Reap the process; a forced shutdown can return an expected exit error.
		_ = cmd.Wait()
	}()

	var debugPort, browserPath string
	for ctx.Err() == nil {
		data, readErr := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if readErr == nil {
			debugPort, browserPath, _ = strings.Cut(string(data), "\n")
			browserPath = strings.TrimSpace(browserPath)
			if debugPort != "" && browserPath != "" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotEmpty(t, debugPort, "Chrome debugging port unavailable")
	require.NotEmpty(t, browserPath, "Chrome browser endpoint unavailable")
	var handshake *http.Response
	browserConn, handshake, err = websocket.DefaultDialer.DialContext(ctx, "ws://127.0.0.1:"+debugPort+browserPath, nil)
	if handshake != nil {
		require.NoError(t, handshake.Body.Close())
	}
	require.NoError(t, err)
	debugURL := "http://127.0.0.1:" + debugPort

	for _, decision := range []string{"approve", "deny"} {
		t.Run(decision, func(t *testing.T) {
			params := url.Values{
				"response_type": {"code"}, "client_id": {clientID},
				"redirect_uri": {clientRedirect.URL + "/callback"}, "scope": {"openid"},
				"state": {"native-" + decision}, "code_challenge": {pkceS256Challenge()},
				"code_challenge_method": {"S256"}, "resource": {cfg.AllowedAudiences[0]},
			}
			create, err := http.NewRequestWithContext(ctx, http.MethodPut, debugURL+"/json/new?about:blank", nil)
			require.NoError(t, err)
			response, err := http.DefaultClient.Do(create)
			if response != nil {
				defer response.Body.Close()
			}
			require.NoError(t, err)
			var target struct {
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			require.NoError(t, json.NewDecoder(response.Body).Decode(&target))
			require.NoError(t, response.Body.Close())
			conn, handshake, err := websocket.DefaultDialer.DialContext(ctx, target.WebSocketDebuggerURL, nil)
			if handshake != nil {
				require.NoError(t, handshake.Body.Close())
			}
			require.NoError(t, err)
			defer conn.Close()
			commandID := 0
			command := func(method string, params any) json.RawMessage {
				t.Helper()
				commandID++
				deadline, _ := ctx.Deadline()
				if pollDeadline := time.Now().Add(5 * time.Second); pollDeadline.Before(deadline) {
					deadline = pollDeadline
				}
				require.NoError(t, conn.SetWriteDeadline(deadline))
				require.NoError(t, conn.SetReadDeadline(deadline))
				require.NoError(t, conn.WriteJSON(map[string]any{"id": commandID, "method": method, "params": params}))
				for {
					var msg struct {
						ID     int             `json:"id"`
						Result json.RawMessage `json:"result"`
						Error  json.RawMessage `json:"error"`
					}
					require.NoError(t, conn.ReadJSON(&msg))
					if msg.ID == commandID {
						require.Empty(t, msg.Error)
						return msg.Result
					}
				}
			}
			evaluate := func(expression string) any {
				t.Helper()
				var result struct {
					Result struct {
						Value any `json:"value"`
					} `json:"result"`
				}
				require.NoError(t, json.Unmarshal(command("Runtime.evaluate", map[string]any{
					"expression": expression, "returnByValue": true,
				}), &result))
				return result.Result.Value
			}
			command("Page.navigate", map[string]any{"url": auth.URL + prefix + "/oauth/authorize?" + params.Encode()})
			// CDP evaluation clicks the actual native HTML submit button; it does
			// not forge Origin, follow redirects, or override the page CSP.
			clicked := false
			for ctx.Err() == nil && !clicked {
				clicked = evaluate(fmt.Sprintf(`(() => { const b = document.querySelector('button[value=%q]'); if (b) { b.click(); return true; } return false; })()`, decision)) == true
				if !clicked {
					time.Sleep(50 * time.Millisecond)
				}
			}
			require.True(t, clicked, "consent form not rendered")
			want := "upstream arrived"
			if decision == "deny" {
				want = "client arrived: access_denied"
			}
			for ctx.Err() == nil {
				if evaluate("document.body.innerText") == want {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatalf("native %s form did not reach %s", decision, want)
		})
	}
}

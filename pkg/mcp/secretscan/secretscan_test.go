// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secretscan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanAndRedactToolCallResult_RedactsKnownCredentialShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
	}{
		{"aws access key", "your key is " + "AKIA" + strings.Repeat("Q", 16) + ", keep it safe"},
		{"github pat", "token: ghp_" + strings.Repeat("a", 36)},
		{"slack token", "xoxb-" + strings.Repeat("1", 10) + "-" + strings.Repeat("a", 16)},
		{"google api key", "AIza" + strings.Repeat("A", 35)},
		{"stripe secret key", "sk_live_" + strings.Repeat("a", 24)},
		{"jwt", "ey" + strings.Repeat("A", 12) + "." + strings.Repeat("B", 12) + "." + strings.Repeat("C", 12)},
		{"pem private key", "-----BEGIN " + "RSA PRIVATE KEY-----" + "\nMIIBogIBAAJ...\n" + "-----END " + "RSA PRIVATE KEY-----"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := callToolResultJSON(t, tc.text)

			result, err := ScanAndRedactToolCallResult(raw)
			require.NoError(t, err)
			require.True(t, result.Matched)
			require.NotContains(t, string(result.Redacted), tc.text)
			require.Contains(t, string(result.Redacted), redactionPlaceholder)
		})
	}
}

func TestScanAndRedactToolCallResult_LeavesOrdinaryTextUnchanged(t *testing.T) {
	t.Parallel()

	raw := callToolResultJSON(t, "the weather in NYC is 72F and sunny")

	result, err := ScanAndRedactToolCallResult(raw)
	require.NoError(t, err)
	require.False(t, result.Matched)
	require.JSONEq(t, string(raw), string(result.Redacted))
}

func TestScanAndRedactToolCallResult_FailsOpenOnMalformedInput(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{not valid json`)

	result, err := ScanAndRedactToolCallResult(raw)
	require.Error(t, err)
	require.False(t, result.Matched)
	require.Equal(t, raw, result.Redacted)
}

func TestScanAndRedactToolCallResult_EmptyInput(t *testing.T) {
	t.Parallel()

	result, err := ScanAndRedactToolCallResult(nil)
	require.NoError(t, err)
	require.False(t, result.Matched)
}

func callToolResultJSON(t *testing.T, text string) json.RawMessage {
	t.Helper()
	payload := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return b
}

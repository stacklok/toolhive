// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/webhook"
)

// testWebhookConfig is a helper that returns a valid webhook.Config for tests.
func testWebhookConfig(name, url string) webhook.Config {
	return webhook.Config{
		Name:          name,
		URL:           url,
		FailurePolicy: webhook.FailurePolicyIgnore,
		TLSConfig: &webhook.TLSConfig{
			InsecureSkipVerify: true,
		},
	}
}

// writeFile is a test helper writing content to a temp file with the given extension.
func writeFile(t *testing.T, dir, ext, content string) string {
	t.Helper()
	f, err := os.CreateTemp(dir, "webhook-*"+ext)
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// ---------------------------------------------------------------------------
// LoadConfig tests
// ---------------------------------------------------------------------------

func TestLoadConfig_YAML_Valid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := `
validating:
  - name: policy
    url: http://localhost/validate
    failure_policy: fail
    tls_config:
      insecure_skip_verify: true
mutating:
  - name: enricher
    url: http://localhost/enrich
    failure_policy: ignore
    tls_config:
      insecure_skip_verify: true
`
	path := writeFile(t, dir, ".yaml", content)

	cfg, err := webhook.LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Validating, 1)
	assert.Equal(t, "policy", cfg.Validating[0].Name)
	require.Len(t, cfg.Mutating, 1)
	assert.Equal(t, "enricher", cfg.Mutating[0].Name)
}

func TestLoadConfig_JSON_Valid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := `{
  "validating": [
    {"name":"v1","url":"http://localhost/v","failure_policy":"ignore","tls_config":{"insecure_skip_verify":true}}
  ],
  "mutating": []
}`
	path := writeFile(t, dir, ".json", content)

	cfg, err := webhook.LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Validating, 1)
	assert.Equal(t, "v1", cfg.Validating[0].Name)
	assert.Empty(t, cfg.Mutating)
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	t.Parallel()
	_, err := webhook.LoadConfig("/this/does/not/exist.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "webhook config file not found")
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Use a tab in indentation - YAML spec forbids tabs in indentation, causing a parse error.
	path := writeFile(t, dir, ".yaml", "validating:\n\t- name: bad")
	_, err := webhook.LoadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse webhook config")
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeFile(t, dir, ".json", "{not valid json")
	_, err := webhook.LoadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse webhook config")
}

func TestLoadConfig_EmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeFile(t, dir, ".yaml", "")

	cfg, err := webhook.LoadConfig(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Validating)
	assert.Empty(t, cfg.Mutating)
}

func TestLoadConfig_YMLExtension(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := `
validating: []
mutating: []
`
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	cfg, err := webhook.LoadConfig(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Validating)
	assert.Empty(t, cfg.Mutating)
}

// ---------------------------------------------------------------------------
// MergeConfigs tests
// ---------------------------------------------------------------------------

func TestMergeConfigs_BasicAppend(t *testing.T) {
	t.Parallel()
	a := &webhook.FileConfig{
		Validating: []webhook.Config{testWebhookConfig("v1", "http://localhost/v1")},
		Mutating:   []webhook.Config{testWebhookConfig("m1", "http://localhost/m1")},
	}
	b := &webhook.FileConfig{
		Validating: []webhook.Config{testWebhookConfig("v2", "http://localhost/v2")},
		Mutating:   []webhook.Config{testWebhookConfig("m2", "http://localhost/m2")},
	}

	merged := webhook.MergeConfigs(a, b)
	require.Len(t, merged.Validating, 2)
	require.Len(t, merged.Mutating, 2)
	assert.Equal(t, "v1", merged.Validating[0].Name)
	assert.Equal(t, "v2", merged.Validating[1].Name)
}

func TestMergeConfigs_LaterOverridesPrior_SameName(t *testing.T) {
	t.Parallel()
	a := &webhook.FileConfig{
		Validating: []webhook.Config{testWebhookConfig("policy", "http://localhost/v1")},
	}
	b := &webhook.FileConfig{
		Validating: []webhook.Config{testWebhookConfig("policy", "http://localhost/v2")},
	}

	merged := webhook.MergeConfigs(a, b)
	require.Len(t, merged.Validating, 1, "duplicate names should be deduplicated")
	assert.Equal(t, "http://localhost/v2", merged.Validating[0].URL, "later URL should win")
}

func TestMergeConfigs_NilInputSkipped(t *testing.T) {
	t.Parallel()
	a := &webhook.FileConfig{
		Validating: []webhook.Config{testWebhookConfig("v1", "http://localhost/v1")},
	}

	merged := webhook.MergeConfigs(nil, a, nil)
	require.Len(t, merged.Validating, 1)
	assert.Equal(t, "v1", merged.Validating[0].Name)
}

func TestMergeConfigs_NoInputs(t *testing.T) {
	t.Parallel()
	merged := webhook.MergeConfigs()
	assert.Empty(t, merged.Validating)
	assert.Empty(t, merged.Mutating)
}

func TestMergeConfigs_OrderPreserved(t *testing.T) {
	t.Parallel()
	a := &webhook.FileConfig{
		Validating: []webhook.Config{
			testWebhookConfig("first", "http://localhost/1"),
			testWebhookConfig("second", "http://localhost/2"),
		},
	}
	b := &webhook.FileConfig{
		Validating: []webhook.Config{
			testWebhookConfig("third", "http://localhost/3"),
		},
	}

	merged := webhook.MergeConfigs(a, b)
	require.Len(t, merged.Validating, 3)
	assert.Equal(t, "first", merged.Validating[0].Name)
	assert.Equal(t, "second", merged.Validating[1].Name)
	assert.Equal(t, "third", merged.Validating[2].Name)
}

// ---------------------------------------------------------------------------
// ValidateConfig tests
// ---------------------------------------------------------------------------

func TestValidateConfig_Valid(t *testing.T) {
	t.Parallel()
	cfg := &webhook.FileConfig{
		Validating: []webhook.Config{testWebhookConfig("v1", "https://example.com/v")},
		Mutating:   []webhook.Config{testWebhookConfig("m1", "https://example.com/m")},
	}
	assert.NoError(t, webhook.ValidateConfig(cfg))
}

func TestValidateConfig_Nil(t *testing.T) {
	t.Parallel()
	assert.NoError(t, webhook.ValidateConfig(nil))
}

func TestValidateConfig_InvalidValidating(t *testing.T) {
	t.Parallel()
	cfg := &webhook.FileConfig{
		Validating: []webhook.Config{
			{Name: "bad-url", URL: "ftp://invalid", FailurePolicy: webhook.FailurePolicyFail},
		},
	}
	err := webhook.ValidateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validating webhook[0]")
}

func TestValidateConfig_InvalidMutating(t *testing.T) {
	t.Parallel()
	cfg := &webhook.FileConfig{
		Mutating: []webhook.Config{
			{Name: "timeout-too-long", URL: "https://example.com/m",
				FailurePolicy: webhook.FailurePolicyIgnore, Timeout: 60 * time.Second},
		},
	}
	err := webhook.ValidateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutating webhook[0]")
}

func TestValidateConfig_CollectsAllErrors(t *testing.T) {
	t.Parallel()
	cfg := &webhook.FileConfig{
		Validating: []webhook.Config{
			{Name: "v-missing-url", URL: "", FailurePolicy: webhook.FailurePolicyFail},
		},
		Mutating: []webhook.Config{
			{Name: "m-missing-url", URL: "", FailurePolicy: webhook.FailurePolicyIgnore},
		},
	}
	err := webhook.ValidateConfig(cfg)
	require.Error(t, err)
	// Both errors should appear in the joined error message
	assert.Contains(t, err.Error(), "validating webhook[0]")
	assert.Contains(t, err.Error(), "mutating webhook[0]")
}

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

// Test pure logic functions only - no I/O, fully parallel

func TestParseNamespaceFromFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      []byte
		want      string
		wantError bool
		errorMsg  string
	}{
		{name: "valid namespace", data: []byte("my-namespace"), want: "my-namespace"},
		{name: "namespace with hyphens", data: []byte("kube-system"), want: "kube-system"},
		{name: "trims trailing newline", data: []byte("my-ns\n"), want: "my-ns"},
		{name: "trims trailing carriage return", data: []byte("my-ns\r\n"), want: "my-ns"},
		{name: "trims multiple trailing newlines", data: []byte("my-ns\n\n"), want: "my-ns"},
		{name: "preserves leading/internal whitespace", data: []byte("  my-ns  "), want: "  my-ns  "},
		{name: "empty file", data: []byte(""), wantError: true, errorMsg: "namespace file is empty"},
		{name: "only newlines", data: []byte("\n\n"), wantError: true, errorMsg: "namespace file is empty"},
		{name: "nil data treated as empty", data: nil, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseNamespaceFromFile(tt.data)

			if tt.wantError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestValidateNamespaceValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ns        string
		source    string
		want      string
		wantError bool
		errorMsg  string
	}{
		{name: "valid namespace", ns: "my-namespace", source: "POD_NAMESPACE", want: "my-namespace"},
		{name: "namespace with special chars", ns: "my-namespace-123", source: "POD_NAMESPACE", want: "my-namespace-123"},
		{name: "empty value", ns: "", source: "POD_NAMESPACE", wantError: true, errorMsg: "not set"},
		{name: "custom source in error", ns: "", source: "CUSTOM_VAR", wantError: true, errorMsg: "CUSTOM_VAR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := validateNamespaceValue(tt.ns, tt.source)

			if tt.wantError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestExtractNamespaceFromKubeconfig(t *testing.T) {
	t.Parallel()

	createConfig := func(currentCtx string, contexts map[string]*api.Context) api.Config {
		return api.Config{
			CurrentContext: currentCtx,
			Contexts:       contexts,
		}
	}

	tests := []struct {
		name      string
		config    api.Config
		want      string
		wantError bool
		errorMsg  string
	}{
		{
			name: "valid context with namespace",
			config: createConfig("test-ctx", map[string]*api.Context{
				"test-ctx": {Namespace: "my-namespace"},
			}),
			want: "my-namespace",
		},
		{
			name: "trims whitespace from namespace",
			config: createConfig("test-ctx", map[string]*api.Context{
				"test-ctx": {Namespace: "  my-namespace  "},
			}),
			want: "my-namespace",
		},
		{
			name:      "no current context",
			config:    createConfig("", map[string]*api.Context{}),
			wantError: true,
			errorMsg:  "no current context set",
		},
		{
			name: "current context not found",
			config: createConfig("missing-ctx", map[string]*api.Context{
				"other-ctx": {Namespace: "my-namespace"},
			}),
			wantError: true,
			errorMsg:  "not found in kubeconfig",
		},
		{
			name: "context without namespace",
			config: createConfig("test-ctx", map[string]*api.Context{
				"test-ctx": {Namespace: ""},
			}),
			wantError: true,
			errorMsg:  "no namespace set",
		},
		{
			name: "context with only whitespace namespace",
			config: createConfig("test-ctx", map[string]*api.Context{
				"test-ctx": {Namespace: "   "},
			}),
			wantError: true,
			errorMsg:  "no namespace set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a ClientConfig from the api.Config
			clientConfig := clientcmd.NewDefaultClientConfig(tt.config, &clientcmd.ConfigOverrides{})

			got, err := extractNamespaceFromKubeconfig(clientConfig)

			if tt.wantError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

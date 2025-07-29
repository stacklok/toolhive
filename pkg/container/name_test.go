package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateContainerBaseName(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		expected string
	}{
		{
			name:     "no namespace, with tag",
			image:    "nginx:latest",
			expected: "nginx",
		},
		{
			name:     "namespace and image, with tag",
			image:    "library/nginx:latest",
			expected: "library-nginx",
		},
		{
			name:     "registry, namespace, image, with tag",
			image:    "docker.io/library/nginx:latest",
			expected: "library-nginx",
		},
		{
			name:     "deep registry, multiple namespaces, image, with tag",
			image:    "quay.io/stacklok/mcp-server:v1",
			expected: "stacklok-mcp-server",
		},
		{
			name:     "simple image, no tag",
			image:    "server",
			expected: "server",
		},
		{
			name:     "namespace, image, no tag",
			image:    "stacklok/server",
			expected: "stacklok-server",
		},
		{
			name:     "multiple slashes, should pick last two",
			image:    "a/b/c/d:foo",
			expected: "c-d",
		},
		{
			name:     "image with special characters",
			image:    "foo/bar@sha256:abcdef",
			expected: "foo-bar-sha256",
		},
		{
			name:     "localhost registry with port",
			image:    "localhost:5000/image:latest",
			expected: "localhost-image",
		},
		{
			name:     "very deep path",
			image:    "x/y/z/w/foo:bar",
			expected: "w-foo",
		},
		{
			name:     "empty image name",
			image:    "",
			expected: "",
		},
		{
			name:     "single slash (should treat as namespace-image)",
			image:    "foo/bar",
			expected: "foo-bar",
		},
		{
			name:     "single image with special chars",
			image:    "my$image:latest",
			expected: "my-image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateContainerBaseName(tt.image)
			assert.Equal(t, tt.expected, got, "generateContainerBaseName(%q)", tt.image)
		})
	}
}

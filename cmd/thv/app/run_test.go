package app

import (
	"testing"
)

func TestDeriveRemoteName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		url      string
		expected string
		wantErr  bool
	}{
		{
			name:     "api.github.com should return github",
			url:      "https://api.github.com",
			expected: "github",
			wantErr:  false,
		},
		{
			name:     "github.com should return github",
			url:      "https://github.com",
			expected: "github",
			wantErr:  false,
		},
		{
			name:     "invalid URL should return error",
			url:      "not-a-url",
			expected: "",
			wantErr:  true,
		},
		{
			name:     "empty URL should return error",
			url:      "",
			expected: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := deriveRemoteName(tt.url)

			if tt.wantErr {
				if err == nil {
					t.Errorf("deriveRemoteName() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("deriveRemoteName() unexpected error: %v", err)
				return
			}

			if got != tt.expected {
				t.Errorf("deriveRemoteName() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"
)

func TestCanonicalUserContext(t *testing.T) {
	t.Parallel()

	base := context.Background()
	unchanged := WithCanonicalUser(base, "")
	if unchanged != base {
		t.Fatal("empty canonical user should return the original context")
	}
	if _, ok := CanonicalUserFromContext(unchanged); ok {
		t.Fatal("empty helper input should not add a canonical user")
	}

	ctx := WithCanonicalUser(base, "user-1")
	if got, ok := CanonicalUserFromContext(ctx); !ok || got != "user-1" {
		t.Fatalf("CanonicalUserFromContext() = (%q, %t), want (%q, true)", got, ok, "user-1")
	}
}

func TestCanonicalUserFromContextDirectValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   any
		wantID  string
		wantSet bool
	}{
		{name: "direct empty string is present", value: "", wantID: "", wantSet: true},
		{name: "wrong type is absent", value: 42, wantID: "", wantSet: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.WithValue(context.Background(), CanonicalUserContextKey{}, tt.value)
			got, ok := CanonicalUserFromContext(ctx)
			if got != tt.wantID || ok != tt.wantSet {
				t.Fatalf("CanonicalUserFromContext() = (%q, %t), want (%q, %t)", got, ok, tt.wantID, tt.wantSet)
			}
		})
	}
}

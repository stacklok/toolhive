// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runtimebin

import "testing"

func TestAvailable_Stub(t *testing.T) {
	t.Parallel()
	if Available() {
		t.Error("Available() = true, want false in stub build")
	}
}

func TestRuntimeSource_Stub(t *testing.T) {
	t.Parallel()
	if src := RuntimeSource(); src != nil {
		t.Error("RuntimeSource() = non-nil, want nil in stub build")
	}
}

func TestVersion_Default(t *testing.T) {
	t.Parallel()
	if Version != "dev" {
		t.Errorf("Version = %q, want %q", Version, "dev")
	}
}

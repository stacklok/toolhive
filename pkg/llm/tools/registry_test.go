// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools_test

import (
	"testing"

	"github.com/stacklok/toolhive/pkg/llm/tools"
)

// fakeAdapter is a minimal Adapter implementation for registry tests.
type fakeAdapter struct {
	name     string
	mode     string
	detected bool
}

func (f *fakeAdapter) Name() string { return f.name }
func (f *fakeAdapter) Mode() string { return f.mode }
func (f *fakeAdapter) Detect() bool { return f.detected }
func (f *fakeAdapter) Apply(_ tools.ApplyConfig) (string, error) {
	return "/fake/" + f.name + "/settings.json", nil
}
func (*fakeAdapter) Revert(_ string) error { return nil }

func TestRegistry_RegisterAndAll(t *testing.T) {
	t.Parallel()
	r := &tools.Registry{}
	a := &fakeAdapter{name: "tool-a", mode: tools.ModeDirect, detected: true}
	b := &fakeAdapter{name: "tool-b", mode: tools.ModeProxy, detected: false}

	r.Register(a)
	r.Register(b)

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 adapters, got %d", len(all))
	}
	if all[0].Name() != "tool-a" || all[1].Name() != "tool-b" {
		t.Errorf("unexpected adapter order: %v, %v", all[0].Name(), all[1].Name())
	}
}

func TestRegistry_Detected(t *testing.T) {
	t.Parallel()
	r := &tools.Registry{}
	r.Register(&fakeAdapter{name: "present", detected: true})
	r.Register(&fakeAdapter{name: "absent", detected: false})
	r.Register(&fakeAdapter{name: "also-present", detected: true})

	detected := r.Detected()
	if len(detected) != 2 {
		t.Fatalf("expected 2 detected adapters, got %d", len(detected))
	}
	if detected[0].Name() != "present" || detected[1].Name() != "also-present" {
		t.Errorf("unexpected detected adapters: %v, %v", detected[0].Name(), detected[1].Name())
	}
}

func TestRegistry_Get(t *testing.T) {
	t.Parallel()
	r := &tools.Registry{}
	r.Register(&fakeAdapter{name: "alpha"})
	r.Register(&fakeAdapter{name: "beta"})

	if got := r.Get("alpha"); got == nil || got.Name() != "alpha" {
		t.Errorf("Get(alpha) = %v, want adapter named alpha", got)
	}
	if got := r.Get("missing"); got != nil {
		t.Errorf("Get(missing) = %v, want nil", got)
	}
}

func TestDefaultRegistry_ContainsExpectedTools(t *testing.T) {
	t.Parallel()
	// Importing this package triggers all init() calls, which register the
	// built-in adapters into the default registry.
	want := []string{"claude-code", "gemini-cli", "cursor", "vscode", "xcode"}
	reg := tools.Default()
	for _, name := range want {
		if reg.Get(name) == nil {
			t.Errorf("default registry missing adapter %q", name)
		}
	}
}

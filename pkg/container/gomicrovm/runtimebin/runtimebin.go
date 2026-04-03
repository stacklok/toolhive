// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runtimebin

import "github.com/stacklok/go-microvm/extract"

// Version is the go-microvm version string used to key the extraction cache.
// It is set via ldflags at build time.
var Version = "dev"

// Available reports whether the runtime binaries are embedded in this build.
func Available() bool {
	return available
}

// RuntimeSource returns an extract.Source that provides go-microvm-runner and
// libkrun. Returns nil when the runtime is not embedded (stub build).
func RuntimeSource() extract.Source {
	if !available {
		return nil
	}
	return extract.RuntimeBundle(Version, runner, libkrun, extraLibs()...)
}

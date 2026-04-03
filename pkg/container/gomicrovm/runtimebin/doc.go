// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package runtimebin optionally embeds go-microvm-runner and libkrun shared
// libraries into the thv binary. When built with the embed_runtime tag, the
// package provides extract.Source instances that extract the embedded binaries
// to a versioned cache directory on first run. Without the tag, all sources
// return nil and Available() returns false.
//
// The embedded binaries are placed in this directory by `task fetch-runtime-embed`
// before building with `-tags embed_runtime`.
package runtimebin

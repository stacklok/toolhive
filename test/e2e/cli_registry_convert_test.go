// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Registry convert CLI", Label("cli", "registry", "e2e"), func() {
	var thvConfig *e2e.TestConfig

	BeforeEach(func() {
		thvConfig = e2e.NewTestConfig()
	})

	const legacyRegistry = `{
		"version": "1.0.0",
		"last_updated": "2026-01-15T10:00:00Z",
		"servers": {
			"filesystem": {
				"description": "A filesystem MCP server",
				"tier": "Official",
				"status": "active",
				"transport": "stdio",
				"image": "ghcr.io/example/filesystem:v1.0.0",
				"tools": ["read_file"],
				"tags": ["filesystem"]
			}
		}
	}`

	const upstreamRegistry = `{
		"$schema": "https://example.com/schema.json",
		"version": "1.0.0",
		"meta": {"last_updated": "2026-01-15T10:00:00Z"},
		"data": {"servers": []}
	}`

	It("converts legacy file via --in/--out flags", func() {
		dir := GinkgoT().TempDir()
		inPath := filepath.Join(dir, "registry.json")
		outPath := filepath.Join(dir, "out.json")
		Expect(os.WriteFile(inPath, []byte(legacyRegistry), 0o600)).To(Succeed())

		e2e.NewTHVCommand(thvConfig, "registry", "convert", "--in", inPath, "--out", outPath).
			ExpectSuccess()

		converted, err := os.ReadFile(outPath)
		Expect(err).ToNot(HaveOccurred())
		var parsed map[string]any
		Expect(json.Unmarshal(converted, &parsed)).To(Succeed())
		data, ok := parsed["data"].(map[string]any)
		Expect(ok).To(BeTrue(), "converted output must wrap servers under data")
		servers, ok := data["servers"].([]any)
		Expect(ok).To(BeTrue())
		Expect(servers).To(HaveLen(1))
	})

	It("converts via stdin to stdout when no flags are given", func() {
		stdout, _ := e2e.NewTHVCommand(thvConfig, "registry", "convert").
			WithStdin(legacyRegistry).
			ExpectSuccess()

		var parsed map[string]any
		Expect(json.Unmarshal([]byte(stdout), &parsed)).To(Succeed())
		Expect(parsed).To(HaveKey("data"))
	})

	It("rewrites in place and creates a .bak by default", func() {
		dir := GinkgoT().TempDir()
		inPath := filepath.Join(dir, "registry.json")
		Expect(os.WriteFile(inPath, []byte(legacyRegistry), 0o600)).To(Succeed())

		e2e.NewTHVCommand(thvConfig, "registry", "convert", "--in", inPath, "--in-place").
			ExpectSuccess()

		updated, err := os.ReadFile(inPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(updated)).To(ContainSubstring(`"data"`),
			"in-place output must be in upstream format")

		bak, err := os.ReadFile(inPath + ".bak")
		Expect(err).ToNot(HaveOccurred())
		Expect(string(bak)).To(ContainSubstring(`"servers": {`),
			".bak must hold the legacy original")
	})

	It("emits a friendly stderr message and exits 0 when input is already upstream", func() {
		dir := GinkgoT().TempDir()
		inPath := filepath.Join(dir, "registry.json")
		Expect(os.WriteFile(inPath, []byte(upstreamRegistry), 0o600)).To(Succeed())

		_, stderr := e2e.NewTHVCommand(thvConfig, "registry", "convert", "--in", inPath).
			ExpectSuccess()
		Expect(stderr).To(ContainSubstring("already in upstream format"))
	})
})

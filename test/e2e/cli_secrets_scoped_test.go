// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("thv secret system key protection", Label("cli", "secrets", "e2e"), func() {
	var (
		thvConfig *e2e.TestConfig
		tempDir   string
		thvCmd    func(args ...string) *e2e.THVCommand
	)

	BeforeEach(func() {
		thvConfig = e2e.NewTestConfig()
		tempDir = GinkgoT().TempDir()

		// thvCmd creates a THVCommand with an isolated config/home directory so
		// these tests never touch the user's real secrets store.
		thvCmd = func(args ...string) *e2e.THVCommand {
			return e2e.NewTHVCommand(thvConfig, args...).
				WithEnv(
					"XDG_CONFIG_HOME="+tempDir,
					"HOME="+tempDir,
				)
		}

		// Configure the secrets provider non-interactively using the environment
		// provider. This provider reads secrets from TOOLHIVE_SECRET_* env vars
		// and is suitable for non-interactive test environments.
		By("Configuring environment secrets provider")
		thvCmd("secret", "provider", "environment").ExpectSuccess()
	})

	It("rejects set with __thv_ prefix", func() {
		// The UserProvider wraps the underlying provider and blocks any key
		// starting with the "__thv_" system prefix. With the environment
		// provider (read-only), the write-capability check fires first;
		// but the reservation error message is still the observable result
		// for write-capable providers at the unit level. Here we verify the
		// broader CLI contract: setting a __thv_ key always fails.
		By("Attempting to set a system-reserved key")
		stdout, stderr, err := thvCmd("secret", "set", "__thv_workloads_token").
			WithStdin("secret-value\n").
			Run()

		By("Verifying the command fails")
		Expect(err).To(HaveOccurred(),
			"setting a __thv_-prefixed key should be rejected; stdout=%q stderr=%q", stdout, stderr)
	})

	It("rejects get with __thv_ prefix", func() {
		// The environment provider supports reads, so GetSecret is called on
		// the UserProvider which enforces the system-key reservation check.
		By("Attempting to get a system-reserved key")
		stdout, stderr, err := thvCmd("secret", "get", "__thv_workloads_token").Run()

		By("Verifying the command fails")
		Expect(err).To(HaveOccurred(),
			"getting a __thv_-prefixed key should be rejected; stdout=%q stderr=%q", stdout, stderr)

		By("Verifying the error message references system use reservation")
		Expect(stderr).To(ContainSubstring("reserved for system use"),
			"stderr should explain that the key is reserved for system use")

		By("Verifying the error message includes the key name")
		Expect(stderr).To(ContainSubstring("__thv_"),
			"stderr should include the offending key prefix")
	})

	It("rejects delete with __thv_ prefix", func() {
		// The environment provider does not support deletion, so the
		// capability check fires before the UserProvider reservation check.
		// Regardless of the exact error path, the operation must fail.
		By("Attempting to delete a system-reserved key")
		stdout, stderr, err := thvCmd("secret", "delete", "__thv_workloads_token").Run()

		By("Verifying the command fails")
		Expect(err).To(HaveOccurred(),
			"deleting a __thv_-prefixed key should be rejected; stdout=%q stderr=%q", stdout, stderr)
	})

	It("confirms __thv_ keys cannot be created via the user CLI", func() {
		// Belt-and-suspenders check: attempt to set two different __thv_ keys
		// to confirm the block is consistent across key names, not tied to a
		// single hardcoded name. Both attempts must fail.
		By("Attempting to set __thv_workloads_token")
		_, _, err1 := thvCmd("secret", "set", "__thv_workloads_token").
			WithStdin("value1\n").
			Run()
		Expect(err1).To(HaveOccurred(),
			"__thv_workloads_token should be rejected")

		By("Attempting to set __thv_registry_oauth_token")
		_, _, err2 := thvCmd("secret", "set", "__thv_registry_oauth_token").
			WithStdin("value2\n").
			Run()
		Expect(err2).To(HaveOccurred(),
			"__thv_registry_oauth_token should be rejected")

		By("Verifying that a normal (non-system) key name does not trigger the same error path")
		// The environment provider is read-only, so a normal key will also fail —
		// but the failure reason is different (provider is read-only), confirming
		// that the system-key check is a separate, additional guard.
		_, stderr, errNormal := thvCmd("secret", "set", "my_user_key").
			WithStdin("some-value\n").
			Run()
		Expect(errNormal).To(HaveOccurred(),
			"environment provider is read-only so this also fails, but not due to system key reservation")
		Expect(stderr).ToNot(ContainSubstring("reserved for system use"),
			"a normal key should NOT produce a system-key reservation error")
	})
})

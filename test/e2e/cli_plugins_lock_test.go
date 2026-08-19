// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/registry"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Plugins CLI lock file exit codes (RFC THV-0080)", Label("api", "cli", "plugins", "plugins-lock", "e2e"), func() {
	var (
		config    *e2e.ServerConfig
		apiServer *e2e.Server
		thvConfig *e2e.TestConfig
	)

	BeforeEach(func() {
		config = e2e.NewServerConfig()
		config.ExtraEnv = []string{"TOOLHIVE_PLUGINS_LOCK_ENABLED=true"}
		apiServer = e2e.StartServer(config)
		thvConfig = e2e.NewTestConfig()
	})

	thvPluginCmd := func(args ...string) *e2e.THVCommand {
		fullArgs := append([]string{"ai-plugin"}, args...)
		return e2e.NewTHVCommand(thvConfig, fullArgs...).
			WithEnv("TOOLHIVE_API_URL=" + apiServer.BaseURL())
	}

	exitCodeOf := func(err error) int {
		var exitErr *exec.ExitError
		ExpectWithOffset(1, errors.As(err, &exitErr)).To(BeTrue(), "expected an *exec.ExitError, got %T: %v", err, err)
		return exitErr.ExitCode()
	}

	Describe("thv ai-plugin sync --check", func() {
		It("exits 0 when the project matches its lock file", func() {
			projectRoot := makeE2EProjectRoot()
			pluginName := "cli-lock-clean-plugin"

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushPlugin(apiServer, ociRegistry, pluginName, "A clean plugin for CLI exit code testing")

			installResp := installPlugin(apiServer, installPluginE2ERequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot, Clients: []string{"claude-code"},
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			stdout, _ := thvPluginCmd("sync", "--check", "--project-root", projectRoot).ExpectSuccess()
			Expect(stdout).To(ContainSubstring("Up to date"))
			Expect(stdout).To(ContainSubstring(pluginName))
		})

		It("exits 2 when the project has drifted from its lock file", func() {
			projectRoot := makeE2EProjectRoot()
			pluginName := "cli-lock-drifted-plugin"

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushPlugin(apiServer, ociRegistry, pluginName, "A drifted plugin for CLI exit code testing")

			installResp := installPlugin(apiServer, installPluginE2ERequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot, Clients: []string{"claude-code"},
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			By("Deleting the installed files so the project drifts from the lock file")
			pluginDir := filepath.Join(projectRoot, ".claude", "plugins", pluginName)
			Expect(os.RemoveAll(pluginDir)).To(Succeed())

			_, _, err := thvPluginCmd("sync", "--check", "--project-root", projectRoot).Run()
			Expect(err).To(HaveOccurred())
			Expect(exitCodeOf(err)).To(Equal(2))
		})
	})

	Describe("thv ai-plugin sync without --yes", func() {
		It("exits 4 when running non-interactively without --yes", func() {
			projectRoot := makeE2EProjectRoot()

			_, _, err := thvPluginCmd("sync", "--project-root", projectRoot).Run()
			Expect(err).To(HaveOccurred(), "a non-interactive sync without --yes must refuse rather than proceed silently")
			Expect(exitCodeOf(err)).To(Equal(4))
		})
	})

	Describe("thv ai-plugin sync --yes", func() {
		It("exits 0 and proceeds without prompting", func() {
			projectRoot := makeE2EProjectRoot()

			stdout, _ := thvPluginCmd("sync", "--yes", "--project-root", projectRoot).ExpectSuccess()
			Expect(stdout).To(ContainSubstring("Nothing to sync"))
		})
	})

	Describe("thv ai-plugin sync partial failure", func() {
		It("exits 3 when a pinned plugin cannot be reinstalled", func() {
			projectRoot := makeE2EProjectRoot()
			pluginName := "cli-lock-exit3-plugin"

			ociRegistry := httptest.NewServer(registry.New())
			ociRef := buildAndPushPlugin(apiServer, ociRegistry, pluginName, "A plugin whose registry will vanish")

			installResp := installPlugin(apiServer, installPluginE2ERequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot, Clients: []string{"claude-code"},
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			By("Tampering with the installed files and killing the registry, so the repair reinstall must fail")
			hello := filepath.Join(projectRoot, ".claude", "plugins", pluginName, "commands", "hello.md")
			Expect(os.WriteFile(hello, []byte("tampered"), 0o644)).To(Succeed())
			ociRegistry.Close()

			_, _, err := thvPluginCmd("sync", "--yes", "--project-root", projectRoot).Run()
			Expect(err).To(HaveOccurred(), "a failed repair must not exit 0")
			Expect(exitCodeOf(err)).To(Equal(3), "an operational failure is exit 3, not a freshness signal")
		})
	})

	Describe("thv ai-plugin sync --check on a fresh clone", func() {
		It("exits 2 when the lock file has entries but nothing is installed", func() {
			projectRoot := makeE2EProjectRoot()
			pluginName := "cli-lock-freshclone-plugin"

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushPlugin(apiServer, ociRegistry, pluginName, "A plugin for the fresh-clone gate")

			installResp := installPlugin(apiServer, installPluginE2ERequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot, Clients: []string{"claude-code"},
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			By("Simulating a fresh clone: the committed lock file survives, local install state does not")
			lockPath := filepath.Join(projectRoot, "toolhive.lock.yaml")
			lockBytes, err := os.ReadFile(lockPath) //nolint:gosec // fixed test path
			Expect(err).ToNot(HaveOccurred())
			uninstallResp := uninstallScopedPlugin(apiServer, pluginName, projectRoot)
			defer uninstallResp.Body.Close()
			Expect(uninstallResp.StatusCode).To(Equal(http.StatusNoContent))
			Expect(os.WriteFile(lockPath, lockBytes, 0o644)).To(Succeed())

			_, _, cmdErr := thvPluginCmd("sync", "--check", "--project-root", projectRoot).Run()
			Expect(cmdErr).To(HaveOccurred(), "the CI gate must not green-light a checkout with nothing installed")
			Expect(exitCodeOf(cmdErr)).To(Equal(2))
		})
	})
})

type installPluginE2ERequest struct {
	Name        string   `json:"name"`
	Scope       string   `json:"scope,omitempty"`
	ProjectRoot string   `json:"project_root,omitempty"`
	Clients     []string `json:"clients,omitempty"`
}

func installPlugin(server *e2e.Server, req installPluginE2ERequest) *http.Response {
	jsonData, err := json.Marshal(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/plugins",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func uninstallScopedPlugin(server *e2e.Server, name, projectRoot string) *http.Response {
	u := fmt.Sprintf("%s/api/v1beta/plugins/%s?scope=project&project_root=%s",
		server.BaseURL(), name, url.QueryEscape(projectRoot))
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func buildPlugin(server *e2e.Server, path, tag string) *http.Response {
	reqBody := struct {
		Path string `json:"path"`
		Tag  string `json:"tag,omitempty"`
	}{Path: path, Tag: tag}
	jsonData, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/plugins/build",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func pushPlugin(server *e2e.Server, reference string) *http.Response {
	reqBody := struct {
		Reference string `json:"reference"`
	}{Reference: reference}
	jsonData, err := json.Marshal(reqBody)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/plugins/push",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

func createTestPluginDir(pluginName, description string) string {
	parentDir := GinkgoT().TempDir()
	pluginDir := filepath.Join(parentDir, pluginName)
	ExpectWithOffset(1, os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o755)).To(Succeed())
	ExpectWithOffset(1, os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o755)).To(Succeed())

	manifest := fmt.Sprintf(`{
  "name": %q,
  "description": %q,
  "version": "0.1.0",
  "license": "Apache-2.0",
  "keywords": ["test"]
}`, pluginName, description)
	ExpectWithOffset(1, os.WriteFile(
		filepath.Join(pluginDir, plugins.ManifestPath),
		[]byte(manifest),
		0o644,
	)).To(Succeed())
	ExpectWithOffset(1, os.WriteFile(
		filepath.Join(pluginDir, "commands", "hello.md"),
		[]byte("# hello\n"),
		0o644,
	)).To(Succeed())

	return pluginDir
}

func buildAndPushPlugin(server *e2e.Server, ociRegistry *httptest.Server, pluginName, description string) string {
	ociRef := fmt.Sprintf("%s/e2e-test/%s:v0.1.0", ociRegistry.Listener.Addr().String(), pluginName)

	pluginDir := createTestPluginDir(pluginName, description)
	buildResp := buildPlugin(server, pluginDir, ociRef)
	defer buildResp.Body.Close()
	ExpectWithOffset(1, buildResp.StatusCode).To(Equal(http.StatusOK))

	pushResp := pushPlugin(server, ociRef)
	defer pushResp.Body.Close()
	ExpectWithOffset(1, pushResp.StatusCode).To(Equal(http.StatusNoContent))

	return ociRef
}

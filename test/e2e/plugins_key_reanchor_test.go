// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/registry"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/server/discovery"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage/sqlite"
	"github.com/stacklok/toolhive/test/e2e"
)

func pushPluginWithKey(server *e2e.Server, reference, privateKeyPath string) {
	discoveryPath := filepath.Join(
		sharedConfigDir, ".local", "state", "toolhive", "server", "server.json",
	)
	discoveryBytes, err := os.ReadFile(discoveryPath)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	var serverInfo discovery.ServerInfo
	ExpectWithOffset(1, json.Unmarshal(discoveryBytes, &serverInfo)).To(Succeed())
	ExpectWithOffset(1, serverInfo.KeySigningCapability).ToNot(BeEmpty())

	reqBody, err := json.Marshal(struct {
		Reference string `json:"reference"`
		Key       string `json:"key"`
	}{Reference: reference, Key: privateKeyPath})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	req, err := http.NewRequest(
		http.MethodPost,
		server.BaseURL()+"/api/v1beta/plugins/push",
		bytes.NewReader(reqBody),
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(discovery.KeySigningCapabilityHeader, serverInfo.KeySigningCapability)
	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	defer resp.Body.Close()
	ExpectWithOffset(1, resp.StatusCode).To(Equal(http.StatusNoContent))
}

func buildAndPushPluginWithKey(
	server *e2e.Server,
	ociRegistry *httptest.Server,
	pluginName, description string,
	key skillSigningKey,
) string {
	ociRef := fmt.Sprintf("%s/e2e-test/%s:v0.1.0", ociRegistry.Listener.Addr().String(), pluginName)
	pluginDir := createTestPluginDir(pluginName, description)
	buildResp := buildPlugin(server, pluginDir, ociRef)
	defer buildResp.Body.Close()
	ExpectWithOffset(1, buildResp.StatusCode).To(Equal(http.StatusOK))
	pushPluginWithKey(server, ociRef, key.privatePath)
	return ociRef
}

func markInstalledPluginUnmanaged(projectRoot, pluginName string) {
	dbPath := filepath.Join(sharedConfigDir, ".local", "state", "toolhive", "toolhive.db")
	db, err := sqlite.Open(context.Background(), dbPath)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	store := sqlite.NewPluginStore(db)

	installed, err := store.Get(context.Background(), pluginName, plugins.ScopeProject, projectRoot)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	installed.Managed = false
	ExpectWithOffset(1, store.Update(context.Background(), installed)).To(Succeed())
	ExpectWithOffset(1, store.Close()).To(Succeed())

	root, err := lockfile.OpenRoot(projectRoot)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	ExpectWithOffset(1, lockfile.RemovePluginEntry(root, pluginName)).To(Succeed())
}

type upgradePluginsE2ERequest struct {
	ProjectRoot       string   `json:"project_root"`
	Names             []string `json:"names,omitempty"`
	AllowSignerChange bool     `json:"allow_signer_change,omitempty"`
	PublicKey         string   `json:"public_key,omitempty"`
}

func upgradePluginsAPI(server *e2e.Server, req upgradePluginsE2ERequest) *http.Response {
	jsonData, err := json.Marshal(req)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	resp, err := http.Post(
		server.BaseURL()+"/api/v1beta/plugins/upgrade",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return resp
}

var _ = Describe("Plugins key trust workflows",
	Label("api", "api-registry", "cli", "plugins", "plugins-lock", "plugins-key-trust", "e2e"), func() {
		var apiServer *e2e.Server

		BeforeEach(func() {
			config := e2e.NewServerConfig()
			config.ExtraEnv = append(config.ExtraEnv,
				"TOOLHIVE_SKIP_DESKTOP_CHECK=true",
				"XDG_DATA_HOME="+filepath.Join(sharedConfigDir, ".local", "share"),
				"XDG_STATE_HOME="+filepath.Join(sharedConfigDir, ".local", "state"),
			)
			apiServer = e2e.StartServer(config)
		})

		It("re-anchors a key-pinned plugin through the upgrade API", func() {
			projectRoot := makeE2EProjectRoot()
			pluginName := "key-reanchor-api-plugin"
			oldKey := generateP256SkillSigningKey()
			newKey := generateP256SkillSigningKey()

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushPluginWithKey(
				apiServer, ociRegistry, pluginName, "A plugin signed by the original key", oldKey,
			)

			installResp := installPlugin(apiServer, installPluginE2ERequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot,
				Clients: []string{"claude-code"}, PublicKey: oldKey.encodedPublic,
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			root, err := lockfile.OpenRoot(projectRoot)
			Expect(err).ToNot(HaveOccurred())
			beforeLock, err := lockfile.Load(root)
			Expect(err).ToNot(HaveOccurred())
			before, ok := beforeLock.GetPlugin(pluginName)
			Expect(ok).To(BeTrue())
			Expect(before.Provenance).ToNot(BeNil())
			Expect(before.Provenance.PublicKey).To(Equal(oldKey.encodedPublic))

			updatedDir := createTestPluginDir(pluginName, "A plugin signed by the rotated key")
			rebuildResp := buildPlugin(apiServer, updatedDir, ociRef)
			defer rebuildResp.Body.Close()
			Expect(rebuildResp.StatusCode).To(Equal(http.StatusOK))
			pushPluginWithKey(apiServer, ociRef, newKey.privatePath)

			upgradeResp := upgradePluginsAPI(apiServer, upgradePluginsE2ERequest{
				ProjectRoot: projectRoot, Names: []string{pluginName},
				AllowSignerChange: true, PublicKey: newKey.encodedPublic,
			})
			defer upgradeResp.Body.Close()
			Expect(upgradeResp.StatusCode).To(Equal(http.StatusOK))
			var result upgradeResultResponse
			Expect(json.NewDecoder(upgradeResp.Body).Decode(&result)).To(Succeed())
			Expect(result.Outcomes).To(HaveLen(1))
			Expect(result.Outcomes[0].Status).To(Equal("upgraded"))
			Expect(result.Outcomes[0].TrustAnchorChanged).To(BeTrue())

			afterLock, err := lockfile.Load(root)
			Expect(err).ToNot(HaveOccurred())
			after, ok := afterLock.GetPlugin(pluginName)
			Expect(ok).To(BeTrue())
			Expect(after.Digest).ToNot(Equal(before.Digest))
			Expect(after.Provenance).ToNot(BeNil())
			Expect(after.Provenance.PublicKey).To(Equal(newKey.encodedPublic))
			Expect(after.Unsigned).To(BeFalse())

			cleanupResp := uninstallScopedPlugin(apiServer, pluginName, projectRoot)
			defer cleanupResp.Body.Close()
			Expect(cleanupResp.StatusCode).To(Equal(http.StatusNoContent))
		})

		It("adopts a key-signed unmanaged plugin through sync --public-key", func() {
			projectRoot := makeE2EProjectRoot()
			pluginName := "key-adopt-cli-plugin"
			key := generateP256SkillSigningKey()

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushPluginWithKey(
				apiServer, ociRegistry, pluginName, "A key-signed plugin to adopt", key,
			)

			installResp := installPlugin(apiServer, installPluginE2ERequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot,
				Clients: []string{"claude-code"}, PublicKey: key.encodedPublic,
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))
			markInstalledPluginUnmanaged(projectRoot, pluginName)

			stdout, stderr, err := e2e.NewTHVCommand(
				e2e.NewTestConfig(), "ai-plugin", "sync",
				"--adopt", "--yes", "--project-root", projectRoot, "--public-key", key.publicPath,
			).WithEnv(
				"TOOLHIVE_API_URL="+apiServer.BaseURL(),
				"TOOLHIVE_SKIP_DESKTOP_CHECK=true",
				"HOME="+sharedConfigDir,
				"XDG_CONFIG_HOME="+sharedConfigDir,
				"XDG_DATA_HOME="+filepath.Join(sharedConfigDir, ".local", "share"),
				"XDG_STATE_HOME="+filepath.Join(sharedConfigDir, ".local", "state"),
			).Run()
			Expect(err).ToNot(HaveOccurred(), "stdout: %s\nstderr: %s", stdout, stderr)
			Expect(stdout).To(ContainSubstring(pluginName))

			root, err := lockfile.OpenRoot(projectRoot)
			Expect(err).ToNot(HaveOccurred())
			lf, err := lockfile.Load(root)
			Expect(err).ToNot(HaveOccurred())
			entry, ok := lf.GetPlugin(pluginName)
			Expect(ok).To(BeTrue())
			Expect(entry.Provenance).ToNot(BeNil())
			Expect(entry.Provenance.PublicKey).To(Equal(key.encodedPublic))
			Expect(entry.Unsigned).To(BeFalse())

			cleanupResp := uninstallScopedPlugin(apiServer, pluginName, projectRoot)
			defer cleanupResp.Body.Close()
			Expect(cleanupResp.StatusCode).To(Equal(http.StatusNoContent))
		})
	})

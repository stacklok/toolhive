// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/registry"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sigstore/sigstore/pkg/cryptoutils"

	"github.com/stacklok/toolhive/pkg/server/discovery"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	"github.com/stacklok/toolhive/pkg/storage/sqlite"
	"github.com/stacklok/toolhive/test/e2e"
)

type skillSigningKey struct {
	privatePath   string
	publicPath    string
	encodedPublic string
}

func generateP256SkillSigningKey() skillSigningKey {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	privatePEM, err := cryptoutils.MarshalPrivateKeyToPEM(privateKey)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	publicPEM, err := cryptoutils.MarshalPublicKeyToPEM(privateKey.Public())
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	encodedPublic, err := verifier.EncodePublicKey(publicPEM)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	keyDir := GinkgoT().TempDir()
	privatePath := filepath.Join(keyDir, "cosign.key")
	publicPath := filepath.Join(keyDir, "cosign.pub")
	ExpectWithOffset(1, os.WriteFile(privatePath, privatePEM, 0o600)).To(Succeed())
	ExpectWithOffset(1, os.WriteFile(publicPath, publicPEM, 0o644)).To(Succeed())
	return skillSigningKey{
		privatePath:   privatePath,
		publicPath:    publicPath,
		encodedPublic: encodedPublic,
	}
}

func pushSkillWithKey(server *e2e.Server, reference, privateKeyPath string) {
	discoveryPath := filepath.Join(
		sharedConfigDir, ".local", "state", "toolhive", "server", "server.json",
	)
	discoveryBytes, err := os.ReadFile(discoveryPath)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	var serverInfo discovery.ServerInfo
	ExpectWithOffset(1, json.Unmarshal(discoveryBytes, &serverInfo)).To(Succeed())
	ExpectWithOffset(1, serverInfo.KeySigningCapability).ToNot(BeEmpty())

	reqBody, err := json.Marshal(pushSkillRequest{Reference: reference, Key: privateKeyPath})
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	req, err := http.NewRequest(
		http.MethodPost,
		server.BaseURL()+"/api/v1beta/skills/push",
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

func buildAndPushSkillWithKey(
	server *e2e.Server,
	ociRegistry *httptest.Server,
	skillName, description string,
	key skillSigningKey,
) string {
	ociRef := fmt.Sprintf("%s/e2e-test/%s:v0.1.0", ociRegistry.Listener.Addr().String(), skillName)
	skillDir := createTestSkillDir(skillName, description)
	buildResp := buildSkill(server, skillDir, ociRef)
	defer buildResp.Body.Close()
	ExpectWithOffset(1, buildResp.StatusCode).To(Equal(http.StatusOK))
	pushSkillWithKey(server, ociRef, key.privatePath)
	return ociRef
}

func markInstalledSkillUnmanaged(projectRoot, skillName string) {
	dbPath := filepath.Join(sharedConfigDir, ".local", "state", "toolhive", "toolhive.db")
	db, err := sqlite.Open(context.Background(), dbPath)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	store := sqlite.NewSkillStore(db)

	installed, err := store.Get(context.Background(), skillName, skills.ScopeProject, projectRoot)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	installed.Managed = false
	ExpectWithOffset(1, store.Update(context.Background(), installed)).To(Succeed())
	ExpectWithOffset(1, store.Close()).To(Succeed())

	root, err := lockfile.OpenRoot(projectRoot)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	ExpectWithOffset(1, lockfile.RemoveEntry(root, skillName)).To(Succeed())
}

var _ = Describe("Skills key trust workflows",
	Label("api", "api-registry", "cli", "skills", "skills-lock", "skills-key-trust", "e2e"), func() {
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

		It("re-anchors a key-pinned skill through the upgrade API", func() {
			projectRoot := makeE2EProjectRoot()
			skillName := "key-reanchor-api-skill"
			oldKey := generateP256SkillSigningKey()
			newKey := generateP256SkillSigningKey()

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushSkillWithKey(
				apiServer, ociRegistry, skillName, "A skill signed by the original key", oldKey,
			)

			installResp := installSkill(apiServer, installSkillRequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot, PublicKey: oldKey.encodedPublic,
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))

			root, err := lockfile.OpenRoot(projectRoot)
			Expect(err).ToNot(HaveOccurred())
			beforeLock, err := lockfile.Load(root)
			Expect(err).ToNot(HaveOccurred())
			before, ok := beforeLock.Get(skillName)
			Expect(ok).To(BeTrue())
			Expect(before.Provenance).ToNot(BeNil())
			Expect(before.Provenance.PublicKey).To(Equal(oldKey.encodedPublic))

			updatedDir := createTestSkillDir(skillName, "A skill signed by the rotated key")
			rebuildResp := buildSkill(apiServer, updatedDir, ociRef)
			defer rebuildResp.Body.Close()
			Expect(rebuildResp.StatusCode).To(Equal(http.StatusOK))
			pushSkillWithKey(apiServer, ociRef, newKey.privatePath)

			upgradeResp := upgradeSkills(apiServer, upgradeSkillsRequest{
				ProjectRoot:       projectRoot,
				Names:             []string{skillName},
				AllowSignerChange: true,
				PublicKey:         newKey.encodedPublic,
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
			after, ok := afterLock.Get(skillName)
			Expect(ok).To(BeTrue())
			Expect(after.Digest).ToNot(Equal(before.Digest))
			Expect(after.Provenance).ToNot(BeNil())
			Expect(after.Provenance.PublicKey).To(Equal(newKey.encodedPublic))
			Expect(after.Unsigned).To(BeFalse())

			cleanupResp := uninstallScopedSkill(apiServer, skillName, projectRoot)
			defer cleanupResp.Body.Close()
			Expect(cleanupResp.StatusCode).To(Equal(http.StatusNoContent))
		})

		It("adopts a key-signed unmanaged skill through sync --public-key", func() {
			projectRoot := makeE2EProjectRoot()
			skillName := "key-adopt-cli-skill"
			key := generateP256SkillSigningKey()

			ociRegistry := httptest.NewServer(registry.New())
			DeferCleanup(ociRegistry.Close)
			ociRef := buildAndPushSkillWithKey(
				apiServer, ociRegistry, skillName, "A key-signed skill to adopt", key,
			)

			installResp := installSkill(apiServer, installSkillRequest{
				Name: ociRef, Scope: "project", ProjectRoot: projectRoot, PublicKey: key.encodedPublic,
			})
			defer installResp.Body.Close()
			Expect(installResp.StatusCode).To(Equal(http.StatusCreated))
			markInstalledSkillUnmanaged(projectRoot, skillName)

			stdout, stderr, err := e2e.NewTHVCommand(
				e2e.NewTestConfig(), "skill", "sync",
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
			Expect(stdout).To(ContainSubstring(skillName))

			root, err := lockfile.OpenRoot(projectRoot)
			Expect(err).ToNot(HaveOccurred())
			lf, err := lockfile.Load(root)
			Expect(err).ToNot(HaveOccurred())
			entry, ok := lf.Get(skillName)
			Expect(ok).To(BeTrue())
			Expect(entry.Provenance).ToNot(BeNil())
			Expect(entry.Provenance.PublicKey).To(Equal(key.encodedPublic))
			Expect(entry.Unsigned).To(BeFalse())

			cleanupResp := uninstallScopedSkill(apiServer, skillName, projectRoot)
			defer cleanupResp.Body.Close()
			Expect(cleanupResp.StatusCode).To(Equal(http.StatusNoContent))
		})
	})
